package handler

import (
	"github.com/gin-gonic/gin"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/httpx"
	"tixgo/backend/internal/service"
)

func (h *H) mountOrganizerEvents(m *gin.RouterGroup) {
	s := h.S
	ev := m.Group("/events")
	h.mountOrganizerAttendees(ev)

	ev.POST("", func(c *gin.Context) {
		b, ok := httpx.Bind[service.EventInput](c)
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		v, err := s.Event.Create(c.Request.Context(), p, sc, b)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.Created(c, v)
	})
	ev.PUT("/:id/ticket-policy", func(c *gin.Context) {
		b, ok := httpx.Bind[service.TicketPolicy](c)
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		v, err := s.Transfer.SetPolicy(c.Request.Context(), p, sc, c.Param("id"), b)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	ev.POST("/:id/duplicate", func(c *gin.Context) {
		b, ok := httpx.Bind[service.DuplicateInput](c)
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		v, err := s.Lifecycle.Duplicate(c.Request.Context(), p, sc, c.Param("id"), b)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.Created(c, v)
	})
	ev.POST("/:id/reschedule", func(c *gin.Context) {
		b, ok := httpx.Bind[service.RescheduleInput](c)
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		v, err := s.Lifecycle.Reschedule(c.Request.Context(), p, sc, c.Param("id"), b)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	ev.POST("/:id/postpone", func(c *gin.Context) {
		p, sc := orgCtx(c)
		v, err := s.Lifecycle.Postpone(c.Request.Context(), p, sc, c.Param("id"))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	ev.POST("/:id/cancel", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Reason string `json:"reason" binding:"required,min=5,max=500"`
		}](c)
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		v, err := s.Lifecycle.Cancel(c.Request.Context(), p, sc, c.Param("id"), b.Reason)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	ev.GET("/:id/promos", func(c *gin.Context) {
		_, sc := orgCtx(c)
		v, err := s.Promo.List(c.Request.Context(), sc, c.Param("id"))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	ev.POST("/:id/promos", func(c *gin.Context) {
		b, ok := httpx.Bind[service.PromoInput](c)
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		v, err := s.Promo.Create(c.Request.Context(), p, sc, c.Param("id"), b)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.Created(c, v)
	})
	ev.PATCH("/:id/promos/:pid", func(c *gin.Context) {
		b, ok := httpx.Bind[service.PromoPatch](c)
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		v, err := s.Promo.Update(c.Request.Context(), p, sc, c.Param("id"), c.Param("pid"), b)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	ev.GET("/:id/promos/:pid/report", func(c *gin.Context) {
		_, sc := orgCtx(c)
		v, err := s.Promo.Report(c.Request.Context(), sc, c.Param("id"), c.Param("pid"))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	ev.POST("/:id/pause-sales", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Reason string `json:"reason" binding:"required,min=3,max=300"`
		}](c)
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		if !sc.IsOwner() {
			httpx.Fail(c, apperr.Forbidden("hanya pemilik organizer"))
			return
		}
		v, err := s.Ops.SetSalesPaused(c.Request.Context(), p, &sc, c.Param("id"), true, b.Reason)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	ev.POST("/:id/resume-sales", func(c *gin.Context) {
		p, sc := orgCtx(c)
		if !sc.IsOwner() {
			httpx.Fail(c, apperr.Forbidden("hanya pemilik organizer"))
			return
		}
		v, err := s.Ops.SetSalesPaused(c.Request.Context(), p, &sc, c.Param("id"), false, "")
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	ev.GET("", func(c *gin.Context) {
		_, sc := orgCtx(c)
		page, per, off := httpx.Page(c)
		items, total, err := s.Event.List(c.Request.Context(), sc, c.Query("status"), c.Query("q"), per, off)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.List(c, items, httpx.Meta{Page: page, PerPage: per, Total: total})
	})
	ev.GET("/:id", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		_, sc := orgCtx(c)
		v, err := s.Event.Get(c.Request.Context(), sc, id)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	ev.PATCH("/:id", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		b, ok := httpx.Bind[service.EventInput](c)
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		v, err := s.Event.Update(c.Request.Context(), p, sc, id, b)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	ev.DELETE("/:id", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		if err := s.Event.Delete(c.Request.Context(), p, sc, id); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})
	action := func(name string, fn func(c *gin.Context, id string) (*service.EventView, error)) {
		ev.POST("/:id/"+name, func(c *gin.Context) {
			id, ok := httpx.ParamID(c, "id")
			if !ok {
				return
			}
			v, err := fn(c, id)
			if err != nil {
				httpx.Fail(c, err)
				return
			}
			httpx.OK(c, v)
		})
	}
	action("submit", func(c *gin.Context, id string) (*service.EventView, error) {
		p, sc := orgCtx(c)
		return s.Event.Submit(c.Request.Context(), p, sc, id)
	})
	action("withdraw", func(c *gin.Context, id string) (*service.EventView, error) {
		p, sc := orgCtx(c)
		return s.Event.Withdraw(c.Request.Context(), p, sc, id)
	})
	action("publish", func(c *gin.Context, id string) (*service.EventView, error) {
		p, sc := orgCtx(c)
		return s.Event.Publish(c.Request.Context(), p, sc, id)
	})

	// ---- tier ----
	ev.GET("/:id/tiers", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		_, sc := orgCtx(c)
		l, err := s.Tier.List(c.Request.Context(), sc, id)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, l)
	})
	ev.POST("/:id/tiers", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		b, ok := httpx.Bind[service.TierInput](c)
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		v, err := s.Tier.Create(c.Request.Context(), p, sc, id, b)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.Created(c, v)
	})
	ev.PATCH("/:id/tiers/:tier_id", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		tid, ok := httpx.ParamID(c, "tier_id")
		if !ok {
			return
		}
		b, ok := httpx.Bind[service.TierInput](c)
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		v, err := s.Tier.Update(c.Request.Context(), p, sc, id, tid, b)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	ev.DELETE("/:id/tiers/:tier_id", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		tid, ok := httpx.ParamID(c, "tier_id")
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		if err := s.Tier.Delete(c.Request.Context(), p, sc, id, tid); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})

	// ---- add-on ----
	ev.GET("/:id/addons", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		_, sc := orgCtx(c)
		l, err := s.Tier.Addons(c.Request.Context(), sc, id)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, l)
	})
	ev.POST("/:id/addons", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		b, ok := httpx.Bind[service.AddonInput](c)
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		v, err := s.Tier.CreateAddon(c.Request.Context(), p, sc, id, b)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.Created(c, v)
	})
	ev.PATCH("/:id/addons/:addon_id", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		aid, ok := httpx.ParamID(c, "addon_id")
		if !ok {
			return
		}
		b, ok := httpx.Bind[service.AddonInput](c)
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		v, err := s.Tier.UpdateAddon(c.Request.Context(), p, sc, id, aid, b)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})

	// ---- denah ----
	ev.POST("/:id/seatmap/generate", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		b, ok := httpx.Bind[struct {
			Sections []struct {
				Name        string   `json:"name" binding:"required,max=40"`
				TierID      string   `json:"tier_id" binding:"required,len=26"`
				Rows        int      `json:"rows" binding:"required,min=1,max=100"`
				SeatsPerRow int      `json:"seats_per_row" binding:"required,min=1,max=100"`
				Accessible  []string `json:"accessible" binding:"omitempty,max=500"`
			} `json:"sections" binding:"required,min=1,max=50,dive"`
		}](c)
		if !ok {
			return
		}
		specs := make([]service.SectionSpec, 0, len(b.Sections))
		for _, sp := range b.Sections {
			specs = append(specs, service.SectionSpec{Name: sp.Name, TierID: sp.TierID, Rows: sp.Rows, SeatsPerRow: sp.SeatsPerRow, Accessible: sp.Accessible})
		}
		p, sc := orgCtx(c)
		v, err := s.Seatmap.Generate(c.Request.Context(), p, sc, id, specs)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	ev.POST("/:id/seatmap/import-svg", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		raw, ok := readUpload(c, "file", 2<<20)
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		v, err := s.Seatmap.ImportSVG(c.Request.Context(), p, sc, id, raw)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	ev.GET("/:id/seatmap", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		_, sc := orgCtx(c)
		if _, err := s.Event.Get(c.Request.Context(), sc, id); err != nil {
			httpx.Fail(c, err)
			return
		}
		sm, err := s.Seatmap.Geometry(c.Request.Context(), id)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, sm)
	})
	seatBlock := func(blocked bool) gin.HandlerFunc {
		return func(c *gin.Context) {
			id, ok := httpx.ParamID(c, "id")
			if !ok {
				return
			}
			b, ok := httpx.Bind[struct {
				SeatIDs []string `json:"seat_ids" binding:"required,min=1,max=5000,dive,len=26"`
			}](c)
			if !ok {
				return
			}
			p, sc := orgCtx(c)
			n, err := s.Seatmap.SetBlocked(c.Request.Context(), p, sc, id, b.SeatIDs, blocked)
			if err != nil {
				httpx.Fail(c, err)
				return
			}
			httpx.OK(c, gin.H{"changed": n})
		}
	}
	ev.POST("/:id/seats/block", seatBlock(true))
	ev.POST("/:id/seats/unblock", seatBlock(false))

	// ---- fase penjualan, presale, fans ----
	ev.GET("/:id/phases", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		_, sc := orgCtx(c)
		l, err := s.Phase.List(c.Request.Context(), sc, id)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, l)
	})
	ev.POST("/:id/phases", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		b, ok := httpx.Bind[service.PhaseInput](c)
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		v, err := s.Phase.Create(c.Request.Context(), p, sc, id, b)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.Created(c, v)
	})
	ev.PATCH("/:id/phases/:phase_id", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		pid, ok := httpx.ParamID(c, "phase_id")
		if !ok {
			return
		}
		b, ok := httpx.Bind[service.PhaseInput](c)
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		v, err := s.Phase.Update(c.Request.Context(), p, sc, id, pid, b)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	ev.DELETE("/:id/phases/:phase_id", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		pid, ok := httpx.ParamID(c, "phase_id")
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		if err := s.Phase.Delete(c.Request.Context(), p, sc, id, pid); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})
	ev.POST("/:id/phases/:phase_id/codes", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		pid, ok := httpx.ParamID(c, "phase_id")
		if !ok {
			return
		}
		b, ok := httpx.Bind[struct {
			Count   int    `json:"count" binding:"omitempty,min=0,max=1000"`
			Code    string `json:"code" binding:"omitempty,max=32"`
			MaxUses *int   `json:"max_uses" binding:"omitempty,min=1"`
		}](c)
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		codes, err := s.Phase.CreateCodes(c.Request.Context(), p, sc, id, pid, b.Count, b.Code, b.MaxUses)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.Created(c, gin.H{"codes": codes, "note": "kode hanya ditampilkan sekali"})
	})
	ev.GET("/:id/fans", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		_, sc := orgCtx(c)
		page, per, off := httpx.Page(c)
		l, total, err := s.Phase.FanRegistrations(c.Request.Context(), sc, id, c.Query("status"), per, off)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.List(c, l, httpx.Meta{Page: page, PerPage: per, Total: total})
	})
	ev.POST("/:id/fans/select", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		b, ok := httpx.Bind[struct {
			UserIDs []string `json:"user_ids" binding:"omitempty,max=5000,dive,len=26"`
			Random  int      `json:"random" binding:"omitempty,min=0,max=5000"`
		}](c)
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		n, err := s.Phase.SelectFans(c.Request.Context(), p, sc, id, b.UserIDs, b.Random)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, gin.H{"granted": n})
	})

	// ---- pintu & staf gate ----
	ev.GET("/:id/gates", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		_, sc := orgCtx(c)
		l, err := s.GateAdmin.Gates(c.Request.Context(), sc, id)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, l)
	})
	ev.POST("/:id/gates", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		b, ok := httpx.Bind[struct {
			Name string `json:"name" binding:"required,max=40"`
		}](c)
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		v, err := s.GateAdmin.CreateGate(c.Request.Context(), p, sc, id, b.Name)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.Created(c, v)
	})
	ev.DELETE("/:id/gates/:gate_id", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		gid, ok := httpx.ParamID(c, "gate_id")
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		if err := s.GateAdmin.DeleteGate(c.Request.Context(), p, sc, id, gid); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})
	ev.GET("/:id/gate-staff", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		_, sc := orgCtx(c)
		l, err := s.GateAdmin.Staff(c.Request.Context(), sc, id)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, l)
	})
	ev.POST("/:id/gate-staff", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		b, ok := httpx.Bind[struct {
			Email  string `json:"email" binding:"required,max=254"`
			GateID string `json:"gate_id" binding:"omitempty,len=26"`
		}](c)
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		v, err := s.GateAdmin.AssignStaff(c.Request.Context(), p, sc, id, b.Email, b.GateID)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.Created(c, v)
	})
	ev.DELETE("/:id/gate-staff/:user_id", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		uid, ok := httpx.ParamID(c, "user_id")
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		if err := s.GateAdmin.RemoveStaff(c.Request.Context(), p, sc, id, uid); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})
	_ = apperr.Internal
}
