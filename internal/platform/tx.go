package platform

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Pool dipakai service agar dapat diganti di tes bila perlu.
type Pool = pgxpool.Pool

// WithTx menjalankan fn dalam satu transaksi; commit bila fn sukses, rollback bila error/panic.
// Di sinilah transaksi dibuka (service), bukan di repository (docs/02-CONVENTIONS).
func WithTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) (err error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(ctx)
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	if cerr := tx.Commit(ctx); cerr != nil {
		return fmt.Errorf("commit: %w", cerr)
	}
	return nil
}

// PG error code helpers.
const (
	pgUniqueViolation     = "23505"
	pgForeignKeyViolation = "23503"
	pgCheckViolation      = "23514"
	pgSerialization       = "40001"
	pgDeadlock            = "40P01"
)

func pgCode(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

func IsUniqueViolation(err error) bool     { return pgCode(err) == pgUniqueViolation }
func IsForeignKeyViolation(err error) bool { return pgCode(err) == pgForeignKeyViolation }
func IsCheckViolation(err error) bool      { return pgCode(err) == pgCheckViolation }
func IsRetryable(err error) bool           { c := pgCode(err); return c == pgSerialization || c == pgDeadlock }

// ConstraintName mengembalikan nama constraint pelanggaran (kosong bila bukan error PG).
func ConstraintName(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.ConstraintName
	}
	return ""
}

// IsNoRows = baris tidak ditemukan.
func IsNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
