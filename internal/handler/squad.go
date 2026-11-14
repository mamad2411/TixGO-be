package handler

import (
	"time"

	"github.com/gin-gonic/gin"

	"tixgo/backend/internal/httpx"
	"tixgo/backend/internal/middleware"
	"tixgo/backend/internal/service"
)

// mountSquad: Squad Checkout (docs/10 §2).
func (h *H) mountSquad(rt *Routes) {
	s := h.S
	uid := func(c *gin.Context) string { return httpx.MustPrincipal(c).UserID }
	a := rt.Authed
	a.POST("/events/:slug/squads", middleware.RateLimit(s, "squadc", 10, time.Minute, middleware.ByUser), func(c *gin.Context) {
		b, ok := httpx.Bind[service.SquadCreate](c)
		if !ok {
			return
		}
		v, err := s.Squad.Create(c.Request.Context(), uid(c), c.Param("slug"), b, c.GetHeader("X-Admission-Token"))
		created(c, v, err)
	})
	a.GET("/me/squads", func(c *gin.Context) {
		v, err := s.Squad.Mine(c.Request.Context(), uid(c))
		respond(c, v, err)
	})
	a.POST("/squads/join", middleware.RateLimit(s, "squadj", 20, time.Minute, middleware.ByUser), func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Code string `json:"code" binding:"required,min=6,max=12"`
		}](c)
		if !ok {
			return
		}
		v, err := s.Squad.Join(c.Request.Context(), uid(c), b.Code)
		respond(c, v, err)
	})
	a.GET("/squads/:id", func(c *gin.Context) {
		v, err := s.Squad.Get(c.Request.Context(), uid(c), c.Param("id"))
		respond(c, v, err)
	})
	a.POST("/squads/:id/invite", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Email string `json:"email" binding:"required,email,max=254"`
		}](c)
		if !ok {
			return
		}
		noContent(c, s.Squad.Invite(c.Request.Context(), uid(c), c.Param("id"), b.Email))
	})
	a.POST("/squads/:id/pay", middleware.Idempotency(s), func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Attendees    []service.OrderAttendee `json:"attendees" binding:"omitempty,max=1,dive"`
			PromoCode    string                  `json:"promo_code" binding:"omitempty,max=32"`
			AgeConfirmed bool                    `json:"age_confirmed"`
		}](c)
		if !ok {
			return
		}
		v, err := s.Squad.Pay(c.Request.Context(), uid(c), c.Param("id"), service.CreateOrderReq{Attendees: b.Attendees, PromoCode: b.PromoCode, AgeConfirmed: b.AgeConfirmed})
		created(c, v, err)
	})
	a.POST("/squads/:id/extend", func(c *gin.Context) {
		v, err := s.Squad.Extend(c.Request.Context(), uid(c), c.Param("id"))
		respond(c, v, err)
	})
	a.DELETE("/squads/:id", func(c *gin.Context) { noContent(c, s.Squad.Cancel(c.Request.Context(), uid(c), c.Param("id"))) })
}
