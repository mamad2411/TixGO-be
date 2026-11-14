package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/httpx"
	"tixgo/backend/internal/middleware"
	"tixgo/backend/internal/service"
)

// mountEngage: favorit, pengingat, follow, rekomendasi, kalender/rencana, best-available, prioritas pilihan, kit siap perang.
func (h *H) mountEngage(rt *Routes) {
	s := h.S
	a := rt.Authed
	uid := func(c *gin.Context) string { return httpx.MustPrincipal(c).UserID }
	ev := func(c *gin.Context) (string, bool) {
		e, err := s.Catalog.EventBySlug(c.Request.Context(), c.Param("slug"), c.GetHeader("X-Event-Password"), c.ClientIP())
		if err != nil {
			httpx.Fail(c, err)
			return "", false
		}
		return e.ID, true
	}

	a.GET("/me/recommendations", func(c *gin.Context) {
		v, err := s.Plan.Recommendations(c.Request.Context(), uid(c))
		respond(c, v, err)
	})

	// kalender & rencana hari-H
	rt.V1.GET("/events/:slug/calendar.ics", func(c *gin.Context) {
		b, name, err := s.Plan.EventICS(c.Request.Context(), c.Param("slug"))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		c.Header("Content-Disposition", `attachment; filename="`+name+`"`)
		c.Data(http.StatusOK, "text/calendar; charset=utf-8", b)
	})
	a.GET("/me/calendar.ics", func(c *gin.Context) {
		b, err := s.Plan.MyICS(c.Request.Context(), uid(c))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		c.Header("Content-Disposition", `attachment; filename="tiket-tixgo.ics"`)
		c.Data(http.StatusOK, "text/calendar; charset=utf-8", b)
	})
	a.GET("/tickets/:id/plan", func(c *gin.Context) {
		v, err := s.Plan.Plan(c.Request.Context(), uid(c), c.Param("id"))
		respond(c, v, err)
	})

	// best-available & prioritas pilihan
	rt.Opt.GET("/events/:slug/seats/suggest", middleware.RateLimit(s, "suggest", 60, time.Minute, middleware.ByIP), func(c *gin.Context) {
		e, err := s.Catalog.EventBySlug(c.Request.Context(), c.Param("slug"), c.GetHeader("X-Event-Password"), c.ClientIP())
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		qty, _ := strconv.Atoi(c.DefaultQuery("qty", "2"))
		v, err := s.Assist.Suggest(c.Request.Context(), e, c.Query("tier_id"), qty, c.Query("accessible") == "true", c.Query("allow_orphan") == "true")
		respond(c, v, err)
	})
	a.GET("/events/:slug/preferences", func(c *gin.Context) {
		if id, ok := ev(c); ok {
			v, err := s.Assist.GetPrefs(c.Request.Context(), uid(c), id)
			respond(c, v, err)
		}
	})
	a.PUT("/events/:slug/preferences", func(c *gin.Context) {
		b, ok := httpx.Bind[service.Preferences](c)
		if !ok {
			return
		}
		if id, ok := ev(c); ok {
			if err := s.Assist.SetPrefs(c.Request.Context(), uid(c), id, b); err != nil {
				httpx.Fail(c, err)
				return
			}
			httpx.OK(c, b)
		}
	})
	a.POST("/events/:slug/holds/auto", middleware.RateLimit(s, "hauto", 30, time.Minute, middleware.ByUser), func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Qty int `json:"qty" binding:"required,min=1,max=50"`
		}](c)
		if !ok {
			return
		}
		v, err := s.Assist.AutoHold(c.Request.Context(), uid(c), c.Param("slug"), b.Qty, c.GetHeader("X-Admission-Token"))
		created(c, v, err)
	})

	// kit siap perang
	rt.V1.GET("/clock", func(c *gin.Context) {
		var cm *int64
		if v := c.Query("client_ms"); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				httpx.Fail(c, apperr.Validation("client_ms harus angka (ms epoch)"))
				return
			}
			cm = &n
		}
		c.Header("Cache-Control", "no-store")
		httpx.OK(c, s.Ready.Clock(cm))
	})
	a.GET("/readiness", func(c *gin.Context) {
		var cm *int64
		if v := c.Query("client_ms"); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				cm = &n
			}
		}
		v, err := s.Ready.Check(c.Request.Context(), uid(c), cm)
		respond(c, v, err)
	})
	pr := a.Group("/practice")
	pr.POST("/start", func(c *gin.Context) {
		v, err := s.Ready.PracticeStart(c.Request.Context(), uid(c))
		respond(c, v, err)
	})
	pr.GET("/status", func(c *gin.Context) {
		v, err := s.Ready.PracticeStatus(c.Request.Context(), uid(c))
		respond(c, v, err)
	})
	pr.POST("/hold", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Qty int `json:"qty" binding:"required"`
		}](c)
		if !ok {
			return
		}
		v, err := s.Ready.PracticeHold(c.Request.Context(), uid(c), b.Qty)
		respond(c, v, err)
	})
	pr.POST("/pay", func(c *gin.Context) {
		v, err := s.Ready.PracticePay(c.Request.Context(), uid(c))
		respond(c, v, err)
	})
}

