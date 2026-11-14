// Package handler = lapisan HTTP tipis: bind+validasi → service → envelope (docs/08).
package handler

import (
	"github.com/gin-gonic/gin"

	"tixgo/backend/internal/middleware"
	"tixgo/backend/internal/service"
)

// H menampung service; tiap berkas domain menambahkan method mount*.
type H struct{ S *service.Services }

// Routes = grup route yang dibagikan ke semua mount*.
type Routes struct {
	Root   *gin.Engine
	V1     *gin.RouterGroup // publik
	Authed *gin.RouterGroup // butuh login
	Opt    *gin.RouterGroup // login opsional
}

// Register memasang semua route /api/v1 (dan beberapa route root).
func Register(r *gin.Engine, s *service.Services) *H {
	h := &H{S: s}
	v1 := r.Group("/api/v1")
	rt := &Routes{
		Root:   r,
		V1:     v1,
		Authed: v1.Group("", middleware.Auth(s, true)),
		Opt:    v1.Group("", middleware.Auth(s, false)),
	}
	h.mountAuth(rt)
	h.mountLegal(rt)
	h.mountCatalog(rt)
	h.mountSale(rt)
	h.mountCommerce(rt)
	h.mountGate(rt)
	h.mountAdminFinance(rt)
	h.mountOps(rt)
	h.mountAccount(rt)
	h.mountSupport(rt)
	h.mountSEO(rt)
	h.mountWS(rt)
	h.mountPartner(rt)
	h.mountCMS(rt)
	h.mountEngage(rt)
	h.mountAdminUsers(rt)
	h.mountPassport(rt)
	h.mountConcierge(rt)
	h.mountResale(rt)
	h.mountSquad(rt)
	h.mountDev(rt)
	h.mountOrganizer(rt)
	h.mountAdmin(rt)
	return h
}
