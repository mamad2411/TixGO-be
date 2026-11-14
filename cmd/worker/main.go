package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"tixgo/backend/internal/app"
	"tixgo/backend/internal/config"
	"tixgo/backend/internal/jobs"
)

// Worker: server asynq (job tertunda/ulang) + loop periodik (antrean, expire, rekonsiliasi, outbox).
func main() {
	if err := run(); err != nil {
		slog.Error("worker berhenti", "error", err)
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

	b.Svc.RunSchedulers(ctx)
	srv := jobs.NewAsynqServer(b.Asynq, b.Reg, cfg.WorkerConcurrency, log)
	go func() { <-ctx.Done(); srv.Shutdown() }()
	log.Info("worker jalan", "jobs", b.Reg.Types())
	return srv.Run()
}
