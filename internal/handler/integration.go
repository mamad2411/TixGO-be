package handler

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/httpx"
	"tixgo/backend/internal/middleware"
	"tixgo/backend/internal/service"
)

const keyCtx = "partner_key"

// mountOrganizerIntegration: API key, webhook, domain widget, piksel (organizer). Dipasang di grup organizer (anggota ber-MFA untuk owner-only di service).
func (h *H) mountOrganizerIntegration(m *gin.RouterGroup) {
	s := h.S
	g := m.Group("/integrations")
	g.GET("/api-keys", func(c *gin.Context) {
		_, sc := orgCtx(c)
		v, err := s.Integration.ListKeys(c.Request.Context(), sc)
		respond(c, v, err)
	})
	g.POST("/api-keys", func(c *gin.Context) {
		b, ok := httpx.Bind[service.APIKeyInput](c)
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		v, err := s.Integration.CreateKey(c.Request.Context(), p, sc, b)
		created(c, v, err)
	})
	g.POST("/api-keys/:id/rotate", func(c *gin.Context) {
		p, sc := orgCtx(c)
		v, err := s.Integration.RotateKey(c.Request.Context(), p, sc, c.Param("id"))
		created(c, v, err)
	})
	g.DELETE("/api-keys/:id", func(c *gin.Context) {
		p, sc := orgCtx(c)
		noContent(c, s.Integration.RevokeKey(c.Request.Context(), p, sc, c.Param("id")))
	})
	g.GET("/webhooks", func(c *gin.Context) {
		_, sc := orgCtx(c)
		v, err := s.Integration.ListWebhooks(c.Request.Context(), sc)
		respond(c, v, err)
	})
	g.POST("/webhooks", func(c *gin.Context) {
		b, ok := httpx.Bind[service.WebhookInput](c)
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		v, err := s.Integration.CreateWebhook(c.Request.Context(), p, sc, b)
		created(c, v, err)
	})
	g.DELETE("/webhooks/:id", func(c *gin.Context) {
		p, sc := orgCtx(c)
		noContent(c, s.Integration.DeleteWebhook(c.Request.Context(), p, sc, c.Param("id")))
	})
	g.GET("/webhooks/:id/deliveries", func(c *gin.Context) {
		_, sc := orgCtx(c)
		v, err := s.Integration.Deliveries(c.Request.Context(), sc, c.Param("id"))
		respond(c, v, err)
	})
	g.POST("/webhooks/:id/ping", middleware.RateLimit(s, "whping", 10, time.Minute, middleware.ByUser), func(c *gin.Context) {
		_, sc := orgCtx(c)
		v, err := s.Integration.Ping(c.Request.Context(), sc, c.Param("id"))
		respond(c, v, err)
	})
	g.GET("/widget-domains", func(c *gin.Context) {
		_, sc := orgCtx(c)
		v, err := s.Integration.Domains(c.Request.Context(), sc)
		respond(c, v, err)
	})
	g.POST("/widget-domains", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Domain string `json:"domain" binding:"required,max=253"`
		}](c)
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		noContent(c, s.Integration.AddDomain(c.Request.Context(), p, sc, b.Domain))
	})
	g.DELETE("/widget-domains/:domain", func(c *gin.Context) {
		p, sc := orgCtx(c)
		noContent(c, s.Integration.RemoveDomain(c.Request.Context(), p, sc, c.Param("domain")))
	})
	g.PUT("/events/:id/tracking", func(c *gin.Context) {
		b, ok := httpx.Bind[service.Tracking](c)
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		v, err := s.Integration.SetTracking(c.Request.Context(), p, sc, c.Param("id"), b)
		respond(c, v, err)
	})
}

func respond(c *gin.Context, v any, err error) {
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, v)
}

func created(c *gin.Context, v any, err error) {
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Created(c, v)
}

func noContent(c *gin.Context, err error) {
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.NoContent(c)
}

// mountPartner: API publik baca-saja berautentikasi API key (scope sales:read / attendees:read) + widget + piksel publik.
func (h *H) mountPartner(rt *Routes) {
	s := h.S
	keyAuth := func(scope string) gin.HandlerFunc {
		return func(c *gin.Context) {
			raw := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
			if raw == "" {
				raw = c.GetHeader("X-API-Key")
			}
			ki, err := s.Integration.AuthenticateKey(c.Request.Context(), raw)
			if err != nil {
				httpx.Fail(c, err)
				c.Abort()
				return
			}
			if !ki.Scopes[scope] {
				httpx.Fail(c, apperr.Forbidden("scope "+scope+" tidak dimiliki key ini"))
				c.Abort()
				return
			}
			c.Set(keyCtx, ki)
		}
	}
	ki := func(c *gin.Context) *service.KeyIdentity {
		v, _ := c.Get(keyCtx)
		k, _ := v.(*service.KeyIdentity)
		return k
	}
	p := rt.V1.Group("/partner/v1")
	p.GET("/events", keyAuth("sales:read"), func(c *gin.Context) {
		v, err := s.Integration.PartnerEvents(c.Request.Context(), ki(c))
		respond(c, v, err)
	})
	p.GET("/events/:id/sales", keyAuth("sales:read"), func(c *gin.Context) {
		v, err := s.Integration.PartnerSales(c.Request.Context(), ki(c), c.Param("id"))
		respond(c, v, err)
	})
	p.GET("/events/:id/attendees", keyAuth("attendees:read"), func(c *gin.Context) {
		page, per, off := httpx.Page(c)
		v, total, err := s.Integration.PartnerAttendees(c.Request.Context(), ki(c), c.Param("id"), service.AttendeeQuery{Status: c.Query("status"), Q: c.Query("q"), Limit: per, Offset: off})
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.List(c, v, httpx.Meta{Page: page, PerPage: per, Total: total})
	})

	// widget: JSON event hanya untuk origin yang di-allowlist organizer; CSP frame-ancestors untuk iframe
	rt.V1.GET("/widget/events/:slug", func(c *gin.Context) {
		ev, err := s.Catalog.EventBySlug(c.Request.Context(), c.Param("slug"), "", c.ClientIP())
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		raw, err := s.Q.GetEvent(c.Request.Context(), ev.ID)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		origin := c.GetHeader("Origin")
		if origin == "" || !s.Integration.WidgetOriginAllowed(c.Request.Context(), raw.OrganizerID, origin) {
			httpx.Fail(c, apperr.Forbidden("domain ini tidak diizinkan menanam widget"))
			return
		}
		c.Header("Access-Control-Allow-Origin", origin)
		c.Header("Vary", "Origin")
		c.Header("Content-Security-Policy", s.Integration.FrameAncestors(c.Request.Context(), raw.OrganizerID))
		httpx.OK(c, ev)
	})
	rt.Opt.GET("/events/:slug/pixels", func(c *gin.Context) {
		v, err := s.Integration.PixelsFor(c.Request.Context(), c.Param("slug"), subjectKey(c))
		respond(c, v, err)
	})
	_ = http.StatusOK
}
