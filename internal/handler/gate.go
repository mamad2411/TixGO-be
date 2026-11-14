package handler

import (
	"time"

	"github.com/gin-gonic/gin"

	"tixgo/backend/internal/httpx"
	"tixgo/backend/internal/middleware"
	"tixgo/backend/internal/service"
)

// mountGate: scan online, manifest + sinkron offline, ringkasan live (docs/06 M10). Otorisasi per event di service.
func (h *H) mountGate(rt *Routes) {
	s := h.S
	g := rt.Authed.Group("/gate", middleware.RateLimit(s, "gate", 600, time.Minute, middleware.ByUser))
	g.POST("/scan", func(c *gin.Context) {
		b, ok := httpx.Bind[service.ScanReq](c)
		if !ok {
			return
		}
		r, err := s.Gate.Scan(c.Request.Context(), httpx.MustPrincipal(c), b)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, r)
	})
	g.GET("/events/:id/manifest", func(c *gin.Context) {
		m, err := s.Gate.Manifest(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id"))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		c.Header("Cache-Control", "no-store")
		httpx.OK(c, m)
	})
	g.POST("/events/:id/sync", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Scans []service.OfflineScan `json:"scans" binding:"required,min=1,max=1000,dive"`
		}](c)
		if !ok {
			return
		}
		r, err := s.Gate.Sync(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id"), b.Scans)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, r)
	})
	g.GET("/events/:id/summary", func(c *gin.Context) {
		r, err := s.Gate.Summary(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id"))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, r)
	})
}
