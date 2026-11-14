package service

import (
	"context"
	"strings"
	"time"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/notify"
	"tixgo/backend/internal/platform"
	"tixgo/backend/internal/repository/sqlcgen"
)

// ---------- Verifikasi berlapis: TOTP, OTP email, OTP SMS, kode pemulihan ----------
//
// Aturan yang dijaga di sini:
//   - Faktor kedua adalah sifat AKUN: aktif sekali, lalu diminta di login dari perangkat/peramban mana pun.
//   - OTP disimpan hanya sebagai hash di Redis (sekali pakai, kedaluwarsa, dibatasi percobaan dan permintaan).
//   - OTP email dikirim langsung (tidak lewat tabel notifikasi) supaya kode tidak tersimpan polos di basis data
//     atau muncul di kotak masuk pengguna.
//   - Reset sandi dan ganti sandi meminta faktor kedua bila akun memilikinya (reset: SMS bila ada HP terverifikasi).

const (
	MFAMethodTOTP  = "totp"
	MFAMethodEmail = "email"
	MFAMethodSMS   = "sms"

	otpPurposeLogin       = "login"
	otpPurposeEnrollEmail = "enroll_email"
	otpPurposeEnrollSMS   = "enroll_sms"
	otpPurposeReset       = "reset"
	otpPurposeStepUp      = "stepup"
)

func mfaOTPKey(userID, purpose string) string { return "mfa:otp:" + userID + ":" + purpose }
func mfaOTPLock(userID string) string         { return "mfa:otp:lock:" + userID }

func maskEmail(e string) string {
	at := strings.LastIndex(e, "@")
	if at < 1 {
		return "***"
	}
	local, domain := e[:at], e[at:]
	return local[:1] + strings.Repeat("*", 3) + domain
}

