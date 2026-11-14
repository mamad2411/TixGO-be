package handler

import (
	"time"

	"github.com/gin-gonic/gin"

	"tixgo/backend/internal/middleware"
)

func timeQ(c *gin.Context, key string) *time.Time {
	v := c.Query(key)
	if v == "" {
		return nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return &t
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return &t
	}
	return nil
}

// mountAnalytics: laporan organizer (penjualan, funnel, arus gate) + dasbor bisnis admin (docs/20 M19, M46).
func (h *H) mountAnalytics(m *gin.RouterGroup, rt *Routes) {
	s := h.S
	g := m.Group("/events/:id/analytics")
	g.GET("/sales", func(c *gin.Context) {
		_, sc := orgCtx(c)
		b := c.DefaultQuery("bucket", "day")
		v, err := s.Analytics.Sales(c.Request.Context(), sc, c.Param("id"), b, timeQ(c, "from"), timeQ(c, "to"))
		respond(c, v, err)
	})
	g.GET("/funnel", func(c *gin.Context) {
		_, sc := orgCtx(c)
		v, err := s.Analytics.EventFunnel(c.Request.Context(), sc, c.Param("id"), timeQ(c, "from"), timeQ(c, "to"))
		respond(c, v, err)
	})
	g.GET("/gate-flow", func(c *gin.Context) {
		_, sc := orgCtx(c)
		v, err := s.Analytics.GateFlow(c.Request.Context(), sc, c.Param("id"), timeQ(c, "from"), timeQ(c, "to"))
		respond(c, v, err)
	})
	ad := rt.Authed.Group("/admin/analytics", middleware.RequireRole("admin"), middleware.RequireMFA())
	ad.GET("/business", func(c *gin.Context) {
		v, err := s.Analytics.Business(c.Request.Context(), timeQ(c, "from"), timeQ(c, "to"))
		respond(c, v, err)
	})
}
