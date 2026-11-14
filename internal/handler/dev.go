package handler

import (
	"github.com/gin-gonic/gin"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/httpx"
	"tixgo/backend/internal/payment"
)

// mountDev: simulasi pembayaran SAAT PAYMENT_GATEWAY=fake (tak pernah aktif di production — dijaga config + pemeriksaan tipe).
// Menggantikan webhook Midtrans pada pengembangan FE/tanpa kunci sandbox.
func (h *H) mountDev(rt *Routes) {
	s := h.S
	f, ok := s.Gateway.(*payment.Fake)
	if !ok || s.Cfg.IsProd() {
		return
	}
	rt.Authed.POST("/dev/payments/settle", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			OrderID string `json:"order_id" binding:"required,len=26"`
			Method  string `json:"method" binding:"omitempty,max=30"`
		}](c)
		if !ok {
			return
		}
		pay, err := s.Q.LatestPaymentForOrder(c.Request.Context(), b.OrderID)
		if err != nil || pay.ProviderRef == nil {
			httpx.Fail(c, apperr.NotFound("pembayaran (jalankan /orders/{id}/pay dulu)"))
			return
		}
		method := b.Method
		if method == "" {
			method = "gopay"
		}
		if err := s.Payment.HandleNotification(c.Request.Context(), f.Settle(*pay.ProviderRef, method), c.Request.Header); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})
}
