package handler

import (
	"github.com/gin-gonic/gin"

	"tixgo/backend/internal/httpx"
	"tixgo/backend/internal/middleware"
)

// mountAdmin: antrean KYB & moderasi event (docs/20 M12 AC1, M33). Semua butuh admin + sesi MFA.
func (h *H) mountAdmin(rt *Routes) {
	s := h.S
	ad := rt.Authed.Group("/admin", middleware.RequireRole("admin"), middleware.RequireMFA())

	ad.GET("/kyb", func(c *gin.Context) {
		page, per, off := httpx.Page(c)
		l, total, err := s.Organizer.AdminList(c.Request.Context(), c.Query("status"), per, off)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.List(c, l, httpx.Meta{Page: page, PerPage: per, Total: total})
	})
	ad.GET("/kyb/:id", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		v, err := s.Organizer.AdminGet(c.Request.Context(), id)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	ad.GET("/kyb/:id/documents/:doc_id/url", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		did, ok := httpx.ParamID(c, "doc_id")
		if !ok {
			return
		}
		u, err := s.Organizer.DocumentURL(c.Request.Context(), httpx.MustPrincipal(c), id, did)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, gin.H{"url": u, "expires_in": 300})
	})
	ad.POST("/kyb/:id/review", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		b, ok := httpx.Bind[struct {
			Decision string `json:"decision" binding:"required,oneof=approve reject needs_info"`
			Reason   string `json:"reason" binding:"max=1000"`
		}](c)
		if !ok {
			return
		}
		v, err := s.Organizer.Review(c.Request.Context(), httpx.MustPrincipal(c), id, b.Decision, b.Reason)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	ad.POST("/organizers/:id/suspend", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		if err := s.Organizer.SetSuspended(c.Request.Context(), httpx.MustPrincipal(c), id, true); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})
	ad.POST("/organizers/:id/unsuspend", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		if err := s.Organizer.SetSuspended(c.Request.Context(), httpx.MustPrincipal(c), id, false); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})

	// moderasi event
	ad.GET("/events/review", func(c *gin.Context) {
		page, per, off := httpx.Page(c)
		l, total, err := s.Event.AdminList(c.Request.Context(), c.Query("status"), per, off)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.List(c, l, httpx.Meta{Page: page, PerPage: per, Total: total})
	})
	ad.GET("/events/:id", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		v, err := s.Event.AdminGet(c.Request.Context(), id)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	ad.POST("/events/:id/review", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		b, ok := httpx.Bind[struct {
			Decision string `json:"decision" binding:"required,oneof=approve reject"`
			Note     string `json:"note" binding:"max=1000"`
		}](c)
		if !ok {
			return
		}
		v, err := s.Event.AdminReview(c.Request.Context(), httpx.MustPrincipal(c), id, b.Decision, b.Note)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})

	// verifikasi rekening (finance/admin)
	fin := rt.Authed.Group("/finance", middleware.RequireRole("finance", "admin"), middleware.RequireMFA())
	fin.POST("/bank-accounts/:id/verify", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		if err := s.Organizer.VerifyBank(c.Request.Context(), httpx.MustPrincipal(c), id); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})
}
