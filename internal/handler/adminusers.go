package handler

import (
	"github.com/gin-gonic/gin"

	"tixgo/backend/internal/httpx"
	"tixgo/backend/internal/middleware"
	"tixgo/backend/internal/service"
)

// mountAdminUsers: pengguna, blokir, peran, blacklist, sinyal risiko. Admin + MFA.
func (h *H) mountAdminUsers(rt *Routes) {
	s := h.S
	ad := rt.Authed.Group("/admin", middleware.RequireRole("admin"), middleware.RequireMFA())
	ad.GET("/users", func(c *gin.Context) {
		_, per, off := httpx.Page(c)
		v, err := s.AdminUsers.Search(c.Request.Context(), httpx.MustPrincipal(c), c.Query("q"), c.Query("role"), c.Query("status"), per, off)
		respond(c, v, err)
	})
	ad.GET("/users/:id", func(c *gin.Context) {
		v, err := s.AdminUsers.Get(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id"))
		respond(c, v, err)
	})
	ad.POST("/users/:id/block", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Reason string `json:"reason" binding:"required,max=300"`
		}](c)
		if !ok {
			return
		}
		noContent(c, s.AdminUsers.SetBlocked(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id"), true, b.Reason))
	})
	ad.POST("/users/:id/unblock", func(c *gin.Context) {
		noContent(c, s.AdminUsers.SetBlocked(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id"), false, ""))
	})
	ad.PUT("/users/:id/role", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Role string `json:"role" binding:"required"`
		}](c)
		if !ok {
			return
		}
		noContent(c, s.AdminUsers.SetRole(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id"), b.Role))
	})
	ad.GET("/users/:id/risk-signals", func(c *gin.Context) {
		_, per, off := httpx.Page(c)
		v, err := s.AdminUsers.Signals(c.Request.Context(), c.Param("id"), per, off)
		respond(c, v, err)
	})
	ad.GET("/blacklist", func(c *gin.Context) {
		_, per, off := httpx.Page(c)
		v, err := s.AdminUsers.ListBlacklist(c.Request.Context(), per, off)
		respond(c, v, err)
	})
	ad.POST("/blacklist", func(c *gin.Context) {
		b, ok := httpx.Bind[service.BlacklistInput](c)
		if !ok {
			return
		}
		v, err := s.AdminUsers.AddBlacklist(c.Request.Context(), httpx.MustPrincipal(c), b)
		created(c, v, err)
	})
	ad.DELETE("/blacklist/:id", func(c *gin.Context) {
		noContent(c, s.AdminUsers.RemoveBlacklist(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id")))
	})
}
