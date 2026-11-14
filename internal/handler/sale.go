package handler

import (
	"github.com/gin-gonic/gin"

	"tixgo/backend/internal/httpx"
)

// mountSale: ketersediaan, presale, registrasi fan (docs/20 M4, M6).
func (h *H) mountSale(rt *Routes) {
	s := h.S
	rt.V1.GET("/events/:slug/availability", func(c *gin.Context) {
		ev, err := s.Catalog.EventBySlug(c.Request.Context(), c.Param("slug"), c.GetHeader("X-Event-Password"), c.ClientIP())
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		av, err := s.Catalog.Availability(c.Request.Context(), ev)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, av)
	})
	rt.Authed.POST("/events/:slug/presale/redeem", func(c *gin.Context) {
		ev, err := s.Catalog.EventBySlug(c.Request.Context(), c.Param("slug"), c.GetHeader("X-Event-Password"), c.ClientIP())
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		b, ok := httpx.Bind[struct {
			Code string `json:"code" binding:"required,max=64"`
		}](c)
		if !ok {
			return
		}
		ph, err := s.Phase.Redeem(c.Request.Context(), httpx.MustPrincipal(c).UserID, ev.ID, b.Code)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, ph)
	})
	rt.Authed.POST("/events/:slug/fan-registration", func(c *gin.Context) {
		ev, err := s.Catalog.EventBySlug(c.Request.Context(), c.Param("slug"), "", c.ClientIP())
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		if err := s.Phase.RegisterFan(c.Request.Context(), httpx.MustPrincipal(c).UserID, ev.ID); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})
}
