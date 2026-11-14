package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"tixgo/backend/internal/httpx"
	"tixgo/backend/internal/service"
)

// mountOrganizerAttendees: peserta, comp, tiket (reissue/void/rename), siaran (docs/20 M36). Dipasang di grup /organizer/events.
func (h *H) mountOrganizerAttendees(ev *gin.RouterGroup) {
	s := h.S
	q := func(c *gin.Context) service.AttendeeQuery {
		_, per, off := httpx.Page(c)
		return service.AttendeeQuery{Status: c.Query("status"), TierID: c.Query("tier_id"), Q: c.Query("q"), Limit: per, Offset: off}
	}
	ev.GET("/:id/attendees", func(c *gin.Context) {
		_, sc := orgCtx(c)
		page, per, _ := httpx.Page(c)
		items, total, err := s.Attendee.List(c.Request.Context(), sc, c.Param("id"), q(c))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.List(c, items, httpx.Meta{Page: page, PerPage: per, Total: total})
	})
	ev.GET("/:id/attendees.csv", func(c *gin.Context) {
		p, sc := orgCtx(c)
		b, err := s.Attendee.ExportCSV(c.Request.Context(), p, sc, c.Param("id"), q(c))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		c.Header("Content-Disposition", `attachment; filename="peserta-`+c.Param("id")+`.csv"`)
		c.Data(http.StatusOK, "text/csv; charset=utf-8", b)
	})
	ev.GET("/:id/comps", func(c *gin.Context) {
		_, sc := orgCtx(c)
		_, per, off := httpx.Page(c)
		v, err := s.Attendee.ListComps(c.Request.Context(), sc, c.Param("id"), per, off)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	ev.POST("/:id/comps", func(c *gin.Context) {
		b, ok := httpx.Bind[service.CompInput](c)
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		v, err := s.Attendee.Comp(c.Request.Context(), p, sc, c.Param("id"), b)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.Created(c, v)
	})
	ev.POST("/:id/comps/import", func(c *gin.Context) {
		data, ok := readUpload(c, "file", 1<<20)
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		v, err := s.Attendee.ImportComps(c.Request.Context(), p, sc, c.Param("id"), c.PostForm("tier_id"), c.PostForm("reason"), data)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	tk := func(path string, fn func(c *gin.Context) error) {
		ev.POST("/:id/tickets/:tid/"+path, func(c *gin.Context) {
			if err := fn(c); err != nil {
				if err != errHandled {
					httpx.Fail(c, err)
				}
				return
			}
			httpx.NoContent(c)
		})
	}
	tk("reissue", func(c *gin.Context) error {
		p, sc := orgCtx(c)
		return s.Attendee.Reissue(c.Request.Context(), p, sc, c.Param("id"), c.Param("tid"))
	})
	tk("void", func(c *gin.Context) error {
		p, sc := orgCtx(c)
		return s.Attendee.Void(c.Request.Context(), p, sc, c.Param("id"), c.Param("tid"))
	})
	tk("rename", func(c *gin.Context) error {
		b, ok := httpx.Bind[struct {
			Name string `json:"name" binding:"required,max=100"`
		}](c)
		if !ok {
			return errHandled
		}
		p, sc := orgCtx(c)
		return s.Attendee.Rename(c.Request.Context(), p, sc, c.Param("id"), c.Param("tid"), b.Name)
	})
	ev.GET("/:id/broadcasts", func(c *gin.Context) {
		_, sc := orgCtx(c)
		v, err := s.Attendee.ListBroadcasts(c.Request.Context(), sc, c.Param("id"))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	ev.POST("/:id/broadcasts", func(c *gin.Context) {
		b, ok := httpx.Bind[service.BroadcastInput](c)
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		v, err := s.Attendee.Broadcast(c.Request.Context(), p, sc, c.Param("id"), b)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.Created(c, v)
	})
}
