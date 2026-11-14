// Package ratelimit = pembatas laju berbasis Redis (GCRA, go-redis/redis_rate).
package ratelimit

import (
	"context"
	"time"

	"github.com/go-redis/redis_rate/v10"
	"github.com/redis/go-redis/v9"
)

type Limiter struct {
	l       *redis_rate.Limiter
	enabled bool
}

// New membuat limiter. enabled=false → selalu mengizinkan (tes yang tidak menguji rate limit).
func New(rdb *redis.Client, enabled bool) *Limiter {
	return &Limiter{l: redis_rate.NewLimiter(rdb), enabled: enabled}
}

// Allow memeriksa `limit` permintaan per `period` untuk kunci. Mengembalikan retryAfter bila ditolak.
func (r *Limiter) Allow(ctx context.Context, key string, limit int, period time.Duration) (bool, time.Duration, error) {
	if !r.enabled {
		return true, 0, nil
	}
	res, err := r.l.Allow(ctx, "rl:"+key, redis_rate.Limit{Rate: limit, Burst: limit, Period: period})
	if err != nil {
		return false, 0, err
	}
	return res.Allowed > 0, res.RetryAfter, nil
}
