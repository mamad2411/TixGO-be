package handler

import (
	"time"

	"github.com/gin-gonic/gin"

	"tixgo/backend/internal/httpx"
	"tixgo/backend/internal/middleware"
	"tixgo/backend/internal/service"
)

// mountSupport: tiket bantuan pelanggan + antrean agen (docs/20 M30). Role agen: support|admin.
func (h *H) mountSupport(rt *Routes) {
	s := h.S
	uid := func(c *gin.Context) string { return httpx.MustPrincipal(c).UserID }
	u := rt.Authed.Group("/support/tickets")
	u.POST("", middleware.RateLimit(s, "sup", 20, time.Hour, middleware.ByUser), func(c *gin.Context) {
		b, ok := httpx.Bind[service.SupportInput](c)
		if !ok {
			return
		}
		v, err := s.Support.Create(c.Request.Context(), uid(c), b)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.Created(c, v)
	})
	u.GET("", func(c *gin.Context) {
		_, per, off := httpx.Page(c)
		v, err := s.Support.MyTickets(c.Request.Context(), uid(c), per, off)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	u.GET("/:id", func(c *gin.Context) {
		v, err := s.Support.MyThread(c.Request.Context(), uid(c), c.Param("id"))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	u.POST("/:id/messages", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Body string `json:"body" binding:"required,max=5000"`
		}](c)
		if !ok {
			return
		}
		if err := s.Support.Reply(c.Request.Context(), uid(c), c.Param("id"), b.Body); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})
	u.POST("/:id/close", func(c *gin.Context) {
		if err := s.Support.CloseMine(c.Request.Context(), uid(c), c.Param("id")); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})

	ag := rt.Authed.Group("/agent/support", middleware.RequireRole("support", "admin"))
	ag.GET("/templates", func(c *gin.Context) { httpx.OK(c, service.SupportTemplates) })
	ag.GET("/tickets", func(c *gin.Context) {
		page, per, off := httpx.Page(c)
		items, total, err := s.Support.Queue(c.Request.Context(), service.AgentFilter{Status: c.Query("status"), Category: c.Query("category"), Priority: c.Query("priority"), Assignee: c.Query("assignee"), Limit: per, Offset: off})
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.List(c, items, httpx.Meta{Page: page, PerPage: per, Total: total})
	})
	ag.GET("/tickets/:id", func(c *gin.Context) {
		v, err := s.Support.AgentThread(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id"))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	ag.POST("/tickets/:id/messages", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Body     string `json:"body" binding:"required,max=5000"`
			Internal bool   `json:"internal"`
		}](c)
		if !ok {
			return
		}
		if err := s.Support.AgentReply(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id"), b.Body, b.Internal); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})
	ag.POST("/tickets/:id/assign", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			AssigneeID string `json:"assignee_id" binding:"omitempty,len=26"`
		}](c)
		if !ok {
			return
		}
		if err := s.Support.Assign(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id"), b.AssigneeID); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})
	ag.POST("/tickets/:id/resolve", func(c *gin.Context) {
		if err := s.Support.Resolve(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id")); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})
}
