package apitest

import (
	"regexp"
	"testing"
	"time"

	"tixgo/backend/internal/auth"
)

var sixDigits = regexp.MustCompile(`\b(\d{6})\b`)

// otpFromMail / otpFromSMS: kode 6 digit terakhir yang dikirim ke tujuan tersebut.
func (e *Env) otpFromMail(to string) string {
	e.T.Helper()
	m, ok := e.Mail.LastMailTo(to)
	if !ok {
		e.T.Fatalf("tidak ada email OTP ke %s", to)
	}
	g := sixDigits.FindStringSubmatch(m.Text)
	if g == nil {
		e.T.Fatalf("email tanpa kode 6 digit: %q", m.Text)
	}
	return g[1]
}

func (e *Env) otpFromSMS(to string) string {
	e.T.Helper()
	m, ok := e.Mail.LastSMS(to)
	if !ok {
		e.T.Fatalf("tidak ada SMS OTP ke %s", to)
	}
	g := sixDigits.FindStringSubmatch(m.Text)
	if g == nil {
		e.T.Fatalf("SMS tanpa kode 6 digit: %q", m.Text)
	}
	return g[1]
}

// enableEmailMFA mengaktifkan OTP email lewat API dan mengembalikan kode pemulihan.
func enableEmailMFA(e *Env, c *Client) []string {
	e.T.Helper()
	c.POST(api+"/me/mfa/otp/email/start", nil).Expect(202)
	var conf struct {
		Recovery []string `json:"recovery_codes"`
	}
	c.POST(api+"/me/mfa/otp/email/confirm", M{"code": e.otpFromMail(c.Email)}).Expect(200).Data(&conf)
	return conf.Recovery
}

func TestEmailOTPSecondFactorAppliesToEveryLogin(t *testing.T) {
	e := NewEnv(t)
	c := e.Verified("Otp Email")

	// kode salah ditolak; belum aktif
	c.POST(api+"/me/mfa/otp/email/start", nil).Expect(202)
	c.POST(api+"/me/mfa/otp/email/confirm", M{"code": "000000"}).ExpectErr(400, "OTP_INVALID")
	c.POST(api+"/me/mfa/otp/email/confirm", M{"code": e.otpFromMail(c.Email)}).Expect(200)

	var st struct {
		Enabled bool     `json:"enabled"`
		Methods []string `json:"methods"`
	}
	c.GET(api + "/me/mfa").Expect(200).Data(&st)
	if !st.Enabled || len(st.Methods) != 1 || st.Methods[0] != "email" {
		t.Fatalf("status MFA: %+v", st)
	}

	// login dari perangkat/peramban BARU manapun sekarang minta faktor kedua
	for i := 0; i < 2; i++ {
		anon := e.Anon()
		anon.POST(api+"/auth/login", M{"email": c.Email, "password": testPassword}).ExpectErr(401, "MFA_REQUIRED")
	}
	anon := e.Anon()
	// kirim OTP butuh sandi yang benar
	anon.POST(api+"/auth/mfa/send", M{"email": c.Email, "password": "Salah-Salah-123", "method": "email"}).ExpectErr(401, "INVALID_CREDENTIALS")
	anon.POST(api+"/auth/mfa/send", M{"email": c.Email, "password": testPassword, "method": "sms"}).ExpectErr(400, "INVALID_STATE") // SMS belum diaktifkan
	anon.POST(api+"/auth/mfa/send", M{"email": c.Email, "password": testPassword, "method": "email"}).Expect(202)
	code := e.otpFromMail(c.Email)
	anon.POST(api+"/auth/login", M{"email": c.Email, "password": testPassword, "mfa_method": "email", "mfa_code": "000000"}).ExpectErr(401, "MFA_REQUIRED")
	anon.POST(api+"/auth/login", M{"email": c.Email, "password": testPassword, "mfa_method": "email", "mfa_code": code}).Expect(200)
	// sekali pakai
	anon.POST(api+"/auth/login", M{"email": c.Email, "password": testPassword, "mfa_method": "email", "mfa_code": code}).ExpectErr(401, "MFA_REQUIRED")
	// kode OTP tidak berlaku sebagai kode TOTP/pemulihan
	anon.POST(api+"/auth/login", M{"email": c.Email, "password": testPassword, "mfa_code": code}).ExpectErr(401, "MFA_REQUIRED")
}

