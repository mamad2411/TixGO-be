package platform

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// NewPostgres membuka pool dan memverifikasi koneksi (gagal keras bila tidak bisa).
func NewPostgres(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("postgres: buka pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	return pool, nil
}

// NewRedis membuat klien (DB 0) dan memverifikasi koneksi.
func NewRedis(ctx context.Context, addr, password string) (*redis.Client, error) {
	return NewRedisDB(ctx, addr, password, 0)
}

// NewRedisDB seperti NewRedis dengan pemilihan nomor DB (tes memakai DB terpisah).
func NewRedisDB(ctx context.Context, addr, password string, db int) (*redis.Client, error) {
	rdb := redis.NewClient(&redis.Options{Addr: addr, Password: password, DB: db})
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("redis: ping: %w", err)
	}
	return rdb, nil
}
