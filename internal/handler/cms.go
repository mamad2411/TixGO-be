package handler

import (
	"time"

	"github.com/gin-gonic/gin"

	"tixgo/backend/internal/httpx"
	"tixgo/backend/internal/middleware"
	"tixgo/backend/internal/service"
)

// mountCMS: konten publik (halaman, artikel, bantuan, banner, koleksi) + studio editor (docs/20 M40, M30 AC1).
func (h *H) mountCMS(rt *Routes) {
	s := h.S
	loc := func(c *gin.Context) string {
		if c.Query("locale") == "en" {
			return "en"
		}
		return "id"
	}
	pub := rt.V1.Group("/content")
	pub.GET("/banners", func(c *gin.Context) {
		v, err := s.CMS.Banners(c.Request.Context(), true)
		respond(c, v, err)
	})
	pub.GET("/collections/:slug", func(c *gin.Context) {
		v, err := s.CMS.Collection(c.Request.Context(), c.Param("slug"))
		respond(c, v, err)
	})
	pub.GET("/help/search", middleware.RateLimit(s, "helpq", 60, time.Minute, middleware.ByIP), func(c *gin.Context) {
		v, err := s.CMS.SearchHelp(c.Request.Context(), loc(c), c.Query("q"))
		respond(c, v, err)
	})
	pub.GET("/:kind", func(c *gin.Context) {
		v, err := s.CMS.PublicList(c.Request.Context(), c.Param("kind"), loc(c), c.Query("category"))
		respond(c, v, err)
	})
	pub.GET("/:kind/:slug", func(c *gin.Context) {
		v, err := s.CMS.Public(c.Request.Context(), c.Param("kind"), c.Param("slug"), loc(c))
		respond(c, v, err)
	})

	ed := rt.Authed.Group("/admin/cms", middleware.RequireRole("editor", "admin"))
	ed.GET("/pages", func(c *gin.Context) {
		_, per, off := httpx.Page(c)
		v, err := s.CMS.List(c.Request.Context(), c.Query("kind"), c.Query("status"), c.Query("q"), per, off)
		respond(c, v, err)
	})
	ed.POST("/pages", func(c *gin.Context) {
		b, ok := httpx.Bind[service.PageInput](c)
		if !ok {
			return
		}
		v, err := s.CMS.Create(c.Request.Context(), httpx.MustPrincipal(c), b)
		created(c, v, err)
	})
	ed.GET("/pages/:id", func(c *gin.Context) {
		v, err := s.CMS.Get(c.Request.Context(), c.Param("id"))
		respond(c, v, err)
	})
	ed.GET("/versions", func(c *gin.Context) {
		v, err := s.CMS.Versions(c.Request.Context(), c.Query("kind"), c.Query("slug"), loc(c))
		respond(c, v, err)
	})
	ed.PUT("/pages/:id", func(c *gin.Context) {
		b, ok := httpx.Bind[service.PageInput](c)
		if !ok {
			return
		}
		v, err := s.CMS.Update(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id"), b)
		respond(c, v, err)
	})
	ed.POST("/pages/:id/submit", func(c *gin.Context) {
		v, err := s.CMS.SubmitReview(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id"))
		respond(c, v, err)
	})
	ed.POST("/pages/:id/reject", func(c *gin.Context) {
		v, err := s.CMS.Reject(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id"))
		respond(c, v, err)
	})
	ed.POST("/pages/:id/publish", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			At *time.Time `json:"publish_at"`
		}](c)
		if !ok {
			return
		}
		v, err := s.CMS.Publish(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id"), b.At)
		respond(c, v, err)
	})
	ed.GET("/banners", func(c *gin.Context) {
		v, err := s.CMS.Banners(c.Request.Context(), false)
		respond(c, v, err)
	})
	ed.POST("/banners", func(c *gin.Context) {
		b, ok := httpx.Bind[service.BannerInput](c)
		if !ok {
			return
		}
		v, err := s.CMS.CreateBanner(c.Request.Context(), httpx.MustPrincipal(c), b)
		created(c, v, err)
	})
	ed.DELETE("/banners/:id", func(c *gin.Context) { noContent(c, s.CMS.DeleteBanner(c.Request.Context(), c.Param("id"))) })
	ed.GET("/collections", func(c *gin.Context) {
		v, err := s.CMS.Collections(c.Request.Context())
		respond(c, v, err)
	})
	ed.PUT("/collections", func(c *gin.Context) {
		b, ok := httpx.Bind[service.CollectionInput](c)
		if !ok {
			return
		}
		v, err := s.CMS.SaveCollection(c.Request.Context(), httpx.MustPrincipal(c), b)
		respond(c, v, err)
	})
	ed.DELETE("/collections/:id", func(c *gin.Context) { noContent(c, s.CMS.DeleteCollection(c.Request.Context(), c.Param("id"))) })
}
