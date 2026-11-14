package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/httpx"
	"tixgo/backend/internal/middleware"
	"tixgo/backend/internal/service"
)

type eventListQuery struct {
	Q        string `form:"q" binding:"max=100"`
	City     string `form:"city" binding:"max=80"`
	Genre    string `form:"genre" binding:"max=40"`
	DateFrom string `form:"date_from" binding:"max=40"`
	DateTo   string `form:"date_to" binding:"max=40"`
	PriceMin *int64 `form:"price_min" binding:"omitempty,gte=0"`
	PriceMax *int64 `form:"price_max" binding:"omitempty,gte=0"`
	Status   string `form:"status" binding:"omitempty,oneof=upcoming onsale soldout"`
	Sort     string `form:"sort" binding:"omitempty,oneof=date new popular"`
}

func parseTimeParam(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return &t, nil
		}
	}
	return nil, apperr.Validation("format tanggal harus RFC3339 atau YYYY-MM-DD")
}

func (h *H) mountCatalog(rt *Routes) {
	s := h.S
	v1 := rt.V1

	// ---- media ----
	v1.GET("/media/:id/:file", func(c *gin.Context) {
		name := strings.TrimSuffix(c.Param("file"), ".jpg")
		w, err := strconv.Atoi(name)
		if err != nil {
			httpx.Fail(c, apperr.NotFound("gambar"))
			return
		}
		rc, info, err := s.Media.Open(c.Request.Context(), c.Param("id"), w)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		defer func() { _ = rc.Close() }()
		etag := `"` + c.Param("id") + "-" + name + `"`
		if c.GetHeader("If-None-Match") == etag {
			c.Status(http.StatusNotModified)
			return
		}
		c.Header("ETag", etag)
		c.Header("Cache-Control", "public, max-age=31536000, immutable")
		c.Header("Content-Length", strconv.FormatInt(info.Size, 10))
		c.Header("Content-Type", "image/jpeg")
		c.Status(http.StatusOK)
		_, _ = io.Copy(c.Writer, rc)
	})
	rt.Authed.POST("/media", func(c *gin.Context) {
		kind := c.PostForm("kind")
		fh, err := c.FormFile("file")
		if err != nil {
			httpx.Fail(c, apperr.Validation("berkas 'file' wajib (multipart)"))
			return
		}
		f, err := fh.Open()
		if err != nil {
			httpx.Fail(c, apperr.Validation("berkas tidak terbaca"))
			return
		}
		defer func() { _ = f.Close() }()
		v, err := s.Media.Upload(c.Request.Context(), httpx.MustPrincipal(c), kind, f)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.Created(c, v)
	})

	// ---- event publik ----
	v1.GET("/events", func(c *gin.Context) {
		q, ok := httpx.BindQuery[eventListQuery](c)
		if !ok {
			return
		}
		page, per, _ := httpx.Page(c)
		from, err := parseTimeParam(q.DateFrom)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		to, err := parseTimeParam(q.DateTo)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		items, total, err := s.Catalog.List(c.Request.Context(), service.EventFilter{Q: q.Q, City: q.City, Genre: q.Genre, DateFrom: from, DateTo: to,
			PriceMin: q.PriceMin, PriceMax: q.PriceMax, Status: q.Status, Sort: q.Sort, Page: page, PerPage: per})
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		c.Header("Cache-Control", "public, max-age=5")
		httpx.List(c, items, httpx.Meta{Page: page, PerPage: per, Total: total})
	})
	v1.GET("/events/:slug", func(c *gin.Context) {
		d, err := s.Catalog.Detail(c.Request.Context(), c.Param("slug"), c.GetHeader("X-Event-Password"), c.ClientIP())
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		if d.Visibility == "public" {
			c.Header("Cache-Control", "public, max-age=5")
		}
		s.Analytics.Track(c.Request.Context(), d.ID, "view") // agregat tanpa PII
		httpx.OK(c, d)
	})
	v1.GET("/events/:slug/seatmap", func(c *gin.Context) {
		ev, err := s.Catalog.EventBySlug(c.Request.Context(), c.Param("slug"), c.GetHeader("X-Event-Password"), c.ClientIP())
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		sm, err := s.Seatmap.Geometry(c.Request.Context(), ev.ID)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		sum := sha256.Sum256([]byte(ev.ID + ev.UpdatedAt.String() + strconv.Itoa(len(sm.Seats))))
		etag := `"` + hex.EncodeToString(sum[:8]) + `"`
		if c.GetHeader("If-None-Match") == etag {
			c.Status(http.StatusNotModified)
			return
		}
		c.Header("ETag", etag)
		c.Header("Cache-Control", "public, max-age=60")
		httpx.OK(c, sm)
	})
	// Status kursi terisi (publik, cache sangat singkat): peta menandai kursi yang tidak tersedia.
	v1.GET("/events/:slug/seats/status", middleware.RateLimit(s, "seatstatus", 120, time.Minute, middleware.ByIP), func(c *gin.Context) {
		ev, err := s.Catalog.EventBySlug(c.Request.Context(), c.Param("slug"), c.GetHeader("X-Event-Password"), c.ClientIP())
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		st, err := s.Seatmap.Statuses(c.Request.Context(), ev.ID)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		c.Header("Cache-Control", "public, max-age=2")
		httpx.OK(c, st)
	})
	v1.GET("/events/:slug/seatmap.svg", func(c *gin.Context) {
		ev, err := s.Catalog.EventBySlug(c.Request.Context(), c.Param("slug"), c.GetHeader("X-Event-Password"), c.ClientIP())
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		b, err := s.Seatmap.SVG(c.Request.Context(), ev.ID)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		c.Header("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
		c.Header("Cache-Control", "public, max-age=60")
		c.Data(http.StatusOK, "image/svg+xml", b)
	})
	v1.GET("/calendar", func(c *gin.Context) {
		m := c.DefaultQuery("month", s.Clock.Now().Format("2006-01"))
		t, err := time.Parse("2006-01", m)
		if err != nil {
			httpx.Fail(c, apperr.Validation("month harus berformat YYYY-MM").With("field", "month"))
			return
		}
		days, err := s.Catalog.Calendar(c.Request.Context(), t)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, days)
	})
	v1.GET("/meta/filters", func(c *gin.Context) {
		f, err := s.Catalog.Filters(c.Request.Context())
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		c.Header("Cache-Control", "public, max-age=60")
		httpx.OK(c, f)
	})

	// ---- artis & venue publik ----
	v1.GET("/artists", func(c *gin.Context) {
		page, per, off := httpx.Page(c)
		items, total, err := s.Artist.List(c.Request.Context(), c.Query("q"), per, off)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.List(c, items, httpx.Meta{Page: page, PerPage: per, Total: total})
	})
	v1.GET("/artists/:slug", func(c *gin.Context) {
		a, err := s.Artist.GetBySlug(c.Request.Context(), c.Param("slug"))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		evs, err := s.Catalog.ArtistEvents(c.Request.Context(), a.ID)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, gin.H{"artist": a, "events": evs})
	})
	v1.GET("/venues", func(c *gin.Context) {
		page, per, off := httpx.Page(c)
		items, total, err := s.Venue.List(c.Request.Context(), c.Query("q"), c.Query("city"), per, off)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.List(c, items, httpx.Meta{Page: page, PerPage: per, Total: total})
	})
	v1.GET("/venues/:id", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		v, err := s.Venue.Get(c.Request.Context(), id)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})

	// ---- favorit, pengingat, follow (login) ----
	me := rt.Authed.Group("/me")
	me.GET("/favorites", func(c *gin.Context) {
		ids, err := s.Follow.FavoriteIDs(c.Request.Context(), httpx.MustPrincipal(c).UserID)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		cards, err := s.Catalog.CardsByIDs(c.Request.Context(), ids)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, cards)
	})
	me.POST("/favorites/:event_id", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "event_id")
		if !ok {
			return
		}
		if err := s.Follow.AddFavorite(c.Request.Context(), httpx.MustPrincipal(c).UserID, id); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})
	me.DELETE("/favorites/:event_id", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "event_id")
		if !ok {
			return
		}
		if err := s.Follow.RemoveFavorite(c.Request.Context(), httpx.MustPrincipal(c).UserID, id); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})
	me.GET("/vouchers", func(c *gin.Context) {
		v, err := s.Voucher.List(c.Request.Context(), httpx.MustPrincipal(c).UserID)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	me.POST("/vouchers", func(c *gin.Context) {
		in, ok := httpx.Bind[struct {
			Code string `json:"code" binding:"required"`
		}](c)
		if !ok {
			return
		}
		v, err := s.Voucher.Redeem(c.Request.Context(), httpx.MustPrincipal(c).UserID, in.Code)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	me.DELETE("/vouchers/:promo_id", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "promo_id")
		if !ok {
			return
		}
		if err := s.Voucher.Remove(c.Request.Context(), httpx.MustPrincipal(c).UserID, id); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})
	rt.Authed.POST("/events/:slug/reminders", func(c *gin.Context) {
		ev, err := s.Catalog.EventBySlug(c.Request.Context(), c.Param("slug"), "", c.ClientIP())
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		at, err := s.Follow.SetReminder(c.Request.Context(), httpx.MustPrincipal(c).UserID, ev.ID)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, gin.H{"remind_at": at})
	})
	rt.Authed.DELETE("/events/:slug/reminders", func(c *gin.Context) {
		ev, err := s.Catalog.EventBySlug(c.Request.Context(), c.Param("slug"), "", c.ClientIP())
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		if err := s.Follow.RemoveReminder(c.Request.Context(), httpx.MustPrincipal(c).UserID, ev.ID); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})
	me.GET("/follows", func(c *gin.Context) {
		l, err := s.Follow.List(c.Request.Context(), httpx.MustPrincipal(c).UserID)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, l)
	})
	followBody := struct {
		Kind   string `json:"kind" binding:"required,oneof=artist venue city genre"`
		Target string `json:"target" binding:"required,max=80"`
	}{}
	_ = followBody
	me.POST("/follows", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Kind   string `json:"kind" binding:"required,oneof=artist venue city genre"`
			Target string `json:"target" binding:"required,max=80"`
		}](c)
		if !ok {
			return
		}
		if err := s.Follow.Follow(c.Request.Context(), httpx.MustPrincipal(c).UserID, b.Kind, b.Target); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})
	me.DELETE("/follows/:kind/:target", func(c *gin.Context) {
		if err := s.Follow.Unfollow(c.Request.Context(), httpx.MustPrincipal(c).UserID, c.Param("kind"), c.Param("target")); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})
}
