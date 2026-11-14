package handler

import (
	"time"

	"github.com/gin-gonic/gin"

	"tixgo/backend/internal/httpx"
	"tixgo/backend/internal/middleware"
)

func (h *H) mountLegal(rt *Routes) {
	s := h.S
	lg := rt.V1.Group("/legal")
	lg.GET("/:kind", func(c *gin.Context) {
		d, err := s.Legal.Active(c.Request.Context(), c.Param("kind"), c.DefaultQuery("locale", "id"))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, d)
	})
	lg.GET("/:kind/versions", func(c *gin.Context) {
		v, err := s.Legal.Versions(c.Request.Context(), c.Param("kind"), c.DefaultQuery("locale", "id"))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	// ---- admin/editor ----
	ad := rt.Authed.Group("/admin/legal", middleware.RequireRole("admin", "editor"), middleware.RequireMFA())
	ad.POST("", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Kind   string `json:"kind" binding:"required"`
			Locale string `json:"locale" binding:"required,oneof=id en"`
			Title  string `json:"title" binding:"required,max=200"`
			Body   string `json:"body" binding:"required,max=200000"`
		}](c)
		if !ok {
			return
		}
		d, err := s.Legal.CreateDraft(c.Request.Context(), httpx.MustPrincipal(c), b.Kind, b.Locale, b.Title, b.Body)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.Created(c, d)
	})
	ad.PUT("/:id", func(c *gin.Context) {
		docID, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		b, ok := httpx.Bind[struct {
			Title string `json:"title" binding:"required,max=200"`
			Body  string `json:"body" binding:"required,max=200000"`
		}](c)
		if !ok {
			return
		}
		d, err := s.Legal.UpdateDraft(c.Request.Context(), httpx.MustPrincipal(c), docID, b.Title, b.Body)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, d)
	})
	ad.POST("/:id/publish", func(c *gin.Context) {
		docID, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		d, err := s.Legal.Publish(c.Request.Context(), httpx.MustPrincipal(c), docID)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, d)
	})

	me := rt.Authed.Group("/me")
	me.GET("/consents", func(c *gin.Context) {
		p := httpx.MustPrincipal(c)
		list, err := s.Legal.MyConsents(c.Request.Context(), p.UserID)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		pend, err := s.Legal.Pending(c.Request.Context(), s.Q, p.UserID)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, gin.H{"accepted": list, "pending": pend})
	})
	me.POST("/consents", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			DocumentID string `json:"document_id" binding:"required,len=26"`
		}](c)
		if !ok {
			return
		}
		if err := s.Legal.Accept(c.Request.Context(), httpx.MustPrincipal(c).UserID, b.DocumentID); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})
	// Cookie consent: subjek anonim (header X-Cookie-Subject) atau user login.
	ck := rt.Opt.Group("/cookie-consent", middleware.RateLimit(s, "cookie", 60, time.Minute, middleware.ByIP))
	ck.GET("", func(c *gin.Context) {
		v, err := s.Legal.GetCookie(c.Request.Context(), subjectKey(c))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	ck.PUT("", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Analytics bool `json:"analytics"`
			Marketing bool `json:"marketing"`
		}](c)
		if !ok {
			return
		}
		uid := ""
		if p := httpx.Principal(c); p != nil {
			uid = p.UserID
		}
		v, err := s.Legal.PutCookie(c.Request.Context(), subjectKey(c), uid, struct {
			Analytics bool `json:"analytics"`
			Marketing bool `json:"marketing"`
		}(b))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
}

func subjectKey(c *gin.Context) string {
	if p := httpx.Principal(c); p != nil {
		return "u:" + p.UserID
	}
	return c.GetHeader("X-Cookie-Subject")
}
