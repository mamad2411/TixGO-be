package handler

import (
	"time"

	"github.com/gin-gonic/gin"

	"tixgo/backend/internal/httpx"
	"tixgo/backend/internal/middleware"
)

// mountWS: tiket WS + endpoint WebSocket (docs/04 §7). Tiket sekali pakai 60 dtk; tanpa tiket = anonim (kanal publik saja).
func (h *H) mountWS(rt *Routes) {
	s := h.S
	rt.Authed.POST("/ws/ticket", middleware.RateLimit(s, "wst", 60, time.Minute, middleware.ByUser), func(c *gin.Context) {
		t, err := s.WSTicket(c.Request.Context(), httpx.MustPrincipal(c).UserID)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, gin.H{"ticket": t, "expires_in": 60})
	})
	rt.V1.GET("/ws", middleware.RateLimit(s, "ws", 60, time.Minute, middleware.ByIP), func(c *gin.Context) {
		uid := ""
		if t := c.Query("ticket"); t != "" {
			u, ok := s.ConsumeWSTicket(c.Request.Context(), t)
			if !ok {
				c.AbortWithStatusJSON(401, gin.H{"error": gin.H{"code": "UNAUTHENTICATED", "message": "tiket WS tidak valid atau kedaluwarsa"}})
				return
			}
			uid = u
		}
		if err := s.WS.Serve(c.Writer, c.Request, uid); err != nil {
			s.Log.Warn("upgrade ws gagal", "error", err)
		}
	})
}
