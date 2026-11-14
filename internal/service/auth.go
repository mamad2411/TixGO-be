package service

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/notify"
	"tixgo/backend/internal/platform"
	"tixgo/backend/internal/repository/sqlcgen"
	"tixgo/backend/internal/reqmeta"
)

// AuthService = akun, sesi, pemulihan, OTP HP, MFA (docs/20 M27, 06 M1).
type AuthService struct{ s *Services }

func newAuthService(s *Services) *AuthService { return &AuthService{s: s} }

const (
	maxFailedLogins   = 5
	lockDuration      = 15 * time.Minute
	passwordHistoryN  = 3
	recoveryCodeCount = 8
)

type UserView struct {
	ID            string    `json:"id"`
	Email         string    `json:"email"`
	Name          string    `json:"name"`
	Phone         *string   `json:"phone"`
	PhoneVerified bool      `json:"phone_verified"`
	EmailVerified bool      `json:"email_verified"`
	Role          string    `json:"role"`
	Locale        string    `json:"locale"`
	MFAEnabled    bool      `json:"mfa_enabled"`
	AvatarKey     *string   `json:"avatar_key,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

func userView(u sqlcgen.User) UserView {
	return UserView{
		ID: u.ID, Email: u.Email, Name: u.Name, Phone: u.Phone, PhoneVerified: u.PhoneVerifiedAt != nil,
		EmailVerified: u.EmailVerifiedAt != nil, Role: u.Role, Locale: u.Locale, MFAEnabled: u.MfaEnabled,
		AvatarKey: u.AvatarKey, CreatedAt: u.CreatedAt,
	}
}

type Session struct {
	AccessToken    string    `json:"access_token"`
	ExpiresAt      time.Time `json:"expires_at"`
	RefreshToken   string    `json:"-"`
	RefreshExpires time.Time `json:"-"`
	SessionID      string    `json:"session_id"`
	User           UserView  `json:"user"`
}

// ---------- Registrasi ----------

type RegisterReq struct {
	Email       string
	Password    string
	Name        string
	Locale      string
	AcceptTerms bool
}

func (a *AuthService) Register(ctx context.Context, r RegisterReq) (*Session, error) {
	email, ok := NormalizeEmail(r.Email)
	if !ok {
		return nil, apperr.Validation("email tidak valid").With("field", "email")
	}
	name := strings.TrimSpace(r.Name)
	if l := len([]rune(name)); l < 2 || l > 100 {
		return nil, apperr.Validation("nama harus 2–100 karakter").With("field", "name")
	}
	if err := auth.ValidatePasswordPolicy(r.Password); err != nil {
		return nil, apperr.Validation(err.Error()).With("field", "password")
	}
	if !r.AcceptTerms {
		return nil, apperr.Validation("syarat & ketentuan wajib disetujui").With("field", "accept_terms")
	}
	locale := r.Locale
	if locale != "en" {
		locale = "id"
	}
	m := reqmeta.From(ctx)
	if ok, _, err := a.s.Limiter.Allow(ctx, "register:ip:"+m.IP, 10, time.Hour); err == nil && !ok {
		return nil, apperr.TooMany("terlalu banyak pendaftaran dari jaringan ini")
	}
	if err := a.s.Fraud.CheckAccess(ctx, "", "", m.IP, m.DeviceID); err != nil {
		return nil, err
	}

	hash := a.s.Hasher.Hash(r.Password)
	var user sqlcgen.User
	var sess *Session
	err := a.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		user, err = q.CreateUser(ctx, sqlcgen.CreateUserParams{ID: id.New(), Email: email, PasswordHash: hash, Name: name, Locale: locale})
		if err != nil {
			if platform.IsUniqueViolation(err) {
				return apperr.Conflict(apperr.CodeEmailTaken, "email sudah terdaftar")
			}
			return err
		}
		if err := q.AddPasswordHistory(ctx, sqlcgen.AddPasswordHistoryParams{UserID: user.ID, PasswordHash: hash}); err != nil {
			return err
		}
		if err := a.s.Legal.recordSignupConsent(ctx, q, user.ID); err != nil {
			return err
		}
		sess, err = a.issueSession(ctx, q, user, "", false)
		return err
	})
	if err != nil {
		return nil, err
	}
	a.sendVerifyEmail(ctx, user)
	return sess, nil
}

// ---------- Sesi ----------

func (a *AuthService) issueSession(ctx context.Context, q *sqlcgen.Queries, u sqlcgen.User, family string, mfa bool) (*Session, error) {
	if family == "" {
		family = id.New()
	}
	m := reqmeta.From(ctx)
	raw := auth.RandomToken(32)
	exp := a.s.now().Add(a.s.Cfg.JWTRefreshTTL)
	p := sqlcgen.CreateRefreshTokenParams{
		ID: id.New(), UserID: u.ID, FamilyID: family, TokenHash: auth.HashToken(raw),
		DeviceLabel: strPtr(deviceLabel(m.UserAgent)), DeviceID: strPtr(m.DeviceID), UserAgent: strPtr(truncate(m.UserAgent, 300)),
		Mfa: mfa, ExpiresAt: exp,
	}
	if ip, err := netip.ParseAddr(m.IP); err == nil {
		p.Ip = &ip
	}
	if err := q.CreateRefreshToken(ctx, p); err != nil {
		return nil, err
	}
	access, accessExp, err := a.s.Tokens.IssueAccess(u.ID, u.Role, family, mfa)
	if err != nil {
		return nil, err
	}
	return &Session{AccessToken: access, ExpiresAt: accessExp, RefreshToken: raw, RefreshExpires: exp, SessionID: family, User: userView(u)}, nil
}

func deviceLabel(ua string) string {
	l := strings.ToLower(ua)
	switch {
	case strings.Contains(l, "iphone"), strings.Contains(l, "android"), strings.Contains(l, "mobile"):
		return "Perangkat seluler"
	case ua == "":
		return "Perangkat tidak dikenal"
	default:
		return "Peramban desktop"
	}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// revokeFamilies menandai keluarga sesi sebagai dicabut di Redis (access token ikut tidak berlaku).
func (a *AuthService) revokeFamilies(ctx context.Context, families ...string) {
	if len(families) == 0 {
		return
	}
	pipe := a.s.Redis.Pipeline()
	ttl := a.s.Tokens.AccessTTL() + time.Minute
	for _, f := range families {
		pipe.Set(ctx, "rev:sid:"+f, "1", ttl)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		a.s.Log.Error("gagal menandai pencabutan sesi", "error", err)
	}
}

// RevokeUserAccess mencabut seluruh akses user (blokir akun, reset sandi).
func (a *AuthService) RevokeUserAccess(ctx context.Context, userID string) {
	fams, err := a.s.Q.RevokeUserSessions(ctx, userID)
	if err != nil {
		a.s.Log.Error("cabut sesi gagal", "error", err)
	}
	a.revokeFamilies(ctx, fams...)
	// Pencabutan per-keluarga sesi (bukan per-user): sesi BARU setelahnya tidak ikut terblokir.
	// Akun diblokir ditolak di Login/Refresh lewat pemeriksaan status.
}

// IsRevoked dipakai middleware: sesi atau user dicabut.
func (a *AuthService) IsRevoked(ctx context.Context, userID, sid string) (bool, error) {
	n, err := a.s.Redis.Exists(ctx, "rev:sid:"+sid).Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ---------- Login ----------

type LoginReq struct {
	Email     string
	Password  string
	MFACode   string
	MFAMethod string // totp (default, termasuk kode pemulihan) | email | sms
}

// dummyHash menyamakan waktu respons saat email tidak ada (anti-enumerasi lewat timing).
func (a *AuthService) dummyVerify(pw string) { a.s.Hasher.Verify(pw, a.s.dummyHash) }

// checkPassword: semua pemeriksaan sebelum faktor kedua (laju, kunci akun, sandi, blokir, fraud). Dipakai Login dan
// pengiriman OTP login agar keduanya tidak bisa dipakai menebak sandi atau membedakan akun.
func (a *AuthService) checkPassword(ctx context.Context, r LoginReq) (sqlcgen.User, string, error) {
	email, ok := NormalizeEmail(r.Email)
	m := reqmeta.From(ctx)
	if ok2, ra, err := a.s.Limiter.Allow(ctx, "login:ip:"+m.IP, 30, time.Minute); err == nil && !ok2 {
		return sqlcgen.User{}, "", apperr.TooMany("terlalu banyak percobaan masuk").With("retry_after", int(ra.Seconds())+1)
	}
	if ok2, ra, err := a.s.Limiter.Allow(ctx, "login:email:"+email, 10, time.Minute); err == nil && !ok2 {
		return sqlcgen.User{}, "", apperr.TooMany("terlalu banyak percobaan masuk").With("retry_after", int(ra.Seconds())+1)
	}
	badCreds := apperr.New(401, apperr.CodeInvalidCredential, "email atau kata sandi salah")
	if !ok {
		a.dummyVerify(r.Password)
		return sqlcgen.User{}, "", badCreds
	}
	u, err := a.s.Q.GetUserByEmail(ctx, email)
	if err != nil {
		if platform.IsNoRows(err) {
			a.dummyVerify(r.Password)
			return sqlcgen.User{}, "", badCreds
		}
		return sqlcgen.User{}, "", err
	}
	if u.Status == "deleted" {
		a.dummyVerify(r.Password)
		return sqlcgen.User{}, "", badCreds
	}
	if u.LockedUntil != nil && u.LockedUntil.After(a.s.now()) {
		return sqlcgen.User{}, "", apperr.New(429, apperr.CodeAccountLocked, "akun dikunci sementara karena terlalu banyak percobaan").
			With("retry_after", int(u.LockedUntil.Sub(a.s.now()).Seconds())+1)
	}
	if !a.s.Hasher.Verify(r.Password, u.PasswordHash) {
		if err := a.recordFailure(ctx, u.ID); err != nil {
			return sqlcgen.User{}, "", err
		}
		return sqlcgen.User{}, "", badCreds
	}
	if u.Status == "blocked" {
		return sqlcgen.User{}, "", apperr.ForbiddenCode(apperr.CodeBlocked, "akun diblokir; hubungi bantuan")
	}
	if err := a.s.Fraud.CheckAccess(ctx, u.ID, deref(u.Phone), m.IP, m.DeviceID); err != nil {
		return sqlcgen.User{}, "", err
	}
	return u, m.IP, nil
}

func (a *AuthService) Login(ctx context.Context, r LoginReq) (*Session, error) {
	u, _, err := a.checkPassword(ctx, r)
	if err != nil {
		return nil, err
	}
	mfa := false
	if u.MfaEnabled {
		// Berlaku untuk SEMUA perangkat dan peramban: faktor kedua adalah sifat akun, bukan sifat perangkat.
		if strings.TrimSpace(r.MFACode) == "" {
			return nil, a.mfaRequired(ctx, u, "kode verifikasi dua langkah diperlukan")
		}
		okMFA, err := a.verifySecondFactor(ctx, a.s.Q, u, otpPurposeLogin, r.MFAMethod, r.MFACode)
		if err != nil {
			return nil, err
		}
		if !okMFA {
			if err := a.recordFailure(ctx, u.ID); err != nil {
				return nil, err
			}
			return nil, a.mfaRequired(ctx, u, "kode verifikasi salah")
		}
		mfa = true
	}
	var sess *Session
	err = a.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		if err := q.RecordLoginSuccess(ctx, u.ID); err != nil {
			return err
		}
		var err error
		sess, err = a.issueSession(ctx, q, u, "", mfa)
		return err
	})
	return sess, err
}

func (a *AuthService) recordFailure(ctx context.Context, userID string) error {
	n, err := a.s.Q.RecordLoginFailure(ctx, userID)
	if err != nil {
		return err
	}
	if int(n) >= maxFailedLogins {
		until := a.s.now().Add(lockDuration)
		return a.s.Q.LockUserUntil(ctx, sqlcgen.LockUserUntilParams{ID: userID, LockedUntil: &until})
	}
	return nil
}

// ---------- Refresh & logout ----------

func (a *AuthService) Refresh(ctx context.Context, raw string) (*Session, error) {
	if raw == "" {
		return nil, apperr.New(401, apperr.CodeTokenInvalid, "refresh token tidak ada")
	}
	hash := auth.HashToken(raw)
	var sess *Session
	var reuseFamily string
	err := a.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		row, err := q.GetRefreshTokenByHashForUpdate(ctx, hash)
		if err != nil {
			if platform.IsNoRows(err) {
				return apperr.New(401, apperr.CodeTokenInvalid, "refresh token tidak valid")
			}
			return err
		}
		if row.RevokedAt != nil {
			return apperr.New(401, apperr.CodeTokenInvalid, "sesi sudah berakhir")
		}
		if row.UsedAt != nil {
			// Token bekas dipakai ulang = indikasi pencurian → cabut seluruh keluarga (docs/12 §3).
			reuseFamily = row.FamilyID
			if err := q.RevokeSessionFamily(ctx, row.FamilyID); err != nil {
				return err
			}
			return nil
		}
		if row.ExpiresAt.Before(a.s.now()) {
			return apperr.New(401, apperr.CodeTokenExpired, "sesi kedaluwarsa, masuk lagi")
		}
		u, err := q.GetUserByID(ctx, row.UserID)
		if err != nil {
			return err
		}
		if u.Status != "active" {
			if err := q.RevokeSessionFamily(ctx, row.FamilyID); err != nil {
				return err
			}
			reuseFamily = row.FamilyID
			return nil
		}
		if err := q.MarkRefreshTokenUsed(ctx, row.ID); err != nil {
			return err
		}
		sess, err = a.issueSession(ctx, q, u, row.FamilyID, row.Mfa)
		return err
	})
	if err != nil {
		return nil, err
	}
	if reuseFamily != "" {
		a.revokeFamilies(ctx, reuseFamily)
		return nil, apperr.New(401, apperr.CodeTokenInvalid, "sesi tidak valid; masuk lagi")
	}
	return sess, nil
}

func (a *AuthService) Logout(ctx context.Context, p *auth.Principal) error {
	if err := a.s.Q.RevokeSessionFamily(ctx, p.SessionID); err != nil {
		return err
	}
	a.revokeFamilies(ctx, p.SessionID)
	return nil
}

func (a *AuthService) LogoutAll(ctx context.Context, userID string) error {
	fams, err := a.s.Q.RevokeUserSessions(ctx, userID)
	if err != nil {
		return err
	}
	a.revokeFamilies(ctx, fams...)
	return nil
}

// ---------- Profil ----------

type ProfileView struct {
	UserView
	Organizer *OrganizerBrief `json:"organizer,omitempty"`
}

type OrganizerBrief struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	KYBStatus string `json:"kyb_status"`
}

func (a *AuthService) Me(ctx context.Context, userID string) (*ProfileView, error) {
	u, err := a.s.Q.GetUserByID(ctx, userID)
	if err != nil {
		if platform.IsNoRows(err) {
			return nil, apperr.NotFound("pengguna")
		}
		return nil, err
	}
	pv := &ProfileView{UserView: userView(u)}
	if m, err := a.s.Q.GetMembership(ctx, userID); err == nil {
		pv.Organizer = &OrganizerBrief{ID: m.OrganizerID, Role: m.Role, KYBStatus: m.KybStatus}
	}
	return pv, nil
}

type ProfilePatch struct {
	Name          *string
	Locale        *string
	AvatarAssetID *string // id aset kind=avatar milik sendiri; "" = hapus foto
}

func (a *AuthService) UpdateProfile(ctx context.Context, userID string, p ProfilePatch) (*UserView, error) {
	arg := sqlcgen.UpdateUserProfileParams{ID: userID}
	if p.Name != nil {
		n := strings.TrimSpace(*p.Name)
		if l := len([]rune(n)); l < 2 || l > 100 {
			return nil, apperr.Validation("nama harus 2–100 karakter").With("field", "name")
		}
		arg.Name = &n
	}
	if p.Locale != nil {
		if *p.Locale != "id" && *p.Locale != "en" {
			return nil, apperr.Validation("locale harus id atau en").With("field", "locale")
		}
		arg.Locale = p.Locale
	}
	clear := p.AvatarAssetID != nil && *p.AvatarAssetID == ""
	if p.AvatarAssetID != nil && !clear {
		if err := a.s.Media.Owned(ctx, userID, *p.AvatarAssetID, "avatar"); err != nil {
			return nil, err
		}
		arg.AvatarKey = p.AvatarAssetID
	}
	u, err := a.s.Q.UpdateUserProfile(ctx, arg)
	if err != nil {
		return nil, err
	}
	if clear {
		if u, err = a.s.Q.ClearUserAvatar(ctx, userID); err != nil {
			return nil, err
		}
	}
	v := userView(u)
	return &v, nil
}

// SetAvatar dipanggil MediaService setelah unggahan tervalidasi.
func (a *AuthService) SetAvatar(ctx context.Context, userID, key string) error {
	_, err := a.s.Q.UpdateUserProfile(ctx, sqlcgen.UpdateUserProfileParams{ID: userID, AvatarKey: &key})
	return err
}

// ---------- Verifikasi email ----------

func (a *AuthService) link(path string, token string) string {
	return a.s.Cfg.PublicWebURL + path + "?token=" + token
}

func (a *AuthService) sendVerifyEmail(ctx context.Context, u sqlcgen.User) {
	raw := auth.RandomToken(32)
	err := a.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		if err := q.InvalidateEmailTokens(ctx, sqlcgen.InvalidateEmailTokensParams{UserID: u.ID, Kind: "verify_email"}); err != nil {
			return err
		}
		return q.CreateEmailToken(ctx, sqlcgen.CreateEmailTokenParams{
			ID: id.New(), UserID: u.ID, Kind: "verify_email", TokenHash: auth.HashToken(raw), ExpiresAt: a.s.now().Add(a.s.Cfg.EmailTokenTTL),
		})
	})
	if err != nil {
		a.s.Log.Error("buat token verifikasi email gagal", "error", err)
		return
	}
	a.s.Notifier.Send(ctx, NotifyReq{UserID: u.ID, To: u.Email, Locale: u.Locale, Template: "verify_email",
		Data: map[string]any{"name": u.Name, "link": a.link("/auth/verify-email", raw), "hours": int(a.s.Cfg.EmailTokenTTL.Hours())}})
}

func (a *AuthService) ResendVerifyEmail(ctx context.Context, userID string) error {
	u, err := a.s.Q.GetUserByID(ctx, userID)
	if err != nil {
		return err
	}
	if u.EmailVerifiedAt != nil {
		return nil
	}
	if ok, _, err := a.s.Limiter.Allow(ctx, "verifymail:"+userID, 3, time.Hour); err == nil && !ok {
		return apperr.TooMany("terlalu sering meminta email verifikasi")
	}
	a.sendVerifyEmail(ctx, u)
	return nil
}

func (a *AuthService) VerifyEmail(ctx context.Context, raw string) error {
	return a.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		t, err := a.useToken(ctx, q, raw, "verify_email")
		if err != nil {
			return err
		}
		return q.SetUserEmailVerified(ctx, t.UserID)
	})
}

// useToken memvalidasi dan menandai token sekali-pakai.
func (a *AuthService) useToken(ctx context.Context, q *sqlcgen.Queries, raw, kind string) (sqlcgen.EmailToken, error) {
	t, err := q.GetEmailTokenByHashForUpdate(ctx, sqlcgen.GetEmailTokenByHashForUpdateParams{TokenHash: auth.HashToken(raw), Kind: kind})
	if err != nil {
		if platform.IsNoRows(err) {
			return t, apperr.BadRequest(apperr.CodeTokenInvalid, "tautan tidak valid")
		}
		return t, err
	}
	if t.UsedAt != nil {
		return t, apperr.BadRequest(apperr.CodeTokenInvalid, "tautan sudah dipakai")
	}
	if t.ExpiresAt.Before(a.s.now()) {
		return t, apperr.BadRequest(apperr.CodeTokenExpired, "tautan kedaluwarsa")
	}
	if err := q.UseEmailToken(ctx, t.ID); err != nil {
		return t, err
	}
	return t, nil
}

// ---------- Lupa & reset sandi ----------

// ForgotPassword SELALU berhasil dari sudut pandang pemanggil (tidak membocorkan keberadaan akun).
func (a *AuthService) ForgotPassword(ctx context.Context, emailRaw string) {
	email, ok := NormalizeEmail(emailRaw)
	if !ok {
		return
	}
	m := reqmeta.From(ctx)
	if ok, _, err := a.s.Limiter.Allow(ctx, "forgot:email:"+email, 3, time.Hour); err == nil && !ok {
		return
	}
	if ok, _, err := a.s.Limiter.Allow(ctx, "forgot:ip:"+m.IP, 10, time.Hour); err == nil && !ok {
		return
	}
	u, err := a.s.Q.GetUserByEmail(ctx, email)
	if err != nil || u.Status != "active" {
		return
	}
	raw := auth.RandomToken(32)
	err = a.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		if err := q.InvalidateEmailTokens(ctx, sqlcgen.InvalidateEmailTokensParams{UserID: u.ID, Kind: "reset_password"}); err != nil {
			return err
		}
		return q.CreateEmailToken(ctx, sqlcgen.CreateEmailTokenParams{
			ID: id.New(), UserID: u.ID, Kind: "reset_password", TokenHash: auth.HashToken(raw), ExpiresAt: a.s.now().Add(a.s.Cfg.ResetTokenTTL),
		})
	})
	if err != nil {
		a.s.Log.Error("buat token reset gagal", "error", err)
		return
	}
	a.s.Notifier.Send(ctx, NotifyReq{UserID: u.ID, To: u.Email, Locale: u.Locale, Template: "reset_password",
		Data: map[string]any{"name": u.Name, "link": a.link("/auth/reset-password", raw), "minutes": int(a.s.Cfg.ResetTokenTTL.Minutes())}})
}

// checkNotReused: kata sandi baru tidak boleh sama dengan sandi saat ini atau 3 terakhir (M27 AC4).
func (a *AuthService) checkNotReused(ctx context.Context, q *sqlcgen.Queries, u sqlcgen.User, newPw string) error {
	if a.s.Hasher.Verify(newPw, u.PasswordHash) {
		return apperr.Validation("kata sandi baru tidak boleh sama dengan yang lama").With("field", "password")
	}
	hist, err := q.RecentPasswordHashes(ctx, sqlcgen.RecentPasswordHashesParams{UserID: u.ID, Limit: passwordHistoryN})
	if err != nil {
		return err
	}
	for _, h := range hist {
		if a.s.Hasher.Verify(newPw, h) {
			return apperr.Validation("kata sandi baru tidak boleh sama dengan 3 kata sandi terakhir").With("field", "password")
		}
	}
	return nil
}

func (a *AuthService) setPassword(ctx context.Context, q *sqlcgen.Queries, u sqlcgen.User, newPw string) error {
	hash := a.s.Hasher.Hash(newPw)
	if err := q.UpdateUserPassword(ctx, sqlcgen.UpdateUserPasswordParams{ID: u.ID, PasswordHash: hash}); err != nil {
		return err
	}
	return q.AddPasswordHistory(ctx, sqlcgen.AddPasswordHistoryParams{UserID: u.ID, PasswordHash: hash})
}

func (a *AuthService) ResetPassword(ctx context.Context, raw, newPw string, sf SecondFactor) error {
	if err := auth.ValidatePasswordPolicy(newPw); err != nil {
		return apperr.Validation(err.Error()).With("field", "password")
	}
	// Faktor kedua diperiksa SEBELUM token dihabiskan: kode salah tidak membakar tautan, tetapi tanpa kode yang benar
	// kata sandi tidak berubah walau tautan email jatuh ke tangan orang lain.
	pt, err := a.peekToken(ctx, raw, "reset_password")
	if err != nil {
		return err
	}
	pu, err := a.s.Q.GetUserByID(ctx, pt.UserID)
	if err != nil {
		return err
	}
	if err := a.verifyResetFactor(ctx, pu, sf.Method, sf.Code); err != nil {
		return err
	}
	var u sqlcgen.User
	err = a.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		t, err := a.useToken(ctx, q, raw, "reset_password")
		if err != nil {
			return err
		}
		u, err = q.GetUserByIDForUpdate(ctx, t.UserID)
		if err != nil {
			return err
		}
		if err := a.checkNotReused(ctx, q, u, newPw); err != nil {
			return err
		}
		if err := a.setPassword(ctx, q, u, newPw); err != nil {
			return err
		}
		// Menerima email reset = bukti kepemilikan alamat.
		return q.SetUserEmailVerified(ctx, u.ID)
	})
	if err != nil {
		return err
	}
	a.RevokeUserAccess(ctx, u.ID) // semua sesi lama dicabut
	a.s.Notifier.Send(ctx, NotifyReq{UserID: u.ID, Template: "password_changed", Data: map[string]any{"name": u.Name}})
	return nil
}

func (a *AuthService) ChangePassword(ctx context.Context, p *auth.Principal, current, newPw string, sf SecondFactor) error {
	if err := auth.ValidatePasswordPolicy(newPw); err != nil {
		return apperr.Validation(err.Error()).With("field", "password")
	}
	var u sqlcgen.User
	err := a.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		u, err = q.GetUserByIDForUpdate(ctx, p.UserID)
		if err != nil {
			return err
		}
		if !a.s.Hasher.Verify(current, u.PasswordHash) {
			return apperr.New(401, apperr.CodeInvalidCredential, "kata sandi saat ini salah")
		}
		if err := a.requireStepUp(ctx, q, u, sf.Method, sf.Code); err != nil {
			return err
		}
		if err := a.checkNotReused(ctx, q, u, newPw); err != nil {
			return err
		}
		return a.setPassword(ctx, q, u, newPw)
	})
	if err != nil {
		return err
	}
	fams, err := a.s.Q.RevokeOtherUserSessions(ctx, sqlcgen.RevokeOtherUserSessionsParams{UserID: u.ID, FamilyID: p.SessionID})
	if err == nil {
		a.revokeFamilies(ctx, fams...)
	}
	a.s.Notifier.Send(ctx, NotifyReq{UserID: u.ID, Template: "password_changed", Data: map[string]any{"name": u.Name}})
	return nil
}

// SecondFactor = faktor kedua untuk aksi sensitif (kosong bila akun belum memakainya).
type SecondFactor struct{ Method, Code string }

// ---------- Ganti email ----------

func (a *AuthService) RequestEmailChange(ctx context.Context, userID, newEmailRaw, password string, sf SecondFactor) error {
	newEmail, ok := NormalizeEmail(newEmailRaw)
	if !ok {
		return apperr.Validation("email tidak valid").With("field", "email")
	}
	u, err := a.s.Q.GetUserByID(ctx, userID)
	if err != nil {
		return err
	}
	if !a.s.Hasher.Verify(password, u.PasswordHash) {
		return apperr.New(401, apperr.CodeInvalidCredential, "kata sandi salah")
	}
	if err := a.requireStepUp(ctx, a.s.Q, u, sf.Method, sf.Code); err != nil {
		return err
	}
	if newEmail == u.Email {
		return apperr.Validation("email baru sama dengan email saat ini").With("field", "email")
	}
	if _, err := a.s.Q.GetUserByEmail(ctx, newEmail); err == nil {
		return apperr.Conflict(apperr.CodeEmailTaken, "email sudah terdaftar")
	}
	if ok, _, err := a.s.Limiter.Allow(ctx, "emailchange:"+userID, 3, time.Hour); err == nil && !ok {
		return apperr.TooMany("terlalu sering meminta perubahan email")
	}
	raw := auth.RandomToken(32)
	err = a.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		if err := q.InvalidateEmailTokens(ctx, sqlcgen.InvalidateEmailTokensParams{UserID: userID, Kind: "change_email"}); err != nil {
			return err
		}
		return q.CreateEmailToken(ctx, sqlcgen.CreateEmailTokenParams{
			ID: id.New(), UserID: userID, Kind: "change_email", TokenHash: auth.HashToken(raw),
			NewValue: &newEmail, ExpiresAt: a.s.now().Add(a.s.Cfg.EmailTokenTTL),
		})
	})
	if err != nil {
		return err
	}
	a.s.Notifier.Send(ctx, NotifyReq{UserID: userID, To: newEmail, Locale: u.Locale, Template: "email_change_confirm",
		Data: map[string]any{"name": u.Name, "link": a.link("/auth/confirm-email-change", raw)}})
	return nil
}

func (a *AuthService) ConfirmEmailChange(ctx context.Context, raw string) error {
	var oldEmail, newEmail, name, locale, userID string
	err := a.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		t, err := a.useToken(ctx, q, raw, "change_email")
		if err != nil {
			return err
		}
		u, err := q.GetUserByIDForUpdate(ctx, t.UserID)
		if err != nil {
			return err
		}
		newEmail = deref(t.NewValue)
		if err := q.UpdateUserEmail(ctx, sqlcgen.UpdateUserEmailParams{ID: u.ID, Email: newEmail}); err != nil {
			if platform.IsUniqueViolation(err) {
				return apperr.Conflict(apperr.CodeEmailTaken, "email sudah dipakai akun lain")
			}
			return err
		}
		oldEmail, name, locale, userID = u.Email, u.Name, u.Locale, u.ID
		return nil
	})
	if err != nil {
		return err
	}
	a.s.Notifier.Send(ctx, NotifyReq{UserID: userID, To: oldEmail, Locale: locale, Template: "email_change_notice",
		Data: map[string]any{"name": name, "new_email": MaskEmail(newEmail)}})
	return nil
}

// ---------- OTP nomor HP ----------

func (a *AuthService) otpKey(userID string) string  { return "otp:" + userID }
func (a *AuthService) otpLock(userID string) string { return "otp:lock:" + userID }

// SendPhoneOTP mengirim OTP ke nomor. Bila akun sudah punya nomor terverifikasi (ganti nomor), sandi wajib.
func (a *AuthService) SendPhoneOTP(ctx context.Context, userID, phoneRaw, password string) error {
	phone, ok := NormalizePhone(phoneRaw)
	if !ok {
		return apperr.Validation("nomor HP tidak valid").With("field", "phone")
	}
	u, err := a.s.Q.GetUserByID(ctx, userID)
	if err != nil {
		return err
	}
	if u.PhoneVerifiedAt != nil && deref(u.Phone) != phone {
		if !a.s.Hasher.Verify(password, u.PasswordHash) {
			return apperr.New(401, apperr.CodeInvalidCredential, "kata sandi diperlukan untuk mengganti nomor")
		}
	}
	if a.s.Fraud.IsBlacklisted(ctx, "phone", phone) {
		return apperr.ForbiddenCode(apperr.CodeBlocked, "nomor tidak dapat digunakan")
	}
	if other, err := a.s.Q.GetUserByPhone(ctx, &phone); err == nil && other.ID != userID {
		return apperr.Conflict(apperr.CodePhoneTaken, "nomor HP sudah dipakai akun lain")
	}
	if n, _ := a.s.Redis.Exists(ctx, a.otpLock(userID)).Result(); n > 0 {
		return apperr.New(429, apperr.CodeOTPLocked, "terlalu banyak percobaan; coba lagi nanti")
	}
	if ok, ra, err := a.s.Limiter.Allow(ctx, "otp:user:"+userID, 3, 10*time.Minute); err == nil && !ok {
		return apperr.TooMany("terlalu sering meminta OTP").With("retry_after", int(ra.Seconds())+1)
	}
	if ok, _, err := a.s.Limiter.Allow(ctx, "otp:phone:"+phone, 5, time.Hour); err == nil && !ok {
		return apperr.TooMany("terlalu sering meminta OTP untuk nomor ini")
	}
	code := auth.RandomDigits(6)
	key := a.otpKey(userID)
	pipe := a.s.Redis.TxPipeline()
	pipe.HSet(ctx, key, map[string]any{"h": auth.HashToken(code + ":" + userID), "phone": phone, "tries": 0})
	pipe.Expire(ctx, key, a.s.Cfg.OTPTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}
	if err := a.s.SMS.Send(ctx, phone, notify.OTPSMS(u.Locale, code, int(a.s.Cfg.OTPTTL.Minutes()))); err != nil {
		a.s.Redis.Del(ctx, key)
		a.s.Log.Error("kirim SMS gagal", "error", err)
		return apperr.New(503, "SMS_UNAVAILABLE", "gagal mengirim SMS, coba lagi sebentar")
	}
	return nil
}

func (a *AuthService) VerifyPhoneOTP(ctx context.Context, userID, code string) error {
	if n, _ := a.s.Redis.Exists(ctx, a.otpLock(userID)).Result(); n > 0 {
		return apperr.New(429, apperr.CodeOTPLocked, "terlalu banyak percobaan; coba lagi nanti")
	}
	key := a.otpKey(userID)
	vals, err := a.s.Redis.HGetAll(ctx, key).Result()
	if err != nil {
		return err
	}
	if len(vals) == 0 {
		return apperr.BadRequest(apperr.CodeOTPInvalid, "kode kedaluwarsa atau belum diminta")
	}
	if !auth.ConstantTimeEqual(vals["h"], auth.HashToken(strings.TrimSpace(code)+":"+userID)) {
		tries, _ := a.s.Redis.HIncrBy(ctx, key, "tries", 1).Result()
		if int(tries) >= a.s.Cfg.OTPMaxTries {
			pipe := a.s.Redis.TxPipeline()
			pipe.Del(ctx, key)
			pipe.Set(ctx, a.otpLock(userID), "1", lockDuration)
			_, _ = pipe.Exec(ctx)
			return apperr.New(429, apperr.CodeOTPLocked, "terlalu banyak percobaan; dikunci 15 menit")
		}
		return apperr.BadRequest(apperr.CodeOTPInvalid, "kode salah").With("remaining_tries", a.s.Cfg.OTPMaxTries-int(tries))
	}
	phone := vals["phone"]
	err = a.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		if err := q.SetUserPhoneVerified(ctx, sqlcgen.SetUserPhoneVerifiedParams{ID: userID, Phone: &phone}); err != nil {
			if platform.IsUniqueViolation(err) {
				return apperr.Conflict(apperr.CodePhoneTaken, "nomor HP sudah dipakai akun lain")
			}
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	a.s.Redis.Del(ctx, key)
	return nil
}

// RequireVerified memastikan email & HP terverifikasi sebelum membeli (docs/06 M27 AC1, M5-F1).
func (a *AuthService) RequireVerified(ctx context.Context, q *sqlcgen.Queries, userID string) (sqlcgen.User, error) {
	u, err := q.GetUserByID(ctx, userID)
	if err != nil {
		return u, err
	}
	if u.Status != "active" {
		return u, apperr.ForbiddenCode(apperr.CodeBlocked, "akun tidak aktif")
	}
	if u.EmailVerifiedAt == nil {
		return u, apperr.ForbiddenCode(apperr.CodeEmailNotVerified, "verifikasi email diperlukan")
	}
	if u.PhoneVerifiedAt == nil {
		return u, apperr.ForbiddenCode(apperr.CodeOTPInvalid, "verifikasi nomor HP diperlukan").With("phone_required", true)
	}
	return u, nil
}

// ---------- Perangkat ----------

type DeviceView struct {
	SessionID  string     `json:"session_id"`
	Label      string     `json:"label"`
	IP         string     `json:"ip,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	Current    bool       `json:"current"`
}