func maskPhone(p string) string {
	if len(p) < 6 {
		return "***"
	}
	return p[:3] + strings.Repeat("*", len(p)-5) + p[len(p)-2:]
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// activeMethods = faktor yang benar-benar bisa dipakai sekarang (OTP SMS hanya bila HP masih terverifikasi).
func (a *AuthService) activeMethods(ctx context.Context, q *sqlcgen.Queries, u sqlcgen.User) []string {
	var m []string
	if row, err := q.GetMFA(ctx, u.ID); err == nil && row.ConfirmedAt != nil {
		m = append(m, MFAMethodTOTP)
	}
	if u.MfaEmail && u.EmailVerifiedAt != nil {
		m = append(m, MFAMethodEmail)
	}
	if u.MfaSms && u.PhoneVerifiedAt != nil && u.Phone != nil {
		m = append(m, MFAMethodSMS)
	}
	return m
}

// syncMFAEnabled menyamakan `users.mfa_enabled` dengan kenyataan (minimal satu faktor aktif).
func (a *AuthService) syncMFAEnabled(ctx context.Context, q *sqlcgen.Queries, userID string) (bool, error) {
	u, err := q.GetUserByID(ctx, userID)
	if err != nil {
		return false, err
	}
	on := u.MfaEmail || u.MfaSms
	if row, err := q.GetMFA(ctx, userID); err == nil && row.ConfirmedAt != nil {
		on = true
	}
	if on != u.MfaEnabled {
		if err := q.SetUserMFA(ctx, sqlcgen.SetUserMFAParams{ID: userID, MfaEnabled: on}); err != nil {
			return on, err
		}
	}
	return on, nil
}

// mfaRequired membentuk galat 401 yang memberi tahu klien faktor mana yang bisa dipakai dan ke mana kode dikirim.
func (a *AuthService) mfaRequired(ctx context.Context, u sqlcgen.User, msg string) error {
	methods := a.activeMethods(ctx, a.s.Q, u)
	e := apperr.New(401, apperr.CodeMFARequired, msg).With("mfa", true).With("methods", methods)
	if contains(methods, MFAMethodEmail) {
		e = e.With("email_masked", maskEmail(u.Email))
	}
	if contains(methods, MFAMethodSMS) {
		e = e.With("phone_masked", maskPhone(deref(u.Phone)))
	}
	return e
}

// sendOTP membuat dan mengirim OTP 6 digit ke email atau HP terverifikasi; mengembalikan tujuan tersamar.
func (a *AuthService) sendOTP(ctx context.Context, u sqlcgen.User, purpose, method string) (string, error) {
	var dest string
	switch method {
	case MFAMethodEmail:
		if u.EmailVerifiedAt == nil {
			return "", apperr.Conflict(apperr.CodeInvalidState, "email belum terverifikasi")
		}
		dest = u.Email
	case MFAMethodSMS:
		if u.Phone == nil || u.PhoneVerifiedAt == nil {
			return "", apperr.Conflict(apperr.CodeInvalidState, "nomor HP belum terverifikasi")
		}
		dest = *u.Phone
	default:
		return "", apperr.Validation("metode harus email atau sms").With("field", "method")
	}
	if n, _ := a.s.Redis.Exists(ctx, mfaOTPLock(u.ID)).Result(); n > 0 {
		return "", apperr.New(429, apperr.CodeOTPLocked, "terlalu banyak percobaan; coba lagi nanti")
	}
	if ok, ra, err := a.s.Limiter.Allow(ctx, "mfaotp:user:"+u.ID, 5, 10*time.Minute); err == nil && !ok {
		return "", apperr.TooMany("terlalu sering meminta kode").With("retry_after", int(ra.Seconds())+1)
	}
	if ok, _, err := a.s.Limiter.Allow(ctx, "mfaotp:dest:"+dest, 10, time.Hour); err == nil && !ok {
		return "", apperr.TooMany("terlalu sering meminta kode ke tujuan ini")
	}
	code := auth.RandomDigits(6)
	key := mfaOTPKey(u.ID, purpose)
	pipe := a.s.Redis.TxPipeline()
	pipe.HSet(ctx, key, map[string]any{"h": auth.HashToken(code + ":" + u.ID + ":" + purpose + ":" + method), "tries": 0})
	pipe.Expire(ctx, key, a.s.Cfg.OTPTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return "", err
	}
	minutes := int(a.s.Cfg.OTPTTL.Minutes())
	var sendErr error
	if method == MFAMethodSMS {
		sendErr = a.s.SMS.Send(ctx, dest, notify.OTPSMS(u.Locale, code, minutes))
	} else {
		subject, text, html, err := notify.Render("otp_code", u.Locale, map[string]any{"name": u.Name, "code": code, "minutes": minutes})
		if err != nil {
			sendErr = err
		} else {
			sendErr = a.s.Mailer.Send(ctx, notify.Mail{To: dest, Subject: subject, Text: text, HTML: html})
		}
	}
	if sendErr != nil {
		a.s.Redis.Del(ctx, key)
		a.s.Log.Error("kirim OTP gagal", "method", method, "error", sendErr)
		return "", apperr.New(503, "OTP_UNAVAILABLE", "gagal mengirim kode, coba lagi sebentar")
	}
	if method == MFAMethodSMS {
		return maskPhone(dest), nil
	}
	return maskEmail(dest), nil
}

// checkOTP: sekali pakai; terlalu banyak salah = kunci 15 menit.
func (a *AuthService) checkOTP(ctx context.Context, userID, purpose, method, code string) (bool, error) {
	if n, _ := a.s.Redis.Exists(ctx, mfaOTPLock(userID)).Result(); n > 0 {
		return false, apperr.New(429, apperr.CodeOTPLocked, "terlalu banyak percobaan; coba lagi nanti")
	}
	key := mfaOTPKey(userID, purpose)
	vals, err := a.s.Redis.HGetAll(ctx, key).Result()
	if err != nil {
		return false, err
	}
	if len(vals) == 0 {
		return false, nil
	}
	if !auth.ConstantTimeEqual(vals["h"], auth.HashToken(strings.TrimSpace(code)+":"+userID+":"+purpose+":"+method)) {
		tries, _ := a.s.Redis.HIncrBy(ctx, key, "tries", 1).Result()
		if int(tries) >= a.s.Cfg.OTPMaxTries {
			pipe := a.s.Redis.TxPipeline()
			pipe.Del(ctx, key)
			pipe.Set(ctx, mfaOTPLock(userID), "1", lockDuration)
			_, _ = pipe.Exec(ctx)
			return false, apperr.New(429, apperr.CodeOTPLocked, "terlalu banyak percobaan; dikunci 15 menit")
		}
		return false, nil
	}
	a.s.Redis.Del(ctx, key)
	return true, nil
}

// verifySecondFactor memeriksa kode untuk faktor yang aktif pada akun. method kosong = TOTP atau kode pemulihan.
func (a *AuthService) verifySecondFactor(ctx context.Context, q *sqlcgen.Queries, u sqlcgen.User, purpose, method, code string) (bool, error) {
	method = strings.ToLower(strings.TrimSpace(method))
	if method == "" || method == MFAMethodTOTP {
		return a.verifyMFA(ctx, q, u.ID, code)
	}
	if !contains(a.activeMethods(ctx, q, u), method) {
		return false, nil
	}
	return a.checkOTP(ctx, u.ID, purpose, method, code)
}

// ---------- Login: kirim OTP setelah sandi benar ----------

// SendLoginOTP: langkah antara sandi dan kode. Memakai pemeriksaan sandi yang sama dengan Login (laju, kunci akun).
func (a *AuthService) SendLoginOTP(ctx context.Context, r LoginReq, method string) (string, error) {
	u, _, err := a.checkPassword(ctx, r)
	if err != nil {
		return "", err
	}
	if !u.MfaEnabled || !contains(a.activeMethods(ctx, a.s.Q, u), strings.ToLower(method)) {
		return "", apperr.BadRequest(apperr.CodeInvalidState, "metode ini tidak aktif untuk akunmu")
	}
	return a.sendOTP(ctx, u, otpPurposeLogin, strings.ToLower(method))
}

// ---------- Pengaturan faktor (akun masuk) ----------

type MFAStatus struct {
	Enabled       bool     `json:"enabled"`
	Methods       []string `json:"methods"`
	EmailMasked   string   `json:"email_masked"`
	EmailVerified bool     `json:"email_verified"`
	PhoneMasked   string   `json:"phone_masked,omitempty"`
	PhoneVerified bool     `json:"phone_verified"`
	RecoveryLeft  int      `json:"recovery_left"`
}

func (a *AuthService) MFAStatus(ctx context.Context, userID string) (*MFAStatus, error) {
	u, err := a.s.Q.GetUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	st := &MFAStatus{Enabled: u.MfaEnabled, Methods: a.activeMethods(ctx, a.s.Q, u), EmailMasked: maskEmail(u.Email),
		EmailVerified: u.EmailVerifiedAt != nil, PhoneVerified: u.PhoneVerifiedAt != nil}
	if st.Methods == nil {
		st.Methods = []string{}
	}
	if u.Phone != nil {
		st.PhoneMasked = maskPhone(*u.Phone)
	}
	if n, err := a.s.Q.CountRecoveryCodes(ctx, userID); err == nil {
		st.RecoveryLeft = int(n)
	}
	return st, nil
}

func enrollPurpose(method string) (string, bool) {
	switch method {
	case MFAMethodEmail:
		return otpPurposeEnrollEmail, true
	case MFAMethodSMS:
		return otpPurposeEnrollSMS, true
	}
	return "", false
}

// StartEnrollOTP mengirim kode untuk membuktikan tujuan itu benar milik pengguna sebelum dijadikan faktor kedua.
func (a *AuthService) StartEnrollOTP(ctx context.Context, userID, method string) (string, error) {
	purpose, ok := enrollPurpose(method)
	if !ok {
		return "", apperr.Validation("metode harus email atau sms").With("field", "method")
	}
	u, err := a.s.Q.GetUserByID(ctx, userID)
	if err != nil {
		return "", err
	}
	if contains(a.activeMethods(ctx, a.s.Q, u), method) {
		return "", apperr.Conflict(apperr.CodeConflict, "metode ini sudah aktif")
	}
	return a.sendOTP(ctx, u, purpose, method)
}

// ConfirmEnrollOTP mengaktifkan faktor. Faktor pertama sekaligus menerbitkan kode pemulihan (sekali tampil).
func (a *AuthService) ConfirmEnrollOTP(ctx context.Context, p *auth.Principal, method, code string) ([]string, string, time.Time, error) {
	purpose, ok := enrollPurpose(method)
	if !ok {
		return nil, "", time.Time{}, apperr.Validation("metode harus email atau sms").With("field", "method")
	}
	okOTP, err := a.checkOTP(ctx, p.UserID, purpose, method, code)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	if !okOTP {
		return nil, "", time.Time{}, apperr.BadRequest(apperr.CodeOTPInvalid, "kode salah atau kedaluwarsa")
	}
	var codes []string
	err = a.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		u, err := q.GetUserByID(ctx, p.UserID)
		if err != nil {
			return err
		}
		first := len(a.activeMethods(ctx, q, u)) == 0
		if method == MFAMethodEmail {
			err = q.SetUserMFAEmail(ctx, sqlcgen.SetUserMFAEmailParams{ID: p.UserID, MfaEmail: true})
		} else {
			err = q.SetUserMFASMS(ctx, sqlcgen.SetUserMFASMSParams{ID: p.UserID, MfaSms: true})
		}
		if err != nil {
			return err
		}
		if _, err := a.syncMFAEnabled(ctx, q, p.UserID); err != nil {
			return err
		}
		if first {
			codes, err = a.storeRecoveryCodes(ctx, q, p.UserID)
		}
		return err
	})
	if err != nil {
		return nil, "", time.Time{}, err
	}
	tok, exp, err := a.s.Tokens.IssueAccess(p.UserID, p.Role, p.SessionID, true)
	return codes, tok, exp, err
}

