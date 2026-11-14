package handler

import (
	"io"

	"github.com/gin-gonic/gin"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/httpx"
	"tixgo/backend/internal/middleware"
	"tixgo/backend/internal/service"
)

// orgCtx mengambil principal + scope organizer (setelah RequireOrganizer).
func orgCtx(c *gin.Context) (*auth.Principal, service.OrgScope) {
	return httpx.MustPrincipal(c), middleware.OrgFrom(c)
}

func readUpload(c *gin.Context, field string, max int64) ([]byte, bool) {
	fh, err := c.FormFile(field)
	if err != nil {
		httpx.Fail(c, apperr.Validation("berkas '"+field+"' wajib (multipart)"))
		return nil, false
	}
	f, err := fh.Open()
	if err != nil {
		httpx.Fail(c, apperr.Validation("berkas tidak terbaca"))
		return nil, false
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		httpx.Fail(c, apperr.Validation("berkas tidak terbaca"))
		return nil, false
	}
	if int64(len(b)) > max {
		httpx.Fail(c, apperr.Validation("berkas terlalu besar"))
		return nil, false
	}
	return b, true
}

func (h *H) mountOrganizer(rt *Routes) {
	s := h.S
	org := rt.Authed.Group("/organizer")

	// daftar jadi organizer: pelanggan terverifikasi email
	org.POST("/apply", func(c *gin.Context) {
		b, ok := httpx.Bind[service.ApplyReq](c)
		if !ok {
			return
		}
		v, err := s.Organizer.Apply(c.Request.Context(), httpx.MustPrincipal(c), b)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.Created(c, v)
	})

	m := org.Group("", middleware.RequireRole("organizer", "organizer_member"), middleware.RequireOrganizer(s, "owner", "member"))

	m.GET("", func(c *gin.Context) {
		_, sc := orgCtx(c)
		v, err := s.Organizer.Get(c.Request.Context(), sc.OrganizerID)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	m.PATCH("", func(c *gin.Context) {
		b, ok := httpx.Bind[service.OrgPatch](c)
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		v, err := s.Organizer.Update(c.Request.Context(), p, sc, b)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})

	// ---- KYB ----
	m.GET("/kyb", func(c *gin.Context) {
		_, sc := orgCtx(c)
		item, err := s.Organizer.AdminGet(c.Request.Context(), sc.OrganizerID)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, item)
	})
	m.POST("/kyb/documents", func(c *gin.Context) {
		p, sc := orgCtx(c)
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
		d, err := s.Organizer.UploadDocument(c.Request.Context(), p, sc, c.PostForm("kind"), f)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.Created(c, d)
	})
	m.GET("/kyb/documents", func(c *gin.Context) {
		_, sc := orgCtx(c)
		d, err := s.Organizer.Documents(c.Request.Context(), sc.OrganizerID)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, d)
	})
	m.DELETE("/kyb/documents/:id", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		if err := s.Organizer.DeleteDocument(c.Request.Context(), p, sc, id); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})
	m.POST("/kyb/contract", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			DocumentID string `json:"document_id" binding:"required,len=26"`
		}](c)
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		if err := s.Organizer.AcceptContract(c.Request.Context(), p, sc, b.DocumentID); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})
	m.POST("/kyb/submit", func(c *gin.Context) {
		p, sc := orgCtx(c)
		v, err := s.Organizer.SubmitKYB(c.Request.Context(), p, sc)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})

	// ---- rekening ----
	m.GET("/bank-accounts", func(c *gin.Context) {
		_, sc := orgCtx(c)
		l, err := s.Organizer.BankAccounts(c.Request.Context(), sc.OrganizerID)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, l)
	})
	m.POST("/bank-accounts", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			BankCode    string `json:"bank_code" binding:"required,max=16"`
			AccountNo   string `json:"account_no" binding:"required,max=32"`
			AccountName string `json:"account_name" binding:"required,max=100"`
		}](c)
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		v, err := s.Organizer.AddBankAccount(c.Request.Context(), p, sc, b.BankCode, b.AccountNo, b.AccountName)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.Created(c, v)
	})
	m.DELETE("/bank-accounts/:id", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		if err := s.Organizer.RemoveBankAccount(c.Request.Context(), p, sc, id); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})

	// ---- tim ----
	m.GET("/team", func(c *gin.Context) {
		_, sc := orgCtx(c)
		l, err := s.Organizer.Team(c.Request.Context(), sc.OrganizerID)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, l)
	})
	m.POST("/team", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Email string `json:"email" binding:"required,max=254"`
			Role  string `json:"role" binding:"required,oneof=member gate_staff"`
		}](c)
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		v, err := s.Organizer.AddMember(c.Request.Context(), p, sc, b.Email, b.Role)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.Created(c, v)
	})
	m.DELETE("/team/:user_id", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "user_id")
		if !ok {
			return
		}
		p, sc := orgCtx(c)
		if err := s.Organizer.RemoveMember(c.Request.Context(), p, sc, id); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})

	// ---- venue & artis (master data) ----
	m.POST("/venues", func(c *gin.Context) {
		b, ok := httpx.Bind[service.VenueInput](c)
		if !ok {
			return
		}
		v, err := s.Venue.Create(c.Request.Context(), httpx.MustPrincipal(c), b)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.Created(c, v)
	})
	m.PATCH("/venues/:id", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		b, ok := httpx.Bind[service.VenueInput](c)
		if !ok {
			return
		}
		v, err := s.Venue.Update(c.Request.Context(), httpx.MustPrincipal(c), id, b)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	type artistBody struct {
		Name         *string        `json:"name" binding:"omitempty,max=120"`
		Bio          *string        `json:"bio" binding:"omitempty,max=5000"`
		ImageAssetID *string        `json:"image_asset_id" binding:"omitempty,len=26"`
		Socials      map[string]any `json:"socials"`
	}
	m.POST("/artists", func(c *gin.Context) {
		b, ok := httpx.Bind[artistBody](c)
		if !ok {
			return
		}
		if b.Name == nil {
			httpx.Fail(c, apperr.Validation("name wajib").With("field", "name"))
			return
		}
		v, err := s.Artist.Create(c.Request.Context(), httpx.MustPrincipal(c), *b.Name, b.Bio, b.ImageAssetID, b.Socials)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.Created(c, v)
	})
	m.PATCH("/artists/:id", func(c *gin.Context) {
		id, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		b, ok := httpx.Bind[artistBody](c)
		if !ok {
			return
		}
		v, err := s.Artist.Update(c.Request.Context(), httpx.MustPrincipal(c), id, b.Name, b.Bio, b.ImageAssetID, b.Socials)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})

	h.mountOrganizerEvents(m)
	h.mountOrganizerFinance(m)
	h.mountOrganizerIntegration(m)
	h.mountAnalytics(m, rt)
	h.mountReviews(rt, m)
}