func (a *AuthService) Devices(ctx context.Context, p *auth.Principal) ([]DeviceView, error) {
	rows, err := a.s.Q.ListUserSessions(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	out := make([]DeviceView, 0, len(rows))
	for _, r := range rows {
		d := DeviceView{SessionID: r.FamilyID, Label: deref(r.DeviceLabel), CreatedAt: r.CreatedAt, LastSeenAt: r.LastSeenAt, Current: r.FamilyID == p.SessionID}
		if r.Ip != nil {
			d.IP = r.Ip.String()
		}
		out = append(out, d)
	}
	return out, nil
}

func (a *AuthService) RevokeDevice(ctx context.Context, p *auth.Principal, family string) error {
	owner, err := a.s.Q.GetSessionOwner(ctx, family)
	if err != nil || owner != p.UserID {
		return apperr.NotFound("perangkat")
	}
	if err := a.s.Q.RevokeSessionFamily(ctx, family); err != nil {
		return err
	}
	a.revokeFamilies(ctx, family)
	return nil
}

// ---------- MFA (TOTP) ----------

type MFASetup struct {
	Secret          string `json:"secret"`
	ProvisioningURI string `json:"provisioning_uri"`
}

func (a *AuthService) SetupMFA(ctx context.Context, userID string) (*MFASetup, error) {
	u, err := a.s.Q.GetUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	// Hanya TOTP yang sudah terkonfirmasi yang menghalangi; faktor email/SMS tidak (boleh ditambah aplikasi autentikator).
	if row, err := a.s.Q.GetMFA(ctx, userID); err == nil && row.ConfirmedAt != nil {
		return nil, apperr.Conflict(apperr.CodeConflict, "aplikasi autentikator sudah aktif")
	}
	secret := auth.NewTOTPSecret()
	enc, err := a.s.Box.Encrypt([]byte(secret))
	if err != nil {
		return nil, err
	}
	if err := a.s.Q.UpsertMFA(ctx, sqlcgen.UpsertMFAParams{UserID: userID, SecretEnc: enc}); err != nil {
		return nil, err
	}
	return &MFASetup{Secret: secret, ProvisioningURI: auth.TOTPProvisioningURI("TixGo", u.Email, secret)}, nil
}

func (a *AuthService) mfaSecret(ctx context.Context, q *sqlcgen.Queries, userID string) (string, *sqlcgen.MfaTotp, error) {
	row, err := q.GetMFA(ctx, userID)
	if err != nil {
		return "", nil, err
	}
	b, err := a.s.Box.Decrypt(row.SecretEnc)
	if err != nil {
		return "", nil, err
	}
	return string(b), &row, nil
}

// verifyMFA menerima kode TOTP (dengan perlindungan replay) atau kode pemulihan.
func (a *AuthService) verifyMFA(ctx context.Context, q *sqlcgen.Queries, userID, code string) (bool, error) {
	code = strings.TrimSpace(code)
	secret, row, err := a.mfaSecret(ctx, q, userID)
	if err != nil && !platform.IsNoRows(err) {
		return false, err
	}
	// Tanpa baris TOTP (akun hanya memakai OTP email/SMS) kode pemulihan tetap berlaku.
	if err == nil && row.ConfirmedAt != nil && len(code) == 6 && auth.TOTPValidate(code, secret, a.s.now(), 30, 1) {
		step := a.s.now().Unix() / 30
		okSet, err := a.s.Redis.SetNX(ctx, fmt.Sprintf("mfa:used:%s:%s", userID, code), step, 2*time.Minute).Result()
		if err != nil {
			return false, err
		}
		return okSet, nil // kode yang sama tidak dapat dipakai ulang dalam jendelanya
	}
	norm := strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(code))
	if len(norm) >= 8 {
		if _, err := q.UseRecoveryCode(ctx, sqlcgen.UseRecoveryCodeParams{UserID: userID, CodeHash: auth.HashToken(norm)}); err == nil {
			return true, nil
		} else if !platform.IsNoRows(err) {
			return false, err
		}
	}
	return false, nil
}