// SendStepUp mengirim kode ke faktor email/SMS yang aktif untuk aksi sensitif saat sudah masuk.
func (a *AuthService) SendStepUp(ctx context.Context, userID, method string) (string, error) {
	u, err := a.s.Q.GetUserByID(ctx, userID)
	if err != nil {
		return "", err
	}
	if !contains(a.activeMethods(ctx, a.s.Q, u), method) || method == MFAMethodTOTP {
		return "", apperr.BadRequest(apperr.CodeInvalidState, "metode ini tidak aktif untuk akunmu")
	}
	return a.sendOTP(ctx, u, otpPurposeStepUp, method)
}

// requireStepUp: aksi sensitif (ganti sandi, matikan faktor) wajib faktor kedua bila akun memilikinya.
func (a *AuthService) requireStepUp(ctx context.Context, q *sqlcgen.Queries, u sqlcgen.User, method, code string) error {
	if !u.MfaEnabled {
		return nil
	}
	if strings.TrimSpace(code) == "" {
		return a.mfaRequired(ctx, u, "kode verifikasi dua langkah diperlukan")
	}
	ok, err := a.verifySecondFactor(ctx, q, u, otpPurposeStepUp, method, code)
	if err != nil {
		return err
	}
	if !ok {
		return a.mfaRequired(ctx, u, "kode verifikasi salah")
	}
	return nil
}

