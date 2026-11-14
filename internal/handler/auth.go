package handler

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/httpx"
	"tixgo/backend/internal/middleware"
	"tixgo/backend/internal/service"
)

const refreshCookie = "tixgo_rt"

func (h *H) setRefreshCookie(c *gin.Context, sess *service.Session) {
	maxAge := int(time.Until(sess.RefreshExpires).Seconds())
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(refreshCookie, sess.RefreshToken, maxAge, "/api/v1/auth", "", h.S.Cfg.IsProd(), true)
}

func (h *H) clearRefreshCookie(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(refreshCookie, "", -1, "/api/v1/auth", "", h.S.Cfg.IsProd(), true)
}

// csrfOK: endpoint berbasis cookie (refresh/logout) wajib header kustom + Origin yang dikenal (docs/12 §3).
func (h *H) csrfOK(c *gin.Context) bool {
	if c.GetHeader("X-Requested-With") == "" {
		httpx.Fail(c, apperr.ForbiddenCode(apperr.CodeForbidden, "header X-Requested-With diperlukan"))
		return false
	}
	if origin := c.GetHeader("Origin"); origin != "" {
		for _, o := range h.S.Cfg.CORSOrigins {
			if strings.EqualFold(o, origin) {
				return true
			}
		}
		if u, err := url.Parse(origin); err == nil && u.Host == c.Request.Host {
			return true
		}
		httpx.Fail(c, apperr.Forbidden("origin tidak diizinkan"))
		return false
	}
	return true
}

type registerBody struct {
	Email       string `json:"email" binding:"required,max=254"`
	Password    string `json:"password" binding:"required,max=128"`
	Name        string `json:"name" binding:"required,min=2,max=100"`
	Locale      string `json:"locale" binding:"omitempty,oneof=id en"`
	AcceptTerms bool   `json:"accept_terms" binding:"required"`
}

type loginBody struct {
	Email     string `json:"email" binding:"required,max=254"`
	Password  string `json:"password" binding:"required,max=128"`
	MFACode   string `json:"mfa_code" binding:"omitempty,max=32"`
	MFAMethod string `json:"mfa_method" binding:"omitempty,oneof=totp email sms"`
}

