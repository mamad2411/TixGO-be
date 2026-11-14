package main

// dev-totp mencetak kode TOTP 6 digit saat ini untuk akun demo ber-2FA (hanya dev/staging; untuk e2e & coba manual).
// DITOLAK di production.
//
//	cd backend && set -a && . ./.env && set +a && go run ./cmd/dev-totp organizer@tixgo.local

import (
	"context"
	"fmt"
	"os"
	"time"

	"tixgo/backend/internal/app"
	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/config"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "pakai: dev-totp <email>")
		os.Exit(2)
	}
	if err := run(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "dev-totp gagal:", err)
		os.Exit(1)
	}
}

func run(email string) error {
	cfg, err := config.FromOS()
	if err != nil {
		return err
	}
	if cfg.IsProd() {
		return fmt.Errorf("dev-totp ditolak di production")
	}
	ctx := context.Background()
	b, err := app.Build(ctx, cfg, app.NewLogger(cfg))
	if err != nil {
		return err
	}
	defer b.Close()
	var enc []byte
	if err := b.Svc.Pool.QueryRow(ctx, `SELECT m.secret_enc FROM mfa_totp m JOIN users u ON u.id=m.user_id WHERE u.email=$1 AND m.confirmed_at IS NOT NULL`, email).Scan(&enc); err != nil {
		return fmt.Errorf("akun %s tidak punya 2FA terkonfirmasi: %w", email, err)
	}
	sec, err := b.Svc.Box.Decrypt(enc)
	if err != nil {
		return err
	}
	code, err := auth.TOTPCode(string(sec), time.Now(), 30)
	if err != nil {
		return err
	}
	fmt.Println(code)
	return nil
}
