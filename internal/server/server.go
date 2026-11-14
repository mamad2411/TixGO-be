// Package server merakit engine Gin: middleware global, health/ready/metrics, dan route /api/v1.
package server

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"tixgo/backend/internal/handler"
	"tixgo/backend/internal/middleware"
	"tixgo/backend/internal/service"
)

// Pinger = dependensi yang bisa dicek kesiapannya (pg, redis).
type Pinger interface {
	Ping(ctx context.Context) error
}

// NewBase membangun engine dengan middleware global + /healthz /readyz /metrics (tanpa route bisnis).
func NewBase(trustedProxies []string, corsOrigins []string, metricsToken string, log *slog.Logger, readiness map[string]Pinger) (*gin.Engine, error) {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	// Gin default mempercayai SEMUA proxy (X-Forwarded-For bisa dispoof).
	// Kosong = tidak percaya proxy mana pun. Pelajaran dari voralens-v2.
	if err := r.SetTrustedProxies(trustedProxies); err != nil {
		return nil, err
	}
	r.RedirectTrailingSlash = false
	r.Use(middleware.RequestID(), middleware.Recover(log), middleware.AccessLog(log), middleware.Secure(), middleware.BodyLimit(), middleware.CORS(corsOrigins))

	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"data": gin.H{"status": "ok"}})
	})

	r.GET("/readyz", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		checks := gin.H{}
		ok := true
		for name, d := range readiness {
			if err := d.Ping(ctx); err != nil {
				checks[name] = "down"
				ok = false
			} else {
				checks[name] = "up"
			}
		}
		code := http.StatusOK
		if !ok {
			code = http.StatusServiceUnavailable
		}
		c.JSON(code, gin.H{"data": gin.H{"ready": ok, "checks": checks}})
	})

	metrics := gin.WrapH(promhttp.Handler())
	r.GET("/metrics", func(c *gin.Context) {
		if metricsToken != "" {
			got := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(metricsToken)) != 1 {
				c.AbortWithStatus(http.StatusUnauthorized)
				return
			}
		}
		metrics(c)
	})
	return r, nil
}

// New = NewBase + seluruh route bisnis.
func New(s *service.Services, readiness map[string]Pinger) (*gin.Engine, error) {
	r, err := NewBase(s.Cfg.TrustedProxies, s.Cfg.CORSOrigins, s.Cfg.MetricsToken, s.Log, readiness)
	if err != nil {
		return nil, err
	}
	r.Use(middleware.Maintenance(s))
	handler.Register(r, s)
	return r, nil
}
