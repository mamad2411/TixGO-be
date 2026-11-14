package handler

import (
	"time"

	"github.com/gin-gonic/gin"

	"tixgo/backend/internal/httpx"
	"tixgo/backend/internal/middleware"
)

// mountResale: listing resale berplafon, pembelian, pembayaran ke penjual (docs/10 §9).
func (h *H) mountResale(rt *Routes) {
	s := h.S
	uid := func(c *gin.Context) string { return httpx.MustPrincipal(c).UserID }
	rt.V1.GET("/events/:slug/resale", func(c *gin.Context) {
		v, err := s.Resale.ForEvent(c.Request.Context(), c.Param("slug"))
		respond(c, v, err)
	})
	a := rt.Authed
	a.POST("/tickets/:id/resale", middleware.RateLimit(s, "relist", 30, time.Hour, middleware.ByUser), func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Price int64 `json:"price" binding:"required,min=1"`
		}](c)
		if !ok {
			return
		}
		v, err := s.Resale.CreateListing(c.Request.Context(), uid(c), c.Param("id"), b.Price)
		created(c, v, err)
	})
	a.GET("/me/resale/listings", func(c *gin.Context) {
		v, err := s.Resale.Mine(c.Request.Context(), uid(c))
		respond(c, v, err)
	})
	a.DELETE("/resale/listings/:id", func(c *gin.Context) { noContent(c, s.Resale.Cancel(c.Request.Context(), uid(c), c.Param("id"))) })
	a.POST("/resale/listings/:id/buy", middleware.Idempotency(s), middleware.RateLimit(s, "rebuy", 20, time.Minute, middleware.ByUser), func(c *gin.Context) {
		v, err := s.Resale.Buy(c.Request.Context(), uid(c), c.Param("id"))
		created(c, v, err)
	})
	a.GET("/me/resale/payouts", func(c *gin.Context) {
		v, err := s.Resale.MyPayouts(c.Request.Context(), uid(c))
		respond(c, v, err)
	})
	a.PUT("/resale/payouts/:id/account", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			BankCode    string `json:"bank_code" binding:"required,max=20"`
			AccountName string `json:"account_name" binding:"required,max=100"`
			AccountNo   string `json:"account_no" binding:"required,max=30"`
		}](c)
		if !ok {
			return
		}
		noContent(c, s.Resale.SetAccount(c.Request.Context(), uid(c), c.Param("id"), b.BankCode, b.AccountName, b.AccountNo))
	})

	f := rt.V1.Group("/admin/finance", middleware.Auth(s, true), middleware.RequireRole("finance", "admin"), middleware.RequireMFA())
	f.GET("/resale-payouts", func(c *gin.Context) {
		_, per, off := httpx.Page(c)
		v, err := s.Resale.FinanceList(c.Request.Context(), c.Query("status"), per, off)
		respond(c, v, err)
	})
	f.POST("/resale-payouts/:id/mark-paid", func(c *gin.Context) {
		proof, ok := readUpload(c, "proof", 5<<20)
		if !ok {
			return
		}
		noContent(c, s.Resale.MarkPaid(c.Request.Context(), httpx.MustPrincipal(c), c.Param("id"), proof))
	})
}
