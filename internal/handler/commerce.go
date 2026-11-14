package handler

import (
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/httpx"
	"tixgo/backend/internal/middleware"
	"tixgo/backend/internal/service"
)

// mountCommerce: antrean, hold, waitlist, kuotasi, order, bayar, webhook, tiket, invoice, refund (docs/20 M5–M9, M17).
func (h *H) mountCommerce(rt *Routes) {
	s := h.S
	a := rt.Authed
	ctxOf := func(c *gin.Context) (uid string) { return httpx.MustPrincipal(c).UserID }

	// antrean
	q := a.Group("/events/:slug/queue")
	q.POST("/join", middleware.RateLimit(s, "qjoin", 30, time.Minute, middleware.ByUser), func(c *gin.Context) {
		st, err := s.Queue.Join(c.Request.Context(), ctxOf(c), c.Param("slug"))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, st)
	})
	q.GET("/status", middleware.RateLimit(s, "qstat", 120, time.Minute, middleware.ByUser), func(c *gin.Context) {
		st, err := s.Queue.Status(c.Request.Context(), ctxOf(c), c.Param("slug"))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, st)
	})
	q.DELETE("", func(c *gin.Context) {
		if err := s.Queue.Leave(c.Request.Context(), ctxOf(c), c.Param("slug")); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})
	rt.V1.GET("/events/:slug/queue/proof", func(c *gin.Context) {
		p, err := s.Queue.Proof(c.Request.Context(), c.Param("slug"))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, p)
	})

	// hold
	a.POST("/events/:slug/holds", middleware.RateLimit(s, "hold", 30, time.Minute, middleware.ByUser), func(c *gin.Context) {
		b, ok := httpx.Bind[service.HoldRequest](c)
		if !ok {
			return
		}
		v, err := s.Hold.Create(c.Request.Context(), ctxOf(c), c.Param("slug"), b, c.GetHeader("X-Admission-Token"))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.Created(c, v)
	})
	a.GET("/holds/:id", func(c *gin.Context) {
		v, err := s.Hold.Get(c.Request.Context(), ctxOf(c), c.Param("id"))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	a.DELETE("/holds/:id", func(c *gin.Context) {
		if err := s.Hold.Release(c.Request.Context(), ctxOf(c), c.Param("id")); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})
	a.POST("/holds/:id/extend", func(c *gin.Context) {
		v, err := s.Hold.Extend(c.Request.Context(), ctxOf(c), c.Param("id"))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})

	// waitlist
	a.GET("/waitlists", func(c *gin.Context) {
		v, err := s.Waitlist.List(c.Request.Context(), ctxOf(c))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	a.POST("/events/:slug/waitlist", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			TierID string `json:"tier_id" binding:"required,len=26"`
			Qty    int    `json:"qty" binding:"required,min=1,max=10"`
		}](c)
		if !ok {
			return
		}
		v, err := s.Waitlist.Join(c.Request.Context(), ctxOf(c), c.Param("slug"), b.TierID, b.Qty)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.Created(c, v)
	})
	a.DELETE("/events/:slug/waitlist/:tier_id", func(c *gin.Context) {
		if err := s.Waitlist.Leave(c.Request.Context(), ctxOf(c), c.Param("slug"), c.Param("tier_id")); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})

	// order
	a.POST("/orders/quote", func(c *gin.Context) {
		b, ok := httpx.Bind[service.QuoteReq](c)
		if !ok {
			return
		}
		v, err := s.Order.Quote(c.Request.Context(), ctxOf(c), b)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	a.POST("/orders", middleware.Idempotency(s), middleware.RateLimit(s, "order", 20, time.Minute, middleware.ByUser), func(c *gin.Context) {
		b, ok := httpx.Bind[service.CreateOrderReq](c)
		if !ok {
			return
		}
		v, err := s.Order.Create(c.Request.Context(), ctxOf(c), b)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.Created(c, v)
	})
	a.GET("/orders", func(c *gin.Context) {
		page, per, off := httpx.Page(c)
		items, total, err := s.Order.List(c.Request.Context(), ctxOf(c), per, off)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.List(c, items, httpx.Meta{Page: page, PerPage: per, Total: total})
	})
	a.GET("/orders/:id", func(c *gin.Context) {
		v, err := s.Order.Get(c.Request.Context(), ctxOf(c), c.Param("id"))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	a.POST("/orders/:id/cancel", func(c *gin.Context) {
		if err := s.Order.Cancel(c.Request.Context(), ctxOf(c), c.Param("id")); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})
	a.POST("/orders/:id/pay", middleware.Idempotency(s), middleware.RateLimit(s, "pay", 20, time.Minute, middleware.ByUser), func(c *gin.Context) {
		v, err := s.Order.Pay(c.Request.Context(), ctxOf(c), c.Param("id"))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	a.GET("/orders/:id/invoice.pdf", func(c *gin.Context) {
		b, name, err := s.Invoice.PDF(c.Request.Context(), ctxOf(c), c.Param("id"))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		c.Header("Content-Disposition", `attachment; filename="`+name+`"`)
		c.Data(http.StatusOK, "application/pdf", b)
	})

	// webhook gateway (Midtrans/Xendit; publik; keabsahan diverifikasi di service, balas 200 setelah diproses)
	webhook := func(c *gin.Context) {
		raw, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
		if err != nil {
			c.String(http.StatusBadRequest, "bad body")
			return
		}
		if err := s.Payment.HandleNotification(c.Request.Context(), raw, c.Request.Header); err != nil {
			httpx.Fail(c, err)
			return
		}
		c.String(http.StatusOK, "OK")
	}
	rt.Root.POST("/webhooks/midtrans", webhook)
	rt.Root.POST("/webhooks/xendit", webhook)

	// tiket
	a.GET("/tickets", func(c *gin.Context) {
		page, per, off := httpx.Page(c)
		items, total, err := s.Ticket.List(c.Request.Context(), ctxOf(c), c.Query("status"), per, off)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.List(c, items, httpx.Meta{Page: page, PerPage: per, Total: total})
	})
	a.GET("/tickets/:id", func(c *gin.Context) {
		v, err := s.Ticket.Get(c.Request.Context(), ctxOf(c), c.Param("id"))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	a.GET("/tickets/:id/qr", func(c *gin.Context) {
		v, err := s.Ticket.QRPayload(c.Request.Context(), ctxOf(c), c.Param("id"))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})

	// kirim tiket (tautan klaim)
	a.POST("/tickets/:id/transfer", middleware.RateLimit(s, "xfer", 20, time.Hour, middleware.ByUser), func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Email string `json:"email" binding:"omitempty,email,max=254"`
		}](c)
		if !ok {
			return
		}
		v, err := s.Transfer.CreateLink(c.Request.Context(), ctxOf(c), c.Param("id"), b.Email)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.Created(c, v)
	})
	a.DELETE("/tickets/:id/transfer", func(c *gin.Context) {
		if err := s.Transfer.Revoke(c.Request.Context(), ctxOf(c), c.Param("id")); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})
	a.GET("/claims/:token", middleware.RateLimit(s, "claimv", 30, time.Minute, middleware.ByUser), func(c *gin.Context) {
		v, err := s.Transfer.Preview(c.Request.Context(), c.Param("token"))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	a.POST("/claims/:token/accept", middleware.RateLimit(s, "claim", 10, time.Minute, middleware.ByUser), func(c *gin.Context) {
		v, err := s.Transfer.Claim(c.Request.Context(), ctxOf(c), c.Param("token"))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})

	// refund pembeli
	a.GET("/orders/:id/refund-eligibility", func(c *gin.Context) {
		v, err := s.Refund.Preview(c.Request.Context(), ctxOf(c), c.Param("id"))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	a.GET("/orders/:id/refunds", func(c *gin.Context) {
		v, err := s.Refund.ForOrder(c.Request.Context(), ctxOf(c), c.Param("id"))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	a.POST("/orders/:id/refunds", middleware.Idempotency(s), func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Reason string `json:"reason" binding:"omitempty,max=500"`
		}](c)
		if !ok {
			return
		}
		v, err := s.Refund.Request(c.Request.Context(), ctxOf(c), c.Param("id"), b.Reason)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.Created(c, v)
	})
	a.GET("/refunds/:id", func(c *gin.Context) {
		v, err := s.Refund.Get(c.Request.Context(), ctxOf(c), c.Param("id"))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	a.PUT("/refunds/:id/account", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			BankCode    string `json:"bank_code" binding:"required,max=20"`
			AccountName string `json:"account_name" binding:"required,max=100"`
			AccountNo   string `json:"account_no" binding:"required,max=30"`
		}](c)
		if !ok {
			return
		}
		v, err := s.Refund.SetAccount(c.Request.Context(), ctxOf(c), c.Param("id"), b.BankCode, b.AccountName, b.AccountNo)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		s.Refund.Kick(c.Request.Context(), v.OrderID)
		httpx.OK(c, v)
	})

	// finance: refund manual
	f := rt.V1.Group("/admin/finance", middleware.Auth(s, true), middleware.RequireRole("finance", "admin"), middleware.RequireMFA())
	f.GET("/refunds", func(c *gin.Context) {
		page, per, off := httpx.Page(c)
		items, total, err := s.Refund.FinanceList(c.Request.Context(), httpx.MustPrincipal(c), c.Query("status"), c.Query("method"), per, off)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.List(c, items, httpx.Meta{Page: page, PerPage: per, Total: total})
	})
	f.POST("/refunds/:id/mark-paid", func(c *gin.Context) {
		fh, err := c.FormFile("proof")
		if err != nil {
			httpx.Fail(c, apperr.Validation("bukti transfer (proof) wajib"))
			return
		}
		fd, err := fh.Open()
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		defer func() { _ = fd.Close() }()
		data, err := io.ReadAll(io.LimitReader(fd, 5<<20))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		v, err := s.Refund.MarkPaid(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id"), data, c.PostForm("note"))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
}