// DisableMethod mematikan satu faktor. Butuh sandi dan kode dari faktor yang aktif (boleh faktor lain).
// Faktor terakhir yang dimatikan juga menghapus kode pemulihan.
func (a *AuthService) DisableMethod(ctx context.Context, userID, method, password, codeMethod, code string) error {
	if method != MFAMethodTOTP && method != MFAMethodEmail && method != MFAMethodSMS {
		return apperr.Validation("metode tidak dikenal").With("field", "method")
	}
	u, err := a.s.Q.GetUserByID(ctx, userID)
	if err != nil {
		return err
	}
	if !a.s.Hasher.Verify(password, u.PasswordHash) {
		return apperr.New(401, apperr.CodeInvalidCredential, "kata sandi salah")
	}
	if !u.MfaEnabled || !contains(a.activeMethods(ctx, a.s.Q, u), method) {
		return apperr.BadRequest(apperr.CodeInvalidState, "metode ini tidak aktif")
	}
	ok, err := a.verifySecondFactor(ctx, a.s.Q, u, otpPurposeStepUp, codeMethod, code)
	if err != nil {
		return err
	}
	if !ok {
		return apperr.BadRequest(apperr.CodeOTPInvalid, "kode salah")
	}
	return a.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		switch method {
		case MFAMethodTOTP:
			if err := q.DeleteMFA(ctx, userID); err != nil && !platform.IsNoRows(err) {
				return err
			}
		case MFAMethodEmail:
			if err := q.SetUserMFAEmail(ctx, sqlcgen.SetUserMFAEmailParams{ID: userID, MfaEmail: false}); err != nil {
				return err
			}
		case MFAMethodSMS:
			if err := q.SetUserMFASMS(ctx, sqlcgen.SetUserMFASMSParams{ID: userID, MfaSms: false}); err != nil {
				return err
			}
		}
		on, err := a.syncMFAEnabled(ctx, q, userID)
		if err != nil {
			return err
		}
		if !on {
			return q.DeleteRecoveryCodes(ctx, userID)
		}
		return nil
	})
}