func (h *H) mountAuth(rt *Routes) {
	s := h.S
	g := rt.V1.Group("/auth")
	g.POST("/register", middleware.RateLimit(s, "reg", 20, time.Hour, middleware.ByIP), func(c *gin.Context) {
		b, ok := httpx.Bind[registerBody](c)
		if !ok {
			return
		}
		sess, err := s.Auth.Register(c.Request.Context(), service.RegisterReq{Email: b.Email, Password: b.Password, Name: b.Name, Locale: b.Locale, AcceptTerms: b.AcceptTerms})
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		h.setRefreshCookie(c, sess)
		httpx.Created(c, sess)
	})
	g.POST("/login", func(c *gin.Context) {
		b, ok := httpx.Bind[loginBody](c)
		if !ok {
			return
		}
		sess, err := s.Auth.Login(c.Request.Context(), service.LoginReq{Email: b.Email, Password: b.Password, MFACode: b.MFACode, MFAMethod: b.MFAMethod})
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		h.setRefreshCookie(c, sess)
		httpx.OK(c, sess)
	})
	// Langkah antara sandi dan kode: kirim OTP (email/SMS) setelah sandi terbukti benar. Dibatasi per IP dan per akun.
	g.POST("/mfa/send", middleware.RateLimit(s, "mfasend", 20, time.Minute, middleware.ByIP), func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Email    string `json:"email" binding:"required,max=254"`
			Password string `json:"password" binding:"required,max=128"`
			Method   string `json:"method" binding:"required,oneof=email sms"`
		}](c)
		if !ok {
			return
		}
		to, err := s.Auth.SendLoginOTP(c.Request.Context(), service.LoginReq{Email: b.Email, Password: b.Password}, b.Method)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.Accepted(c, gin.H{"sent": true, "to": to, "expires_in": int(s.Cfg.OTPTTL.Seconds())})
	})
	g.POST("/refresh", func(c *gin.Context) {
		if !h.csrfOK(c) {
			return
		}
		raw, _ := c.Cookie(refreshCookie)
		sess, err := s.Auth.Refresh(c.Request.Context(), raw)
		if err != nil {
			h.clearRefreshCookie(c)
			httpx.Fail(c, err)
			return
		}
		h.setRefreshCookie(c, sess)
		httpx.OK(c, sess)
	})
	g.POST("/password/forgot", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Email string `json:"email" binding:"required,max=254"`
		}](c)
		if !ok {
			return
		}
		s.Auth.ForgotPassword(c.Request.Context(), b.Email)
		httpx.Accepted(c, gin.H{"message": "jika email terdaftar, tautan reset telah dikirim"}) // selalu 202
	})
	// Reset sandi: faktor kedua yang dibutuhkan akun ini, dan pengiriman OTP-nya (token tautan belum dihabiskan).
	g.POST("/password/reset/info", middleware.RateLimit(s, "resetinfo", 30, time.Minute, middleware.ByIP), func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Token string `json:"token" binding:"required,max=200"`
		}](c)
		if !ok {
			return
		}
		v, err := s.Auth.ResetInfo(c.Request.Context(), b.Token)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	g.POST("/password/reset/send", middleware.RateLimit(s, "resetsend", 10, time.Minute, middleware.ByIP), func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Token  string `json:"token" binding:"required,max=200"`
			Method string `json:"method" binding:"required,oneof=email sms"`
		}](c)
		if !ok {
			return
		}
		to, err := s.Auth.ResetSendOTP(c.Request.Context(), b.Token, b.Method)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.Accepted(c, gin.H{"sent": true, "to": to, "expires_in": int(s.Cfg.OTPTTL.Seconds())})
	})
	g.POST("/password/reset", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Token     string `json:"token" binding:"required,max=200"`
			Password  string `json:"password" binding:"required,max=128"`
			MFACode   string `json:"mfa_code" binding:"omitempty,max=32"`
			MFAMethod string `json:"mfa_method" binding:"omitempty,oneof=totp email sms"`
		}](c)
		if !ok {
			return
		}
		if err := s.Auth.ResetPassword(c.Request.Context(), b.Token, b.Password, service.SecondFactor{Method: b.MFAMethod, Code: b.MFACode}); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, gin.H{"message": "kata sandi diperbarui; silakan masuk"})
	})
	g.POST("/email/verify", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Token string `json:"token" binding:"required,max=200"`
		}](c)
		if !ok {
			return
		}
		if err := s.Auth.VerifyEmail(c.Request.Context(), b.Token); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, gin.H{"verified": true})
	})
	g.POST("/email/change/confirm", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Token string `json:"token" binding:"required,max=200"`
		}](c)
		if !ok {
			return
		}
		if err := s.Auth.ConfirmEmailChange(c.Request.Context(), b.Token); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, gin.H{"changed": true})
	})

	// ---- butuh login ----
	ag := rt.Authed.Group("/auth")
	ag.POST("/logout", func(c *gin.Context) {
		p := httpx.MustPrincipal(c)
		if err := s.Auth.Logout(c.Request.Context(), p); err != nil {
			httpx.Fail(c, err)
			return
		}
		h.clearRefreshCookie(c)
		httpx.NoContent(c)
	})
	ag.POST("/logout-all", func(c *gin.Context) {
		p := httpx.MustPrincipal(c)
		if err := s.Auth.LogoutAll(c.Request.Context(), p.UserID); err != nil {
			httpx.Fail(c, err)
			return
		}
		h.clearRefreshCookie(c)
		httpx.NoContent(c)
	})
	ag.POST("/email/verify/send", func(c *gin.Context) {
		p := httpx.MustPrincipal(c)
		if err := s.Auth.ResendVerifyEmail(c.Request.Context(), p.UserID); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.Accepted(c, gin.H{"sent": true})
	})
	ag.POST("/otp/send", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Phone    string `json:"phone" binding:"required,max=32"`
			Password string `json:"password" binding:"omitempty,max=128"`
		}](c)
		if !ok {
			return
		}
		p := httpx.MustPrincipal(c)
		if err := s.Auth.SendPhoneOTP(c.Request.Context(), p.UserID, b.Phone, b.Password); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.Accepted(c, gin.H{"sent": true, "expires_in": int(s.Cfg.OTPTTL.Seconds())})
	})
	ag.POST("/otp/verify", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Code string `json:"code" binding:"required,len=6,numeric"`
		}](c)
		if !ok {
			return
		}
		p := httpx.MustPrincipal(c)
		if err := s.Auth.VerifyPhoneOTP(c.Request.Context(), p.UserID, b.Code); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, gin.H{"phone_verified": true})
	})

	// ---- /me ----
	me := rt.Authed.Group("/me")
	me.GET("", func(c *gin.Context) {
		p := httpx.MustPrincipal(c)
		v, err := s.Auth.Me(c.Request.Context(), p.UserID)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	me.PATCH("", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Name   *string `json:"name" binding:"omitempty,min=2,max=100"`
			Locale *string `json:"locale" binding:"omitempty,oneof=id en"`
			// id aset hasil POST /media (kind=avatar); string kosong menghapus foto
			AvatarAssetID *string `json:"avatar_asset_id" binding:"omitempty,max=26"`
		}](c)
		if !ok {
			return
		}
		p := httpx.MustPrincipal(c)
		v, err := s.Auth.UpdateProfile(c.Request.Context(), p.UserID, service.ProfilePatch{Name: b.Name, Locale: b.Locale, AvatarAssetID: b.AvatarAssetID})
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	me.POST("/password", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Current   string `json:"current_password" binding:"required,max=128"`
			New       string `json:"new_password" binding:"required,max=128"`
			MFACode   string `json:"mfa_code" binding:"omitempty,max=32"`
			MFAMethod string `json:"mfa_method" binding:"omitempty,oneof=totp email sms"`
		}](c)
		if !ok {
			return
		}
		if err := s.Auth.ChangePassword(c.Request.Context(), httpx.MustPrincipal(c), b.Current, b.New, service.SecondFactor{Method: b.MFAMethod, Code: b.MFACode}); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, gin.H{"changed": true})
	})
	me.POST("/email", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Email     string `json:"email" binding:"required,max=254"`
			Password  string `json:"password" binding:"required,max=128"`
			MFACode   string `json:"mfa_code" binding:"omitempty,max=32"`
			MFAMethod string `json:"mfa_method" binding:"omitempty,oneof=totp email sms"`
		}](c)
		if !ok {
			return
		}
		if err := s.Auth.RequestEmailChange(c.Request.Context(), httpx.MustPrincipal(c).UserID, b.Email, b.Password, service.SecondFactor{Method: b.MFAMethod, Code: b.MFACode}); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.Accepted(c, gin.H{"message": "tautan konfirmasi dikirim ke email baru"})
	})
	me.GET("/devices", func(c *gin.Context) {
		d, err := s.Auth.Devices(c.Request.Context(), httpx.MustPrincipal(c))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, d)
	})
	me.DELETE("/devices/:id", func(c *gin.Context) {
		fam, ok := httpx.ParamID(c, "id")
		if !ok {
			return
		}
		if err := s.Auth.RevokeDevice(c.Request.Context(), httpx.MustPrincipal(c), fam); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})

	// ---- Verifikasi berlapis (OTP email/SMS) ----
	me.GET("/mfa", func(c *gin.Context) {
		v, err := s.Auth.MFAStatus(c.Request.Context(), httpx.MustPrincipal(c).UserID)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	otp := me.Group("/mfa/otp", middleware.RateLimit(s, "mfaotp", 20, time.Hour, middleware.ByUser))
	otp.POST("/:method/start", func(c *gin.Context) {
		to, err := s.Auth.StartEnrollOTP(c.Request.Context(), httpx.MustPrincipal(c).UserID, c.Param("method"))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.Accepted(c, gin.H{"sent": true, "to": to, "expires_in": int(s.Cfg.OTPTTL.Seconds())})
	})
	otp.POST("/:method/confirm", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Code string `json:"code" binding:"required,max=16"`
		}](c)
		if !ok {
			return
		}
		codes, tok, exp, err := s.Auth.ConfirmEnrollOTP(c.Request.Context(), httpx.MustPrincipal(c), c.Param("method"), b.Code)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, gin.H{"recovery_codes": codes, "access_token": tok, "expires_at": exp})
	})
	// kode untuk aksi sensitif (ganti sandi/email, hapus akun, matikan faktor) saat sudah masuk
	me.POST("/mfa/step-up/send", middleware.RateLimit(s, "mfastep", 20, time.Hour, middleware.ByUser), func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Method string `json:"method" binding:"required,oneof=email sms"`
		}](c)
		if !ok {
			return
		}
		to, err := s.Auth.SendStepUp(c.Request.Context(), httpx.MustPrincipal(c).UserID, b.Method)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.Accepted(c, gin.H{"sent": true, "to": to, "expires_in": int(s.Cfg.OTPTTL.Seconds())})
	})
	me.DELETE("/mfa/methods/:method", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Password   string `json:"password" binding:"required,max=128"`
			Code       string `json:"code" binding:"required,max=32"`
			CodeMethod string `json:"code_method" binding:"omitempty,oneof=totp email sms"`
		}](c)
		if !ok {
			return
		}
		if err := s.Auth.DisableMethod(c.Request.Context(), httpx.MustPrincipal(c).UserID, c.Param("method"), b.Password, b.CodeMethod, b.Code); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})

	// ---- MFA ----
	mfa := me.Group("/mfa/totp")
	mfa.POST("/setup", func(c *gin.Context) {
		v, err := s.Auth.SetupMFA(c.Request.Context(), httpx.MustPrincipal(c).UserID)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, v)
	})
	mfa.POST("/confirm", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Code string `json:"code" binding:"required,max=16"`
		}](c)
		if !ok {
			return
		}
		codes, tok, exp, err := s.Auth.ConfirmMFA(c.Request.Context(), httpx.MustPrincipal(c), b.Code)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, gin.H{"recovery_codes": codes, "access_token": tok, "expires_at": exp})
	})
	mfa.POST("/disable", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Password string `json:"password" binding:"required,max=128"`
			Code     string `json:"code" binding:"required,max=32"`
		}](c)
		if !ok {
			return
		}
		if err := s.Auth.DisableMFA(c.Request.Context(), httpx.MustPrincipal(c).UserID, b.Password, b.Code); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.NoContent(c)
	})
	mfa.POST("/recovery-codes", func(c *gin.Context) {
		b, ok := httpx.Bind[struct {
			Password string `json:"password" binding:"required,max=128"`
		}](c)
		if !ok {
			return
		}
		codes, err := s.Auth.RegenerateRecoveryCodes(c.Request.Context(), httpx.MustPrincipal(c).UserID, b.Password)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, gin.H{"recovery_codes": codes})
	})
}