func newRecoveryCodes() (plain []string, hashes []string) {
	for i := 0; i < recoveryCodeCount; i++ {
		raw := auth.RandomCode(10)
		plain = append(plain, raw[:5]+"-"+raw[5:])
		hashes = append(hashes, auth.HashToken(raw))
	}
	return
}

func (a *AuthService) storeRecoveryCodes(ctx context.Context, q *sqlcgen.Queries, userID string) ([]string, error) {
	if err := q.DeleteRecoveryCodes(ctx, userID); err != nil {
		return nil, err
	}
	plain, hashes := newRecoveryCodes()
	for _, h := range hashes {
		if err := q.InsertRecoveryCode(ctx, sqlcgen.InsertRecoveryCodeParams{ID: id.New(), UserID: userID, CodeHash: h}); err != nil {
			return nil, err
		}
	}
	return plain, nil
}

// ConfirmMFA mengaktifkan MFA; mengembalikan kode pemulihan (hanya sekali ditampilkan) dan sesi baru ber-klaim mfa.
func (a *AuthService) ConfirmMFA(ctx context.Context, p *auth.Principal, code string) ([]string, string, time.Time, error) {
	secret, row, err := a.mfaSecret(ctx, a.s.Q, p.UserID)
	if err != nil {
		if platform.IsNoRows(err) {
			return nil, "", time.Time{}, apperr.BadRequest(apperr.CodeInvalidState, "mulai pengaturan MFA dahulu")
		}
		return nil, "", time.Time{}, err
	}
	if row.ConfirmedAt != nil {
		return nil, "", time.Time{}, apperr.Conflict(apperr.CodeConflict, "MFA sudah aktif")
	}
	if !auth.TOTPValidate(strings.TrimSpace(code), secret, a.s.now(), 30, 1) {
		return nil, "", time.Time{}, apperr.BadRequest(apperr.CodeOTPInvalid, "kode salah")
	}
	var codes []string
	err = a.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		if err := q.ConfirmMFA(ctx, p.UserID); err != nil {
			return err
		}
		if err := q.SetUserMFA(ctx, sqlcgen.SetUserMFAParams{ID: p.UserID, MfaEnabled: true}); err != nil {
			return err
		}
		var err error
		codes, err = a.storeRecoveryCodes(ctx, q, p.UserID)
		return err
	})
	if err != nil {
		return nil, "", time.Time{}, err
	}
	tok, exp, err := a.s.Tokens.IssueAccess(p.UserID, p.Role, p.SessionID, true)
	return codes, tok, exp, err
}

// DisableMFA mematikan TOTP (kompatibel dengan endpoint lama); faktor lain tetap berlaku bila ada.
func (a *AuthService) DisableMFA(ctx context.Context, userID, password, code string) error {
	return a.DisableMethod(ctx, userID, MFAMethodTOTP, password, "", code)
}

func (a *AuthService) RegenerateRecoveryCodes(ctx context.Context, userID, password string) ([]string, error) {
	u, err := a.s.Q.GetUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !a.s.Hasher.Verify(password, u.PasswordHash) {
		return nil, apperr.New(401, apperr.CodeInvalidCredential, "kata sandi salah")
	}
	if !u.MfaEnabled {
		return nil, apperr.BadRequest(apperr.CodeInvalidState, "MFA tidak aktif")
	}
	var codes []string
	err = a.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		codes, err = a.storeRecoveryCodes(ctx, q, userID)
		return err
	})
	return codes, err
}

// MFARequiredRole: peran yang wajib MFA untuk aksi sensitif (docs/20 M27 AC6).
func MFARequiredRole(role string) bool {
	return role == "admin" || role == "finance"
}

var _ = errors.New
var _ redis.Cmdable
