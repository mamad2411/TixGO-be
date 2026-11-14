package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"tixgo/backend/internal/app"
	"tixgo/backend/internal/config"
	"tixgo/backend/internal/server"
)

// redisPing mengadaptasi *redis.Client ke server.Pinger.
type redisPing struct{ c *redis.Client }

func (r redisPing) Ping(ctx context.Context) error { return r.c.Ping(ctx).Err() }

func main() {
	if err := run(); err != nil {
		slog.Error("api berhenti", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.FromOS()
	if err != nil {
		return err
	}
	log := app.NewLogger(cfg)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	b, err := app.Build(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer b.Close()

	go b.Svc.WS.Run(ctx)
	defer b.Svc.WS.Close()

	engine, err := server.New(b.Svc, map[string]server.Pinger{
		"postgres": b.Svc.Pool,
		"redis":    redisPing{b.Svc.Redis},
	})
	if err != nil {
		return err
	}
	// Timeout ketat melawan slowloris/koneksi menggantung. Koneksi WebSocket di-hijack sehingga tidak terkena
	// ReadTimeout/WriteTimeout (net/http menghapus deadline saat Hijack).
	srv := &http.Server{
		Addr: cfg.HTTPAddr, Handler: engine,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("api listen", "addr", cfg.HTTPAddr, "env", cfg.AppEnv)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutCtx)
}
