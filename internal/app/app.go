// Package app merakit dependensi produksi (Postgres, Redis, MinIO, asynq, SMTP, gateway pembayaran midtrans|xendit|fake) dari Config.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"

	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/clock"
	"tixgo/backend/internal/config"
	"tixgo/backend/internal/jobs"
	"tixgo/backend/internal/notify"
	"tixgo/backend/internal/payment"
	"tixgo/backend/internal/platform"
	"tixgo/backend/internal/ratelimit"
	"tixgo/backend/internal/repository/sqlcgen"
	"tixgo/backend/internal/service"
	"tixgo/backend/internal/storage"
)

// Built = hasil perakitan + fungsi penutup sumber daya.
type Built struct {
	Svc      *service.Services
	Reg      *jobs.Registry
	Asynq    asynq.RedisClientOpt
	Close    func()
	Enqueuer *jobs.AsynqClient
}

// NewLogger membuat slog sesuai LOG_LEVEL dan APP_ENV (JSON di produksi).
func NewLogger(cfg *config.Config) *slog.Logger {
	var lvl slog.Level
	_ = lvl.UnmarshalText([]byte(cfg.LogLevel))
	opts := &slog.HandlerOptions{Level: lvl}
	if cfg.IsProd() {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}

// Build menyambungkan semua infrastruktur (gagal keras bila ada yang tidak terjangkau).
func Build(ctx context.Context, cfg *config.Config, log *slog.Logger) (*Built, error) {
	pool, err := platform.NewPostgres(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	rdb, err := platform.NewRedisDB(ctx, cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	if err != nil {
		pool.Close()
		return nil, err
	}
	store, err := storage.NewMinIO(ctx, cfg.S3Endpoint, cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3Bucket, cfg.S3UseSSL)
	if err != nil {
		pool.Close()
		_ = rdb.Close()
		return nil, err
	}
	box, err := auth.NewBox(cfg.EncKey)
	if err != nil {
		return nil, err
	}
	ropt := asynq.RedisClientOpt{Addr: cfg.RedisAddr, Password: cfg.RedisPassword, DB: cfg.RedisDB}
	aq := jobs.NewAsynqClient(ropt)
	reg := jobs.NewRegistry()

	var mailer notify.Mailer = notify.LogMailer{Log: log}
	if cfg.SMTPHost != "" {
		mailer = notify.NewSMTP(cfg.SMTPHost, cfg.SMTPPort, cfg.SMTPUser, cfg.SMTPPass, cfg.SMTPFrom)
	}
	var gw payment.Gateway = payment.Unconfigured{}
	switch {
	case cfg.PaymentGateway == "fake": // eksplisit & hanya non-production (divalidasi config); /dev/payments untuk simulasi bayar
		gw = payment.NewFake(clock.Real.Now)
		log.Warn("PAYMENT_GATEWAY=fake aktif: pembayaran DISIMULASIKAN (bukan Midtrans)")
	case cfg.PaymentGateway == "xendit":
		gw = payment.NewXendit(cfg.XenditSecretKey, cfg.XenditCallbackToken, cfg.XenditBaseURL, nil)
	case cfg.MidtransServerKey != "":
		gw = payment.NewMidtrans(cfg.MidtransServerKey, cfg.MidtransEnv, cfg.MidtransSnapURL, cfg.MidtransCoreURL, nil)
	case cfg.IsProd():
		return nil, fmt.Errorf("MIDTRANS_SERVER_KEY wajib di production")
	}
	d := &service.Deps{
		Cfg: cfg, Log: log, Pool: pool, Redis: rdb, Clock: clock.Real,
		Tokens: auth.NewTokens(cfg.JWTSecret, cfg.JWTAccessTTL, clock.Real),
		Hasher: auth.NewPasswordHasher(auth.PasswordParams{MemoryKB: cfg.ArgonMemoryKB, Time: cfg.ArgonTime, Threads: cfg.ArgonThreads}),
		Box:    box, Mailer: mailer, SMS: notify.LogSMS{Log: log}, Push: pushSender(cfg), Gateway: gw, Store: store,
		Jobs: aq, Reg: reg, Limiter: ratelimit.New(rdb, !cfg.RateLimitDisabled), Q: sqlcgen.New(pool),
	}
	svc := service.New(d)
	return &Built{Svc: svc, Reg: reg, Asynq: ropt, Enqueuer: aq, Close: func() {
		_ = aq.Close()
		_ = rdb.Close()
		pool.Close()
	}}, nil
}

var _ = redis.Nil

func pushSender(cfg *config.Config) notify.PushSender {
	if cfg.VAPIDPublic != "" && cfg.VAPIDPrivate != "" {
		return notify.WebPush{Public: cfg.VAPIDPublic, Private: cfg.VAPIDPrivate, Subject: cfg.VAPIDSubject}
	}
	return notify.NoPush{}
}