// ---------- Reset sandi: faktor kedua ----------

// ResetRequirement: tidak ada | faktor kedua akun (mfa) | OTP ke HP terverifikasi (phone).
type ResetInfo struct {
	SecondFactor string   `json:"second_factor"` // none | mfa | phone
	Methods      []string `json:"methods"`
	EmailMasked  string   `json:"email_masked,omitempty"`
	PhoneMasked  string   `json:"phone_masked,omitempty"`
}

// peekToken memeriksa token tanpa menghabiskannya (kode OTP salah tidak boleh membakar tautan).
func (a *AuthService) peekToken(ctx context.Context, raw, kind string) (sqlcgen.EmailToken, error) {
	t, err := a.s.Q.GetEmailTokenByHashForUpdate(ctx, sqlcgen.GetEmailTokenByHashForUpdateParams{TokenHash: auth.HashToken(raw), Kind: kind})
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
	return t, nil
}

func (a *AuthService) resetInfoFor(ctx context.Context, q *sqlcgen.Queries, u sqlcgen.User) ResetInfo {
	info := ResetInfo{SecondFactor: "none", Methods: []string{}}
	if u.MfaEnabled {
		info.SecondFactor = "mfa"
		info.Methods = a.activeMethods(ctx, q, u)
	} else if u.PhoneVerifiedAt != nil && u.Phone != nil {
		info.SecondFactor = "phone"
		info.Methods = []string{MFAMethodSMS}
	}
	if contains(info.Methods, MFAMethodEmail) {
		info.EmailMasked = maskEmail(u.Email)
	}
	if contains(info.Methods, MFAMethodSMS) {
		info.PhoneMasked = maskPhone(deref(u.Phone))
	}
	return info
}

func (a *AuthService) ResetInfo(ctx context.Context, raw string) (*ResetInfo, error) {
	t, err := a.peekToken(ctx, raw, "reset_password")
	if err != nil {
		return nil, err
	}
	u, err := a.s.Q.GetUserByID(ctx, t.UserID)
	if err != nil {
		return nil, err
	}
	info := a.resetInfoFor(ctx, a.s.Q, u)
	return &info, nil
}

func (a *AuthService) ResetSendOTP(ctx context.Context, raw, method string) (string, error) {
	t, err := a.peekToken(ctx, raw, "reset_password")
	if err != nil {
		return "", err
	}
	u, err := a.s.Q.GetUserByID(ctx, t.UserID)
	if err != nil {
		return "", err
	}
	info := a.resetInfoFor(ctx, a.s.Q, u)
	if method == MFAMethodTOTP || !contains(info.Methods, method) {
		return "", apperr.BadRequest(apperr.CodeInvalidState, "metode ini tidak tersedia untuk akunmu")
	}
	return a.sendOTP(ctx, u, otpPurposeReset, method)
}

// verifyResetFactor dipanggil SEBELUM token dihabiskan.
func (a *AuthService) verifyResetFactor(ctx context.Context, u sqlcgen.User, method, code string) error {
	info := a.resetInfoFor(ctx, a.s.Q, u)
	if info.SecondFactor == "none" {
		return nil
	}
	fail := func(msg string) error {
		return apperr.New(401, apperr.CodeMFARequired, msg).With("mfa", true).With("second_factor", info.SecondFactor).
			With("methods", info.Methods).With("email_masked", info.EmailMasked).With("phone_masked", info.PhoneMasked)
	}
	if strings.TrimSpace(code) == "" {
		return fail("kode verifikasi diperlukan untuk mengatur ulang kata sandi")
	}
	method = strings.ToLower(strings.TrimSpace(method))
	if info.SecondFactor == "phone" {
		method = MFAMethodSMS // satu-satunya pilihan
	}
	var ok bool
	var err error
	if method == "" || method == MFAMethodTOTP {
		ok, err = a.verifyMFA(ctx, a.s.Q, u.ID, code)
	} else if contains(info.Methods, method) {
		ok, err = a.checkOTP(ctx, u.ID, otpPurposeReset, method, code)
	}
	if err != nil {
		return err
	}
	if !ok {
		return fail("kode salah atau kedaluwarsa")
	}
	return nil
}
