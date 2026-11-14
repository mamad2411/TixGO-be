package handler

import (
	"time"

	"github.com/gin-gonic/gin"

	"tixgo/backend/internal/httpx"
	"tixgo/backend/internal/middleware"
	"tixgo/backend/internal/service"
)

// mountAccount: preferensi notifikasi, web push, riwayat notifikasi, pusat privasi, pengaduan, antrean DSR admin (docs/20 M29, M42).
func (h *H) mountAccount(rt *Routes) {
	s := h.S
	me := rt.Authed.Group("/me")
	uid := func(c *gin.Context) string { return httpx.MustPrincipal(c).UserID }

	me.GET("/notification-prefs", func(c *gin.Context) {
		v, err := s.Account.Prefs(c.Request.Context(), uid(c))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	me.PUT("/notification-prefs", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Category string `json:"category" binding:"required"`
			Channel  string `json:"channel" binding:"required"`
			Enabled  bool   `json:"enabled"`
		}](c)
		if !ok {
			return
		}
		if err := s.Account.SetPref(c.Request.Context(), uid(c), b.Category, b.Channel, b.Enabled); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})
	me.GET("/notifications", func(c *gin.Context) {
		_, per, off := httpx.Page(c)
		loc := "id"
		if c.Query("locale") == "en" {
			loc = "en"
		}
		v, err := s.Account.Notifications(c.Request.Context(), uid(c), loc, per, off)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	me.GET("/notifications/unread-count", func(c *gin.Context) {
		n, err := s.Account.UnreadNotifications(c.Request.Context(), uid(c))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, gin.H{"unread": n})
	})
	// ids kosong/tidak ada = tandai semua dibaca.
	me.POST("/notifications/read", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			IDs []string `json:"ids" binding:"omitempty,max=100,dive,len=26"`
		}](c)
		if !ok {
			return
		}
		if err := s.Account.MarkNotificationsRead(c.Request.Context(), uid(c), b.IDs); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})
	me.GET("/push/key", func(c *gin.Context) { httpx.OK(c, gin.H{"vapid_public_key": s.Account.VAPIDPublicKey()}) })
	me.POST("/push/subscriptions", func(c *gin.Context) {
		b, ok := httpx.Bind[service.PushSubInput](c)
		if !ok {
			return
		}
		if err := s.Account.PushSubscribe(c.Request.Context(), uid(c), b); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})
	me.DELETE("/push/subscriptions", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Endpoint string `json:"endpoint" binding:"required,max=1000"`
		}](c)
		if !ok {
			return
		}
		if err := s.Account.PushUnsubscribe(c.Request.Context(), uid(c), b.Endpoint); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})

	// privasi
	me.GET("/privacy/exports", func(c *gin.Context) {
		v, err := s.Privacy.Exports(c.Request.Context(), uid(c))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	me.POST("/privacy/exports", func(c *gin.Context) {
		v, err := s.Privacy.RequestExport(c.Request.Context(), uid(c))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.Accepted(c, v)
	})
	me.GET("/privacy/requests", func(c *gin.Context) {
		v, err := s.Privacy.MyRequests(c.Request.Context(), uid(c))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	me.POST("/privacy/delete-account", middleware.RateLimit(s, "dsrdel", 5, time.Hour, middleware.ByUser), func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Password  string `json:"password" binding:"required,max=200"`
			Reason    string `json:"reason" binding:"omitempty,max=500"`
			MFACode   string `json:"mfa_code" binding:"omitempty,max=32"`
			MFAMethod string `json:"mfa_method" binding:"omitempty,oneof=totp email sms"`
		}](c)
		if !ok {
			return
		}
		v, err := s.Privacy.RequestDeletion(c.Request.Context(), uid(c), b.Password, b.Reason, service.SecondFactor{Method: b.MFAMethod, Code: b.MFACode})
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.Created(c, v)
	})

	// pengaduan privasi (publik, dibatasi)
	rt.V1.POST("/privacy/complaints", middleware.RateLimit(s, "pcomp", 5, time.Hour, middleware.ByIP), func(c *gin.Context) {
		b, ok := httpx.Bind[service.ComplaintInput](c)
		if !ok {
			return
		}
		if err := s.Privacy.Complain(c.Request.Context(), b); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})

	// admin
	ad := rt.Authed.Group("/admin/privacy", middleware.RequireRole("admin"), middleware.RequireMFA())
	ad.GET("/requests", func(c *gin.Context) {
		page, per, off := httpx.Page(c)
		items, total, err := s.Privacy.AdminList(c.Request.Context(), c.Query("status"), per, off)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.List(c, items, httpx.Meta{Page: page, PerPage: per, Total: total})
	})
	ad.POST("/requests/:id/execute", func(c *gin.Context) {
		if err := s.Privacy.Execute(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id")); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})
	ad.POST("/requests/:id/reject", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Reason string `json:"reason" binding:"required,max=500"`
		}](c)
		if !ok {
			return
		}
		if err := s.Privacy.Reject(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id"), b.Reason); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})
	ad.GET("/complaints", func(c *gin.Context) {
		_, per, off := httpx.Page(c)
		v, err := s.Privacy.Complaints(c.Request.Context(), c.Query("status"), per, off)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	ad.POST("/complaints/:id/close", func(c *gin.Context) {
		if err := s.Privacy.CloseComplaint(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id")); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})
}
