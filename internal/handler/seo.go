package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"tixgo/backend/internal/httpx"
)

// mountSEO: HTML event dengan meta + JSON-LD (tanpa JS), sitemap.xml, robots.txt (Q11, docs/20 M32). Route root (di luar /api/v1).
func (h *H) mountSEO(rt *Routes) {
	s := h.S
	rt.Root.GET("/events/:slug", func(c *gin.Context) {
		page, status, err := s.SEO.EventPage(c.Request.Context(), c.Param("slug"), c.GetHeader("X-Event-Password"), c.ClientIP())
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		c.Header("Cache-Control", "public, max-age=60, stale-while-revalidate=300")
		c.Data(status, "text/html; charset=utf-8", []byte(page))
	})
	rt.Root.GET("/sitemap.xml", func(c *gin.Context) {
		b, err := s.SEO.Sitemap(c.Request.Context())
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		c.Header("Cache-Control", "public, max-age=600")
		c.Data(http.StatusOK, "application/xml; charset=utf-8", b)
	})
	rt.Root.GET("/robots.txt", func(c *gin.Context) {
		c.Header("Cache-Control", "public, max-age=3600")
		c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(s.SEO.Robots()))
	})
}