func TestRecoveryCodeWorksWithoutTOTP(t *testing.T) {
	e := NewEnv(t)
	c := e.Verified("Pemulihan")
	rc := enableEmailMFA(e, c)
	if len(rc) != 8 {
		t.Fatalf("faktor pertama harus menerbitkan 8 kode pemulihan, dapat %d", len(rc))
	}
	e.Anon().POST(api+"/auth/login", M{"email": c.Email, "password": testPassword, "mfa_code": rc[0]}).Expect(200)
	e.Anon().POST(api+"/auth/login", M{"email": c.Email, "password": testPassword, "mfa_code": rc[0]}).ExpectErr(401, "MFA_REQUIRED") // sekali pakai
}

func TestSMSSecondFactorNeedsVerifiedPhone(t *testing.T) {
	e := NewEnv(t)
	noPhone := e.Register(uniqEmail("np"), "Tanpa HP")
	e.Exec("UPDATE users SET email_verified_at=now() WHERE id=$1", noPhone.UserID)
	noPhone.POST(api+"/me/mfa/otp/sms/start", nil).ExpectErr(409, "INVALID_STATE")

	c := e.Verified("Pakai SMS")
	phone := e.QueryStr("SELECT phone FROM users WHERE id=$1", c.UserID)
	c.POST(api+"/me/mfa/otp/sms/start", nil).Expect(202)
	c.POST(api+"/me/mfa/otp/sms/confirm", M{"code": e.otpFromSMS(phone)}).Expect(200)
	anon := e.Anon()
	anon.POST(api+"/auth/login", M{"email": c.Email, "password": testPassword}).ExpectErr(401, "MFA_REQUIRED")
	anon.POST(api+"/auth/mfa/send", M{"email": c.Email, "password": testPassword, "method": "sms"}).Expect(202)
	anon.POST(api+"/auth/login", M{"email": c.Email, "password": testPassword, "mfa_method": "sms", "mfa_code": e.otpFromSMS(phone)}).Expect(200)
}

func TestOTPLockoutAfterTooManyWrongCodes(t *testing.T) {
	e := NewEnv(t)
	c := e.Verified("Salah Terus")
	enableEmailMFA(e, c)
	anon := e.Anon()
	anon.POST(api+"/auth/mfa/send", M{"email": c.Email, "password": testPassword, "method": "email"}).Expect(202)
	real := e.otpFromMail(c.Email)
	for i := 0; i < 6; i++ {
		anon.Do("POST", api+"/auth/login", M{"email": c.Email, "password": testPassword, "mfa_method": "email", "mfa_code": "111111"})
	}
	// setelah dikunci, kode yang BENAR pun tidak diterima
	if r := anon.Do("POST", api+"/auth/login", M{"email": c.Email, "password": testPassword, "mfa_method": "email", "mfa_code": real}); r.Code == 200 {
		t.Fatalf("kode benar tidak boleh diterima setelah dikunci: %s", r.Body)
	}
}

