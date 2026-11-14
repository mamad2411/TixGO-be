package handler

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"tixgo/backend/internal/httpx"
	"tixgo/backend/internal/middleware"
	"tixgo/backend/internal/service"
)

var errHandled = errors.New("handled")

// mountOrganizerFinance: saldo, mutasi, pencairan, laporan settlement (M34). Dipasang di grup organizer.
func (h *H) mountOrganizerFinance(m *gin.RouterGroup) {
	s := h.S
	fin := m.Group("/finance")
	fin.GET("/balance", func(c *gin.Context) {
		_, sc := orgCtx(c)
		v, err := s.Finance.Balance(c.Request.Context(), sc)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	fin.GET("/statement", func(c *gin.Context) {
		_, sc := orgCtx(c)
		page, per, off := httpx.Page(c)
		items, total, err := s.Finance.Statement(c.Request.Context(), sc, per, off)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.List(c, items, httpx.Meta{Page: page, PerPage: per, Total: total})
	})
	fin.GET("/payouts", func(c *gin.Context) {
		_, sc := orgCtx(c)
		page, per, off := httpx.Page(c)
		items, total, err := s.Finance.OrgPayouts(c.Request.Context(), sc, per, off)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.List(c, items, httpx.Meta{Page: page, PerPage: per, Total: total})
	})
	fin.POST("/payouts", middleware.Idempotency(s), func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Amount int64 `json:"amount" binding:"required,min=1"`
		}](c)
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		v, err := s.Finance.RequestPayout(c.Request.Context(), p, sc, b.Amount)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.Created(c, v)
	})
	fin.GET("/events/:id/settlement", func(c *gin.Context) {
		_, sc := orgCtx(c)
		v, err := s.Finance.EventSettlement(c.Request.Context(), sc, c.Param("id"))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	fin.GET("/events/:id/settlement.xlsx", func(c *gin.Context) {
		_, sc := orgCtx(c)
		v, err := s.Finance.EventSettlement(c.Request.Context(), sc, c.Param("id"))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		sendXLSX(c, s, v)
	})
}

func sendXLSX(c *gin.Context, s *service.Services, v *service.SettlementReport) {
	b, err := s.Finance.XLSX(v)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Content-Disposition", `attachment; filename="settlement-`+v.EventID+`.xlsx"`)
	c.Data(http.StatusOK, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", b)
}

// mountAdminFinance: antrean pencairan, rekonsiliasi, kesehatan ledger. Role finance/admin + MFA.
func (h *H) mountAdminFinance(rt *Routes) {
	s := h.S
	f := rt.V1.Group("/admin/finance", middleware.Auth(s, true), middleware.RequireRole("finance", "admin"), middleware.RequireMFA())
	f.GET("/payouts", func(c *gin.Context) {
		page, per, off := httpx.Page(c)
		items, total, err := s.Finance.AdminPayouts(c.Request.Context(), c.Query("status"), per, off)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.List(c, items, httpx.Meta{Page: page, PerPage: per, Total: total})
	})
	act := func(path string, fn func(c *gin.Context) (any, error)) {
		f.POST(path, func(c *gin.Context) {
			v, err := fn(c)
			if errors.Is(err, errHandled) {
				return
			}
			if err != nil {
				httpx.Fail(c, err)
				return
			}
			httpx.OK(c, v)
		})
	}
	type noteBody struct {
		Note string `json:"note" binding:"omitempty,max=500"`
	}
	act("/payouts/:id/approve", func(c *gin.Context) (any, error) {
		return s.Finance.Approve(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id"))
	})
	act("/payouts/:id/reject", func(c *gin.Context) (any, error) {
		b, ok := httpx.Bind[noteBody](c)
		if !ok {
			return nil, errHandled
		}
		return s.Finance.Reject(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id"), b.Note)
	})
	act("/payouts/:id/process", func(c *gin.Context) (any, error) {
		return s.Finance.StartProcessing(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id"))
	})
	act("/payouts/:id/fail", func(c *gin.Context) (any, error) {
		b, ok := httpx.Bind[noteBody](c)
		if !ok {
			return nil, errHandled
		}
		return s.Finance.MarkFailed(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id"), b.Note)
	})
	act("/payouts/:id/mark-paid", func(c *gin.Context) (any, error) {
		proof, ok := readUpload(c, "proof", 5<<20)
		if !ok {
			return nil, errHandled
		}
		return s.Finance.MarkPaid(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id"), proof, c.PostForm("note"))
	})
	f.GET("/events/:id/settlement", func(c *gin.Context) {
		v, err := s.Finance.AdminEventSettlement(c.Request.Context(), c.Param("id"))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	f.GET("/events/:id/settlement.xlsx", func(c *gin.Context) {
		v, err := s.Finance.AdminEventSettlement(c.Request.Context(), c.Param("id"))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		sendXLSX(c, s, v)
	})
	f.GET("/ledger-health", func(c *gin.Context) {
		v, err := s.Finance.Health(c.Request.Context())
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	f.GET("/recon", func(c *gin.Context) {
		page, per, off := httpx.Page(c)
		items, total, err := s.Finance.ListRecon(c.Request.Context(), c.Query("status"), per, off)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.List(c, items, httpx.Meta{Page: page, PerPage: per, Total: total})
	})
	f.POST("/recon/import", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			From time.Time            `json:"from" binding:"required"`
			To   time.Time            `json:"to" binding:"required"`
			Rows []service.GatewayRow `json:"rows" binding:"required,min=1,max=20000,dive"`
		}](c)
		if !ok {
			return
		}
		v, err := s.Finance.ReconcileReport(c.Request.Context(), httpx.MustPrincipal(c), b.Rows, b.From, b.To)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	f.GET("/orders", func(c *gin.Context) {
		_, per, off := httpx.Page(c)
		v, err := s.AdminOrders.Search(c.Request.Context(), httpx.MustPrincipal(c), c.Query("q"), c.Query("status"), per, off)
		respond(c, v, err)
	})
	f.GET("/orders/:id", func(c *gin.Context) {
		v, err := s.AdminOrders.Get(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id"))
		respond(c, v, err)
	})
	f.POST("/orders/:id/resync", func(c *gin.Context) {
		v, err := s.AdminOrders.Resync(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id"))
		respond(c, v, err)
	})
	f.POST("/orders/:id/refund", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Reason string `json:"reason" binding:"required,max=300"`
		}](c)
		if !ok {
			return
		}
		v, err := s.AdminOrders.Refund(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id"), b.Reason)
		created(c, v, err)
	})
	f.POST("/disputes", func(c *gin.Context) {
		b, ok := httpx.Bind[service.DisputeInput](c)
		if !ok {
			return
		}
		v, err := s.Finance.OpenDispute(c.Request.Context(), httpx.MustPrincipal(c), b)
		created(c, v, err)
	})
	f.POST("/disputes/:id/evidence", func(c *gin.Context) {
		data, ok := readUpload(c, "file", 5<<20)
		if !ok {
			return
		}
		noContent(c, s.Finance.AddDisputeEvidence(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id"), data, c.PostForm("name")))
	})
	f.POST("/disputes/:id/resolve", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Outcome string `json:"outcome" binding:"required"`
			Note    string `json:"note" binding:"omitempty,max=500"`
		}](c)
		if !ok {
			return
		}
		noContent(c, s.Finance.ResolveDispute(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id"), b.Outcome, b.Note))
	})
	f.POST("/recon/:id/resolve", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Status     string `json:"status" binding:"required"`
			Resolution string `json:"resolution" binding:"omitempty,max=500"`
		}](c)
		if !ok {
			return
		}
		if err := s.Finance.ResolveRecon(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id"), b.Status, b.Resolution); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})
}
