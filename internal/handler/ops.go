package handler

import (
	"github.com/gin-gonic/gin"

	"tixgo/backend/internal/httpx"
	"tixgo/backend/internal/middleware"
	"tixgo/backend/internal/service"
)

// mountOps: status layanan, pengumuman, flag publik + control room admin (docs/20 M39, M30).
func (h *H) mountOps(rt *Routes) {
	s := h.S
	rt.V1.GET("/status", func(c *gin.Context) {
		v, err := s.Ops.Status(c.Request.Context())
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	rt.V1.GET("/announcements", func(c *gin.Context) {
		v, err := s.Ops.Active(c.Request.Context(), c.DefaultQuery("locale", "id"))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	rt.Opt.GET("/flags", func(c *gin.Context) {
		subject := c.GetHeader("X-Device-Id")
		if p := httpx.Principal(c); p != nil {
			subject = p.UserID
		}
		httpx.OK(c, s.Ops.FlagsFor(c.Request.Context(), subject))
	})

	ad := rt.Authed.Group("/admin/ops", middleware.RequireRole("admin"), middleware.RequireMFA())
	ad.GET("/events/:id/control-room", func(c *gin.Context) {
		v, err := s.Ops.ControlRoom(c.Request.Context(), c.Param("id"))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	ad.POST("/events/:id/pause", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Reason string `json:"reason" binding:"required,min=3,max=300"`
		}](c)
		if !ok {
			return
		}
		v, err := s.Ops.SetSalesPaused(c.Request.Context(), httpx.MustPrincipal(c), nil, c.Param("id"), true, b.Reason)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	ad.POST("/events/:id/resume", func(c *gin.Context) {
		v, err := s.Ops.SetSalesPaused(c.Request.Context(), httpx.MustPrincipal(c), nil, c.Param("id"), false, "")
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	ad.POST("/events/:id/cancel", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Reason string `json:"reason" binding:"required,min=5,max=500"`
		}](c)
		if !ok {
			return
		}
		v, err := s.Lifecycle.AdminCancel(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id"), b.Reason)
		respond(c, v, err)
	})
	ad.PUT("/events/:id/admit-rate", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Rate int `json:"rate" binding:"required"`
		}](c)
		if !ok {
			return
		}
		if err := s.Ops.SetAdmitRate(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id"), b.Rate); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})
	ad.PUT("/maintenance", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			On      bool   `json:"on"`
			Message string `json:"message" binding:"omitempty,max=300"`
			ETA     string `json:"eta" binding:"omitempty,max=60"`
		}](c)
		if !ok {
			return
		}
		if err := s.Ops.SetMaintenance(c.Request.Context(), httpx.MustPrincipal(c), b.On, b.Message, b.ETA); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})
	ad.GET("/announcements", func(c *gin.Context) {
		v, err := s.Ops.AllAnnouncements(c.Request.Context())
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	ad.POST("/announcements", func(c *gin.Context) {
		b, ok := httpx.Bind[service.AnnouncementInput](c)
		if !ok {
			return
		}
		v, err := s.Ops.CreateAnnouncement(c.Request.Context(), httpx.MustPrincipal(c), b)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.Created(c, v)
	})
	ad.POST("/announcements/:id/end", func(c *gin.Context) {
		if err := s.Ops.EndAnnouncement(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id")); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})
	ad.GET("/flags", func(c *gin.Context) {
		v, err := s.Ops.ListFlags(c.Request.Context())
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	ad.PUT("/flags/:key", func(c *gin.Context) {
		b, ok := httpx.Bind[service.FlagInput](c)
		if !ok {
			return
		}
		v, err := s.Ops.SetFlag(c.Request.Context(), httpx.MustPrincipal(c), c.Param("key"), b)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
}