// mountPassport: Concert Passport (privat; tidak ada profil publik).
func (h *H) mountPassport(rt *Routes) {
	s := h.S
	uid := func(c *gin.Context) string { return httpx.MustPrincipal(c).UserID }
	g := rt.Authed.Group("/passport")
	g.GET("", func(c *gin.Context) {
		v, err := s.Passport.Mine(c.Request.Context(), uid(c))
		respond(c, v, err)
	})
	g.POST("/redeem", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Event string `json:"event" binding:"required,max=100"`
		}](c)
		if !ok {
			return
		}
		noContent(c, s.Passport.Redeem(c.Request.Context(), uid(c), b.Event))
	})
	g.DELETE("", func(c *gin.Context) { noContent(c, s.Passport.Delete(c.Request.Context(), uid(c))) })
	g.POST("/enable", func(c *gin.Context) { noContent(c, s.Passport.Enable(c.Request.Context(), uid(c))) })
}

// mountReviews: ulasan, laporan, balasan organizer, galeri, moderasi (docs/20 M41).
func (h *H) mountReviews(rt *Routes, org *gin.RouterGroup) {
	s := h.S
	uid := func(c *gin.Context) string { return httpx.MustPrincipal(c).UserID }
	rt.V1.GET("/events/:slug/reviews", func(c *gin.Context) {
		_, per, off := httpx.Page(c)
		v, err := s.Reviews.ForEvent(c.Request.Context(), c.Param("slug"), per, off)
		respond(c, v, err)
	})
	rt.V1.GET("/events/:slug/photos", func(c *gin.Context) {
		v, err := s.Reviews.Photos(c.Request.Context(), c.Param("slug"))
		respond(c, v, err)
	})
	rt.Authed.POST("/tickets/:id/review", middleware.RateLimit(s, "review", 20, time.Hour, middleware.ByUser), func(c *gin.Context) {
		b, ok := httpx.Bind[service.ReviewInput](c)
		if !ok {
			return
		}
		v, err := s.Reviews.Submit(c.Request.Context(), uid(c), c.Param("id"), b)
		created(c, v, err)
	})
	rt.Authed.POST("/reviews/:id/report", middleware.RateLimit(s, "revrep", 30, time.Hour, middleware.ByUser), func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Reason string `json:"reason" binding:"required,max=300"`
		}](c)
		if !ok {
			return
		}
		noContent(c, s.Reviews.Report(c.Request.Context(), uid(c), c.Param("id"), b.Reason))
	})
	rt.Authed.POST("/events/:slug/photos", middleware.RateLimit(s, "photo", 20, time.Hour, middleware.ByUser), func(c *gin.Context) {
		b, ok := httpx.Bind[service.PhotoInput](c)
		if !ok {
			return
		}
		v, err := s.Reviews.AddPhoto(c.Request.Context(), uid(c), c.Param("slug"), b)
		created(c, v, err)
	})
	org.POST("/events/:id/reviews/:rid/reply", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Reply string `json:"reply" binding:"required,max=1000"`
		}](c)
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		noContent(c, s.Reviews.Reply(c.Request.Context(), p, sc, c.Param("id"), c.Param("rid"), b.Reply))
	})
	mod := rt.Authed.Group("/admin/moderation", middleware.RequireRole("admin", "support"))
	mod.GET("/reviews", func(c *gin.Context) {
		_, per, off := httpx.Page(c)
		v, err := s.Reviews.Queue(c.Request.Context(), c.Query("status"), per, off)
		respond(c, v, err)
	})
	mod.POST("/reviews/:id/:decision", func(c *gin.Context) {
		noContent(c, s.Reviews.Moderate(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id"), c.Param("decision")))
	})
	mod.GET("/photos", func(c *gin.Context) {
		_, per, off := httpx.Page(c)
		v, err := s.Reviews.PhotoQueue(c.Request.Context(), c.Query("status"), per, off)
		respond(c, v, err)
	})
	mod.POST("/photos/:id/:decision", func(c *gin.Context) {
		noContent(c, s.Reviews.ModeratePhoto(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id"), c.Param("decision")))
	})
}

func (h *H) mountConcierge(rt *Routes) {
	s := h.S
	rt.Authed.POST("/concierge/ask", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Q string `json:"q" binding:"required,max=300"`
		}](c)
		if !ok {
			return
		}
		v, err := s.Concierge.Ask(c.Request.Context(), httpx.MustPrincipal(c).UserID, b.Q)
		respond(c, v, err)
	})
}