func TestResetPasswordNeedsSecondFactor(t *testing.T) {
	e := NewEnv(t)
	c := e.Verified("Reset Aman")
	enableEmailMFA(e, c)
	newPw := "Sandi-Baru-77xyz"

	e.Anon().POST(api+"/auth/password/forgot", M{"email": c.Email}).Expect(202)
	tok := e.TokenFromMail(c.Email)
	anon := e.Anon()

	var info struct {
		SecondFactor string   `json:"second_factor"`
		Methods      []string `json:"methods"`
	}
	anon.POST(api+"/auth/password/reset/info", M{"token": tok}).Expect(200).Data(&info)
	if info.SecondFactor != "mfa" || len(info.Methods) != 1 || info.Methods[0] != "email" {
		t.Fatalf("info reset: %+v", info)
	}
	// tautan saja TIDAK cukup
	anon.POST(api+"/auth/password/reset", M{"token": tok, "password": newPw}).ExpectErr(401, "MFA_REQUIRED")
	anon.POST(api+"/auth/password/reset", M{"token": tok, "password": newPw, "mfa_method": "email", "mfa_code": "000000"}).ExpectErr(401, "MFA_REQUIRED")
	// kode salah tidak membakar tautan; kode benar berhasil
	anon.POST(api+"/auth/password/reset/send", M{"token": tok, "method": "email"}).Expect(202)
	code := e.otpFromMail(c.Email)
	anon.POST(api+"/auth/password/reset", M{"token": tok, "password": newPw, "mfa_method": "email", "mfa_code": code}).Expect(200)
	// tautan sekali pakai
	anon.POST(api+"/auth/password/reset", M{"token": tok, "password": "Lain-Lagi-9988x", "mfa_method": "email", "mfa_code": code}).ExpectErr(400, "TOKEN_INVALID")
	// sandi baru berlaku; login tetap minta faktor kedua
	anon.POST(api+"/auth/login", M{"email": c.Email, "password": newPw}).ExpectErr(401, "MFA_REQUIRED")
}

func TestResetPasswordWithoutMFAUsesVerifiedPhone(t *testing.T) {
	e := NewEnv(t)
	c := e.Verified("Reset Via HP")
	phone := e.QueryStr("SELECT phone FROM users WHERE id=$1", c.UserID)
	newPw := "Sandi-Baru-77xyz"
	e.Anon().POST(api+"/auth/password/forgot", M{"email": c.Email}).Expect(202)
	tok := e.TokenFromMail(c.Email)
	anon := e.Anon()
	var info struct {
		SecondFactor string   `json:"second_factor"`
		Methods      []string `json:"methods"`
	}
	anon.POST(api+"/auth/password/reset/info", M{"token": tok}).Expect(200).Data(&info)
	if info.SecondFactor != "phone" || len(info.Methods) != 1 || info.Methods[0] != "sms" {
		t.Fatalf("info reset: %+v", info)
	}
	anon.POST(api+"/auth/password/reset", M{"token": tok, "password": newPw}).ExpectErr(401, "MFA_REQUIRED")
	anon.POST(api+"/auth/password/reset/send", M{"token": tok, "method": "email"}).ExpectErr(400, "INVALID_STATE") // hanya SMS
	anon.POST(api+"/auth/password/reset/send", M{"token": tok, "method": "sms"}).Expect(202)
	anon.POST(api+"/auth/password/reset", M{"token": tok, "password": newPw, "mfa_code": e.otpFromSMS(phone)}).Expect(200)
	anon.POST(api+"/auth/login", M{"email": c.Email, "password": newPw}).Expect(200)
}

func TestChangePasswordAndDeleteNeedStepUp(t *testing.T) {
	e := NewEnv(t)
	c := e.Verified("Step Up")
	enableEmailMFA(e, c)
	newPw := "Sandi-Baru-77xyz"

	// ganti sandi: sandi lama saja tidak cukup
	c.POST(api+"/me/password", M{"current_password": testPassword, "new_password": newPw}).ExpectErr(401, "MFA_REQUIRED")
	c.POST(api+"/me/password", M{"current_password": testPassword, "new_password": newPw, "mfa_method": "email", "mfa_code": "000000"}).ExpectErr(401, "MFA_REQUIRED")
	c.POST(api+"/me/mfa/step-up/send", M{"method": "sms"}).ExpectErr(400, "INVALID_STATE") // SMS tidak aktif
	c.POST(api+"/me/mfa/step-up/send", M{"method": "email"}).Expect(202)
	c.POST(api+"/me/password", M{"current_password": testPassword, "new_password": newPw, "mfa_method": "email", "mfa_code": e.otpFromMail(c.Email)}).Expect(200)

	// hapus akun: sandi benar tetapi tanpa kode ditolak
	c2 := e.Anon()
	c2.POST(api+"/auth/mfa/send", M{"email": c.Email, "password": newPw, "method": "email"}).Expect(202)
	r := c2.POST(api+"/auth/login", M{"email": c.Email, "password": newPw, "mfa_method": "email", "mfa_code": e.otpFromMail(c.Email)}).Expect(200)
	var sess struct {
		AccessToken string `json:"access_token"`
	}
	r.Data(&sess)
	c2.Token = sess.AccessToken
	c2.POST(api+"/me/privacy/delete-account", M{"password": newPw}).ExpectErr(401, "MFA_REQUIRED")
}

