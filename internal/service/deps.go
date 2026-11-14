// Package service = aturan bisnis, otorisasi, dan transaksi (docs/backend/01-ARCHITECTURE).
// Handler tipis memanggil service; service memanggil sqlcgen (repository) dan paket domain khusus.
package service

import (
	"context"
	"log/slog"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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
	"tixgo/backend/internal/storage"
)

// Deps = semua dependensi eksternal; diinjeksi sekali, dipakai semua service.
type Deps struct {
	Cfg     *config.Config
	Log     *slog.Logger
	Pool    *pgxpool.Pool
	Redis   *redis.Client
	Clock   clock.Clock
	Tokens  *auth.Tokens
	Hasher  *auth.PasswordHasher
	Box     *auth.Box
	Mailer  notify.Mailer
	SMS     notify.SMSSender
	Push    notify.PushSender
	Gateway payment.Gateway
	Store   storage.Store
	Jobs    jobs.Enqueuer
	Reg     *jobs.Registry
	Limiter *ratelimit.Limiter
	Q       *sqlcgen.Queries
}

// InTx menjalankan fn dalam satu transaksi dengan Queries terikat tx.
func (d *Deps) InTx(ctx context.Context, fn func(q *sqlcgen.Queries) error) error {
	return platform.WithTx(ctx, d.Pool, func(tx pgx.Tx) error { return fn(d.Q.WithTx(tx)) })
}

// InTxRaw = seperti InTx tetapi juga memberi pgx.Tx (untuk query dinamis/raw).
func (d *Deps) InTxRaw(ctx context.Context, fn func(q *sqlcgen.Queries, tx pgx.Tx) error) error {
	return platform.WithTx(ctx, d.Pool, func(tx pgx.Tx) error { return fn(d.Q.WithTx(tx), tx) })
}

func (d *Deps) now() time.Time { return d.Clock.Now() }

// ---- helper umum ----

func ptr[T any](v T) *T { return &v }

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

var phoneRe = regexp.MustCompile(`^\+[1-9][0-9]{8,14}$`)

// NormalizePhone → E.164. "08xx" → "+628xx", "628xx" → "+628xx".
func NormalizePhone(s string) (string, bool) {
	s = strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' || r == '(' || r == ')' || r == '.' {
			return -1
		}
		return r
	}, s)
	switch {
	case strings.HasPrefix(s, "+"):
	case strings.HasPrefix(s, "08"):
		s = "+62" + s[1:]
	case strings.HasPrefix(s, "62"):
		s = "+" + s
	default:
		return "", false
	}
	return s, phoneRe.MatchString(s)
}

// NormalizeEmail → huruf kecil, tervalidasi (RFC 5322 sederhana), maks 254.
func NormalizeEmail(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || len(s) > 254 {
		return "", false
	}
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s || !strings.Contains(s[strings.LastIndex(s, "@"):], ".") {
		return "", false
	}
	return s, true
}

// MaskEmail untuk log/respons yang tidak perlu alamat penuh.
func MaskEmail(e string) string {
	i := strings.IndexByte(e, '@')
	if i <= 1 {
		return "***" + e[i:]
	}
	return e[:1] + "***" + e[i:]
}