func TestDisableLastFactorRestoresPlainLogin(t *testing.T) {
	e := NewEnv(t)
	c := e.Verified("Matikan")
	enableEmailMFA(e, c)
	// matikan: sandi + kode dari faktor yang aktif
	c.DELETE(api+"/me/mfa/methods/email").ExpectErr(400, "VALIDATION_FAILED")
	c.POST(api+"/me/mfa/step-up/send", M{"method": "email"}).Expect(202)
	code := e.otpFromMail(c.Email)
	c.Do("DELETE", api+"/me/mfa/methods/email", M{"password": "Salah-Salah-123", "code": code, "code_method": "email"}).ExpectErr(401, "INVALID_CREDENTIALS")
	c.Do("DELETE", api+"/me/mfa/methods/email", M{"password": testPassword, "code": code, "code_method": "email"}).Expect(204)
	e.Anon().POST(api+"/auth/login", M{"email": c.Email, "password": testPassword}).Expect(200)
	if n := e.QueryInt("SELECT count(*) FROM mfa_recovery_codes WHERE user_id=$1", c.UserID); n != 0 {
		t.Fatalf("kode pemulihan harus dihapus bersama faktor terakhir, tersisa %d", n)
	}
}

func TestAddAuthenticatorAfterEmailFactorAndKeepBoth(t *testing.T) {
	e := NewEnv(t)
	c := e.Verified("Dua Faktor")
	enableEmailMFA(e, c)
	var setup struct {
		Secret string `json:"secret"`
	}
	c.POST(api+"/me/mfa/totp/setup", nil).Expect(200).Data(&setup)
	code, _ := auth.TOTPCode(setup.Secret, e.Clock.Now(), 30)
	var conf struct {
		Recovery []string `json:"recovery_codes"`
	}
	c.POST(api+"/me/mfa/totp/confirm", M{"code": code}).Expect(200).Data(&conf)
	var st struct {
		Methods []string `json:"methods"`
	}
	c.GET(api + "/me/mfa").Expect(200).Data(&st)
	if len(st.Methods) != 2 {
		t.Fatalf("harus ada totp dan email: %+v", st)
	}
	// login minta salah satunya; TOTP tanpa kirim apa pun
	e.Clock.Advance(31 * time.Second)
	code2, _ := auth.TOTPCode(setup.Secret, e.Clock.Now(), 30)
	e.Anon().POST(api+"/auth/login", M{"email": c.Email, "password": testPassword, "mfa_method": "totp", "mfa_code": code2}).Expect(200)
	// mematikan TOTP tidak mematikan MFA: email tetap berlaku dan kode pemulihan tetap ada
	c.POST(api+"/me/mfa/step-up/send", M{"method": "email"}).Expect(202)
	c.Do("DELETE", api+"/me/mfa/methods/totp", M{"password": testPassword, "code": e.otpFromMail(c.Email), "code_method": "email"}).Expect(204)
	e.Anon().POST(api+"/auth/login", M{"email": c.Email, "password": testPassword}).ExpectErr(401, "MFA_REQUIRED")
	if n := e.QueryInt("SELECT count(*) FROM mfa_recovery_codes WHERE user_id=$1", c.UserID); n == 0 {
		t.Fatal("kode pemulihan harus tetap ada selama masih ada faktor")
	}
}
