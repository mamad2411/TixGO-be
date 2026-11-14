package apitest

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"tixgo/backend/internal/auth"
)

const api = "/api/v1"

func TestRegisterAndValidation(t *testing.T) {
	e := NewEnv(t)
	c := e.Anon()

	// AC M1-F1/AC1: sandi ≥ 12 karakter, huruf+angka
	c.POST(api+"/auth/register", M{"email": "a@example.com", "password": "pendek1", "name": "Rara", "accept_terms": true}).ExpectErr(400, "VALIDATION_FAILED")
	c.POST(api+"/auth/register", M{"email": "a@example.com", "password": "hurufsajahurufsaja", "name": "Rara", "accept_terms": true}).ExpectErr(400, "VALIDATION_FAILED")
	c.POST(api+"/auth/register", M{"email": "bukan-email", "password": testPassword, "name": "Rara", "accept_terms": true}).ExpectErr(400, "VALIDATION_FAILED")
	// persetujuan wajib (M28 AC2)
	c.POST(api+"/auth/register", M{"email": "a@example.com", "password": testPassword, "name": "Rara", "accept_terms": false}).ExpectErr(400, "VALIDATION_FAILED")

	r := c.POST(api+"/auth/register", M{"email": "Rara@Example.com", "password": testPassword, "name": " Rara ", "accept_terms": true}).Expect(201)
	m := r.Map()
	user := m["user"].(map[string]any)
	if user["email"] != "rara@example.com" || user["name"] != "Rara" || user["role"] != "customer" {
		t.Fatalf("user tidak dinormalisasi: %v", user)
	}
	if strings.Contains(string(r.Body), "password") {
		t.Fatal("respons tidak boleh memuat hash/sandi")
	}
	if c.Cookies["tixgo_rt"] == "" {
		t.Fatal("refresh token harus dikirim sebagai cookie")
	}
	// hash sandi argon2id di DB
	if h := e.QueryStr("SELECT password_hash FROM users WHERE email='rara@example.com'"); !strings.HasPrefix(h, "$argon2id$") {
		t.Fatalf("hash bukan argon2id: %s", h)
	}
	// email verifikasi terkirim
	if _, ok := e.Mail.LastMailTo("rara@example.com"); !ok {
		t.Fatal("email verifikasi harus terkirim")
	}
	// duplikat
	e.Anon().POST(api+"/auth/register", M{"email": "rara@example.com", "password": testPassword, "name": "Rara 2", "accept_terms": true}).ExpectErr(409, "EMAIL_TAKEN")
}

func TestEmailVerification(t *testing.T) {
	e := NewEnv(t)
	c := e.Register("v@example.com", "Vera")
	tok := e.TokenFromMail("v@example.com")
	anon := e.Anon()
	anon.POST(api+"/auth/email/verify", M{"token": "salah-salah-salah"}).ExpectErr(400, "TOKEN_INVALID")
	anon.POST(api+"/auth/email/verify", M{"token": tok}).Expect(200)
	var me struct {
		EmailVerified bool `json:"email_verified"`
	}
	c.GET(api + "/me").Expect(200).Data(&me)
	if !me.EmailVerified {
		t.Fatal("email harus terverifikasi")
	}
	// sekali pakai
	anon.POST(api+"/auth/email/verify", M{"token": tok}).ExpectErr(400, "TOKEN_INVALID")

	// kedaluwarsa
	c2 := e.Register("w@example.com", "Wawan")
	_ = c2
	tok2 := e.TokenFromMail("w@example.com")
	e.Clock.Advance(25 * time.Hour)
	anon.POST(api+"/auth/email/verify", M{"token": tok2}).ExpectErr(400, "TOKEN_EXPIRED")
}

func TestLoginErrorsLockoutAndNoEnumeration(t *testing.T) {
	e := NewEnv(t)
	e.Register("l@example.com", "Lia")
	c := e.Anon()

	bad := c.POST(api+"/auth/login", M{"email": "l@example.com", "password": "Salah-Salah-123"})
	bad.ExpectErr(401, "INVALID_CREDENTIALS")
	none := c.POST(api+"/auth/login", M{"email": "tidak-ada@example.com", "password": "Salah-Salah-123"})
	none.ExpectErr(401, "INVALID_CREDENTIALS")
	stripRID := regexp.MustCompile(`,"request_id":"[^"]*"`)
	if stripRID.ReplaceAllString(string(bad.Body), "") != stripRID.ReplaceAllString(string(none.Body), "") {
		t.Fatalf("respons email-ada vs tidak-ada harus identik (anti-enumerasi):\n%s\n%s", bad.Body, none.Body)
	}

	// 5 kegagalan → terkunci 15 menit (T), walau sandi benar
	for i := 0; i < 4; i++ {
		c.POST(api+"/auth/login", M{"email": "l@example.com", "password": "Salah-Salah-123"}).ExpectErr(401, "INVALID_CREDENTIALS")
	}
	c.POST(api+"/auth/login", M{"email": "l@example.com", "password": testPassword}).ExpectErr(429, "ACCOUNT_LOCKED")
	e.Clock.Advance(16 * time.Minute)
	c.POST(api+"/auth/login", M{"email": "l@example.com", "password": testPassword}).Expect(200)
}

func TestLoginRateLimitByIP(t *testing.T) {
	e := NewEnv(t)
	e.EnableRateLimit()
	e.Register("rl@example.com", "Rate")
	c := e.Anon().WithIP("198.51.100.7")
	var limited int
	for i := 0; i < 40; i++ {
		r := c.POST(api+"/auth/login", M{"email": "x@example.com", "password": "Salah-Salah-123"})
		if r.Code == 429 {
			limited++
			if r.Header.Get("Retry-After") == "" {
				t.Fatal("429 harus memuat Retry-After")
			}
		}
	}
	if limited == 0 {
		t.Fatal("harus ada pembatasan laju (AC5)")
	}
}

func TestRefreshRotationAndReuseDetection(t *testing.T) {
	e := NewEnv(t)
	c := e.Register("r@example.com", "Rini")
	oldCookie := c.Cookies["tixgo_rt"]

	// CSRF: tanpa header kustom ditolak
	c.Do("POST", api+"/auth/refresh", nil).ExpectErr(403, "FORBIDDEN")
	// origin asing ditolak
	c.Do("POST", api+"/auth/refresh", nil, "X-Requested-With", "tixgo", "Origin", "http://evil.test").ExpectErr(403, "FORBIDDEN")

	r := c.Refresh().Expect(200)
	var d struct {
		AccessToken string `json:"access_token"`
	}
	r.Data(&d)
	newCookie := c.Cookies["tixgo_rt"]
	if newCookie == oldCookie {
		t.Fatal("refresh token harus dirotasi")
	}
	// token lama dipakai ulang → seluruh keluarga dicabut
	thief := e.Anon()
	thief.Cookies["tixgo_rt"] = oldCookie
	thief.Refresh().ExpectErr(401, "TOKEN_INVALID")
	// pemilik sah juga kehilangan sesi (keluarga dicabut)
	c.Refresh().ExpectErr(401, "TOKEN_INVALID")
	// access token turunan keluarga itu tidak berlaku lagi
	c2 := e.Anon()
	c2.Token = d.AccessToken
	c2.GET(api+"/me").ExpectErr(401, "UNAUTHENTICATED")
}

func TestLogoutRevokesAccessToken(t *testing.T) {
	e := NewEnv(t)
	c := e.Register("lo@example.com", "Logout")
	c.GET(api + "/me").Expect(200)
	c.POST(api+"/auth/logout", nil).Expect(204)
	c.GET(api+"/me").ExpectErr(401, "UNAUTHENTICATED")
	// refresh cookie juga hangus
	c.Refresh().ExpectErr(401, "TOKEN_INVALID")
}

func TestAccessTokenExpiry(t *testing.T) {
	e := NewEnv(t)
	c := e.Register("ex@example.com", "Expire")
	e.Clock.Advance(16 * time.Minute)
	c.GET(api+"/me").ExpectErr(401, "TOKEN_EXPIRED")
	// refresh (masih dalam 30 hari) memulihkan sesi
	var d struct {
		AccessToken string `json:"access_token"`
	}
	c.Refresh().Expect(200).Data(&d)
	c.Token = d.AccessToken
	c.GET(api + "/me").Expect(200)
	// tanpa/format token salah
	e.Anon().GET(api+"/me").ExpectErr(401, "UNAUTHENTICATED")
	bad := e.Anon()
	bad.Token = "bukan.jwt.valid"
	bad.GET(api+"/me").ExpectErr(401, "UNAUTHENTICATED")
}

func TestForgotAndResetPassword(t *testing.T) {
	e := NewEnv(t)
	c := e.Register("fp@example.com", "Fita")
	other := e.Login("fp@example.com") // sesi kedua
	before := e.Mail.Count()

	// respons sama untuk email tak terdaftar (T21), dan tidak ada email
	unk := e.Anon().POST(api+"/auth/password/forgot", M{"email": "tak-ada@example.com"}).Expect(202)
	known := e.Anon().POST(api+"/auth/password/forgot", M{"email": "fp@example.com"}).Expect(202)
	if string(unk.Body) != string(known.Body) {
		t.Fatalf("respons forgot harus identik: %s vs %s", unk.Body, known.Body)
	}
	if e.Mail.Count() != before+1 {
		t.Fatalf("hanya email terdaftar yang menerima tautan reset (count %d→%d)", before, e.Mail.Count())
	}
	tok := e.TokenFromMail("fp@example.com")

	anon := e.Anon()
	newPw := "Sandi-Baru-77xyz"
	anon.POST(api+"/auth/password/reset", M{"token": tok, "password": "pendek"}).ExpectErr(400, "VALIDATION_FAILED")
	// tidak boleh sama dengan sandi saat ini (M27 AC4)
	anon.POST(api+"/auth/password/reset", M{"token": tok, "password": testPassword}).ExpectErr(400, "VALIDATION_FAILED")
	anon.POST(api+"/auth/password/reset", M{"token": tok, "password": newPw}).Expect(200)
	// token sekali pakai (T22)
	anon.POST(api+"/auth/password/reset", M{"token": tok, "password": "Lain-Lagi-9988x"}).ExpectErr(400, "TOKEN_INVALID")
	// semua sesi lama dicabut setelah reset (T22)
	c.GET(api+"/me").ExpectErr(401, "UNAUTHENTICATED")
	other.GET(api+"/me").ExpectErr(401, "UNAUTHENTICATED")
	// login dengan sandi baru; lama ditolak
	anon.POST(api+"/auth/login", M{"email": "fp@example.com", "password": testPassword}).ExpectErr(401, "INVALID_CREDENTIALS")
	anon.POST(api+"/auth/login", M{"email": "fp@example.com", "password": newPw}).Expect(200)

	// token kedaluwarsa (T22)
	e.Anon().POST(api+"/auth/password/forgot", M{"email": "fp@example.com"}).Expect(202)
	tok2 := e.TokenFromMail("fp@example.com")
	e.Clock.Advance(31 * time.Minute)
	anon.POST(api+"/auth/password/reset", M{"token": tok2, "password": "Lain-Lagi-9988x"}).ExpectErr(400, "TOKEN_EXPIRED")
}

func TestPasswordHistoryBlocksLastThree(t *testing.T) {
	e := NewEnv(t)
	c := e.Register("ph@example.com", "Hist")
	pws := []string{"Sandi-Dua-111abc", "Sandi-Tiga-222abc", "Sandi-Empat-333abc"}
	cur := testPassword
	for _, p := range pws {
		c.POST(api+"/me/password", M{"current_password": cur, "new_password": p}).Expect(200)
		cur = p
		c = e.Anon()
		r := c.POST(api+"/auth/login", M{"email": "ph@example.com", "password": cur}).Expect(200)
		var d struct {
			AccessToken string `json:"access_token"`
		}
		r.Data(&d)
		c.Token = d.AccessToken
	}
	// sandi awal sudah di luar 3 terakhir (riwayat: awal,2,3,4 → 3 terakhir = 4,3,2 + saat ini 4)
	c.POST(api+"/me/password", M{"current_password": cur, "new_password": pws[0]}).ExpectErr(400, "VALIDATION_FAILED")
	c.POST(api+"/me/password", M{"current_password": cur, "new_password": pws[1]}).ExpectErr(400, "VALIDATION_FAILED")
	c.POST(api+"/me/password", M{"current_password": cur, "new_password": testPassword}).Expect(200)
}

func TestChangePasswordRevokesOtherSessionsOnly(t *testing.T) {
	e := NewEnv(t)
	cur := e.Register("cp@example.com", "Cipi")
	other := e.Login("cp@example.com")
	cur.POST(api+"/me/password", M{"current_password": "Salah-Salah-123", "new_password": "Sandi-Baru-77xyz"}).ExpectErr(401, "INVALID_CREDENTIALS")
	cur.POST(api+"/me/password", M{"current_password": testPassword, "new_password": "Sandi-Baru-77xyz"}).Expect(200)
	cur.GET(api + "/me").Expect(200)                       // sesi saat ini tetap
	other.GET(api+"/me").ExpectErr(401, "UNAUTHENTICATED") // sesi lain dicabut
	if _, ok := e.Mail.LastMailTo("cp@example.com"); !ok {
		t.Fatal("pemberitahuan perubahan sandi harus terkirim")
	}
}

func TestPhoneOTPFlow(t *testing.T) {
	e := NewEnv(t)
	c := e.Register("o@example.com", "Oki")
	c.POST(api+"/auth/otp/send", M{"phone": "123"}).ExpectErr(400, "VALIDATION_FAILED")
	c.POST(api+"/auth/otp/send", M{"phone": "0812-3456-7890"}).Expect(202)
	sms, ok := e.Mail.LastSMS("+6281234567890")
	if !ok {
		t.Fatal("SMS OTP harus terkirim ke nomor ternormalisasi E.164")
	}
	code := sms.Text[strings.Index(sms.Text, ": ")+2 : strings.Index(sms.Text, ": ")+8]
	// salah dulu → sisa percobaan berkurang
	r := c.POST(api+"/auth/otp/verify", M{"code": "000000"})
	r.ExpectErr(400, "OTP_INVALID")
	if code == "000000" {
		code = "000001"
	}
	c.POST(api+"/auth/otp/verify", M{"code": code}).Expect(200)
	var me struct {
		PhoneVerified bool    `json:"phone_verified"`
		Phone         *string `json:"phone"`
	}
	c.GET(api + "/me").Expect(200).Data(&me)
	if !me.PhoneVerified || me.Phone == nil || *me.Phone != "+6281234567890" {
		t.Fatalf("telepon tidak terverifikasi benar: %+v", me)
	}

	// nomor yang sama dipakai akun lain → PHONE_TAKEN (M1 AC2)
	c2 := e.Register("o2@example.com", "Oki Dua")
	c2.POST(api+"/auth/otp/send", M{"phone": "+6281234567890"}).ExpectErr(409, "PHONE_TAKEN")
	// ganti nomor butuh sandi
	c.POST(api+"/auth/otp/send", M{"phone": "+6281999888777"}).ExpectErr(401, "INVALID_CREDENTIALS")
	c.POST(api+"/auth/otp/send", M{"phone": "+6281999888777", "password": testPassword}).Expect(202)
}

func TestOTPLockoutAfterFiveWrongTries(t *testing.T) {
	e := NewEnv(t)
	c := e.Register("ol@example.com", "Olla")
	c.POST(api+"/auth/otp/send", M{"phone": "+6281777666555"}).Expect(202)
	for i := 0; i < 4; i++ {
		c.POST(api+"/auth/otp/verify", M{"code": "999999"}).ExpectErr(400, "OTP_INVALID")
	}
	c.POST(api+"/auth/otp/verify", M{"code": "999999"}).ExpectErr(429, "OTP_LOCKED")
	// terkunci walau minta OTP baru
	c.POST(api+"/auth/otp/send", M{"phone": "+6281777666555"}).ExpectErr(429, "OTP_LOCKED")
}

func TestOTPExpiry(t *testing.T) {
	e := NewEnv(t)
	c := e.Register("oe@example.com", "Oex")
	c.POST(api+"/auth/otp/send", M{"phone": "+6281222333444"}).Expect(202)
	// TTL Redis nyata; paksa kedaluwarsa
	e.Redis.Del(e.ctx, "otp:"+c.UserID)
	c.POST(api+"/auth/otp/verify", M{"code": "123456"}).ExpectErr(400, "OTP_INVALID")
}

func TestMFAFlow(t *testing.T) {
	e := NewEnv(t)
	c := e.Register("mfa@example.com", "Mira")
	var setup struct {
		Secret string `json:"secret"`
	}
	c.POST(api+"/me/mfa/totp/setup", nil).Expect(200).Data(&setup)
	if len(setup.Secret) < 16 {
		t.Fatal("secret MFA kosong")
	}
	// kode salah
	c.POST(api+"/me/mfa/totp/confirm", M{"code": "000000"}).ExpectErr(400, "OTP_INVALID")
	code, _ := auth.TOTPCode(setup.Secret, e.Clock.Now(), 30)
	var conf struct {
		Recovery []string `json:"recovery_codes"`
		Access   string   `json:"access_token"`
	}
	c.POST(api+"/me/mfa/totp/confirm", M{"code": code}).Expect(200).Data(&conf)
	if len(conf.Recovery) != 8 {
		t.Fatalf("harus 8 kode pemulihan, dapat %d", len(conf.Recovery))
	}
	// login sekarang butuh mfa_code
	anon := e.Anon()
	anon.POST(api+"/auth/login", M{"email": "mfa@example.com", "password": testPassword}).ExpectErr(401, "MFA_REQUIRED")
	anon.POST(api+"/auth/login", M{"email": "mfa@example.com", "password": testPassword, "mfa_code": "000000"}).ExpectErr(401, "MFA_REQUIRED")
	// kode yang sama (sudah dipakai saat confirm? tidak — confirm tidak menandai) harus berhasil sekali, lalu replay ditolak
	e.Clock.Advance(31 * time.Second)
	code2, _ := auth.TOTPCode(setup.Secret, e.Clock.Now(), 30)
	anon.POST(api+"/auth/login", M{"email": "mfa@example.com", "password": testPassword, "mfa_code": code2}).Expect(200)
	anon.POST(api+"/auth/login", M{"email": "mfa@example.com", "password": testPassword, "mfa_code": code2}).ExpectErr(401, "MFA_REQUIRED")
	// kode pemulihan: sekali pakai
	rc := conf.Recovery[0]
	anon.POST(api+"/auth/login", M{"email": "mfa@example.com", "password": testPassword, "mfa_code": rc}).Expect(200)
	anon.POST(api+"/auth/login", M{"email": "mfa@example.com", "password": testPassword, "mfa_code": rc}).ExpectErr(401, "MFA_REQUIRED")
	// nonaktifkan butuh sandi + kode
	c.POST(api+"/me/mfa/totp/disable", M{"password": "Salah-Salah-123", "code": conf.Recovery[1]}).ExpectErr(401, "INVALID_CREDENTIALS")
	c.POST(api+"/me/mfa/totp/disable", M{"password": testPassword, "code": conf.Recovery[1]}).Expect(204)
	anon.POST(api+"/auth/login", M{"email": "mfa@example.com", "password": testPassword}).Expect(200)
}

func TestAdminRequiresMFASession(t *testing.T) {
	e := NewEnv(t)
	// admin tanpa MFA → ditolak di endpoint sensitif
	email := uniqEmail("adm")
	c := e.Register(email, "Admin Tanpa MFA")
	e.Exec("UPDATE users SET role='admin' WHERE id=$1", c.UserID)
	c = e.Login(email)
	c.POST(api+"/admin/legal", M{"kind": "terms", "locale": "id", "title": "t", "body": "b"}).ExpectErr(403, "MFA_REQUIRED")
	// admin dengan sesi MFA lolos
	ok := e.UserWithRole("admin", "Admin MFA")
	ok.POST(api+"/admin/legal", M{"kind": "terms", "locale": "id", "title": "t", "body": "b"}).Expect(201)
	// customer ditolak
	cust := e.Verified("Pembeli")
	cust.POST(api+"/admin/legal", M{"kind": "terms", "locale": "id", "title": "t", "body": "b"}).ExpectErr(403, "FORBIDDEN")
}

func TestDevicesListAndRevoke(t *testing.T) {
	e := NewEnv(t)
	a := e.Register("dv@example.com", "Devi")
	b := e.Login("dv@example.com")
	b.UA = "Mozilla/5.0 (iPhone)"
	var devs []struct {
		SessionID string `json:"session_id"`
		Label     string `json:"label"`
		Current   bool   `json:"current"`
	}
	a.GET(api + "/me/devices").Expect(200).Data(&devs)
	if len(devs) != 2 {
		t.Fatalf("harus 2 perangkat, dapat %d", len(devs))
	}
	var other string
	for _, d := range devs {
		if !d.Current {
			other = d.SessionID
		}
	}
	// tidak bisa mencabut sesi milik orang lain
	x := e.Register("dx@example.com", "Dex")
	x.DELETE(api+"/me/devices/"+other).ExpectErr(404, "NOT_FOUND")
	a.DELETE(api + "/me/devices/" + other).Expect(204)
	b.GET(api+"/me").ExpectErr(401, "UNAUTHENTICATED")
	a.GET(api + "/me").Expect(200)
}

func TestEmailChangeFlow(t *testing.T) {
	e := NewEnv(t)
	c := e.Register("old@example.com", "Oldi")
	e.Register("taken@example.com", "Taken")
	c.POST(api+"/me/email", M{"email": "new@example.com", "password": "Salah-Salah-123"}).ExpectErr(401, "INVALID_CREDENTIALS")
	c.POST(api+"/me/email", M{"email": "taken@example.com", "password": testPassword}).ExpectErr(409, "EMAIL_TAKEN")
	c.POST(api+"/me/email", M{"email": "new@example.com", "password": testPassword}).Expect(202)
	tok := e.TokenFromMail("new@example.com") // dikirim ke ALAMAT BARU
	// belum berubah sebelum konfirmasi
	var me struct {
		Email string `json:"email"`
	}
	c.GET(api + "/me").Data(&me)
	if me.Email != "old@example.com" {
		t.Fatal("email tidak boleh berubah sebelum konfirmasi")
	}
	e.Anon().POST(api+"/auth/email/change/confirm", M{"token": tok}).Expect(200)
	c.GET(api + "/me").Data(&me)
	if me.Email != "new@example.com" {
		t.Fatalf("email harus berubah, dapat %s", me.Email)
	}
	// pemberitahuan ke alamat lama
	found := false
	for _, m := range e.Mail.MailsTo("old@example.com") {
		if strings.Contains(m.Text, "diganti") {
			found = true
		}
	}
	if !found {
		t.Fatal("alamat lama harus diberi tahu")
	}
}

func TestProfileUpdate(t *testing.T) {
	e := NewEnv(t)
	c := e.Register("pr@example.com", "Prima")
	c.PATCH(api+"/me", M{"name": "P"}).ExpectErr(400, "VALIDATION_FAILED")
	c.PATCH(api+"/me", M{"locale": "fr"}).ExpectErr(400, "VALIDATION_FAILED")
	var u struct {
		Name   string `json:"name"`
		Locale string `json:"locale"`
	}
	c.PATCH(api+"/me", M{"name": "Prima Baru", "locale": "en"}).Expect(200).Data(&u)
	if u.Name != "Prima Baru" || u.Locale != "en" {
		t.Fatalf("profil tidak diperbarui: %+v", u)
	}
}

// ---------- Legal (M28) ----------

func TestLegalVersionedDocsAndConsent(t *testing.T) {
	e := NewEnv(t)
	terms, _ := e.SeedLegal()

	// publik dapat membaca dokumen aktif
	var doc struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
	}
	e.Anon().GET(api + "/legal/terms").Expect(200).Data(&doc)
	if doc.ID != terms || doc.Version != 1 {
		t.Fatalf("dokumen aktif salah: %+v", doc)
	}
	e.Anon().GET(api+"/legal/tidak-ada").ExpectErr(404, "NOT_FOUND")
	// locale en tidak ada → jatuh ke id
	e.Anon().GET(api + "/legal/terms?locale=en").Expect(200)

	// pendaftaran mencatat persetujuan versi terbit (AC2)
	c := e.Register("lg@example.com", "Legal")
	if n := e.QueryInt("SELECT count(*) FROM consents WHERE user_id=$1", c.UserID); n != 2 {
		t.Fatalf("harus mencatat 2 persetujuan (terms+privacy), dapat %d", n)
	}
	var mine struct {
		Pending []any `json:"pending"`
	}
	c.GET(api + "/me/consents").Expect(200).Data(&mine)
	if len(mine.Pending) != 0 {
		t.Fatal("tidak boleh ada pending setelah daftar")
	}

	// versi baru terbit → pengguna lama punya pending (T24)
	admin := e.UserWithRole("admin", "Admin")
	var d2 struct {
		ID string `json:"id"`
	}
	admin.POST(api+"/admin/legal", M{"kind": "terms", "locale": "id", "title": "terms v2", "body": "isi baru"}).Expect(201).Data(&d2)
	// draf belum mengubah dokumen aktif
	e.Anon().GET(api + "/legal/terms").Data(&doc)
	if doc.Version != 1 {
		t.Fatal("draf tidak boleh jadi dokumen aktif")
	}
	admin.PUT(api+"/admin/legal/"+d2.ID, M{"title": "terms v2b", "body": "isi baru b"}).Expect(200)
	admin.POST(api+"/admin/legal/"+d2.ID+"/publish", nil).Expect(200)
	// versi terbit tidak dapat diubah
	admin.PUT(api+"/admin/legal/"+d2.ID, M{"title": "x", "body": "y"}).ExpectErr(409, "INVALID_STATE")
	admin.POST(api+"/admin/legal/"+d2.ID+"/publish", nil).ExpectErr(409, "INVALID_STATE")
	e.Anon().GET(api + "/legal/terms").Data(&doc)
	if doc.Version != 2 {
		t.Fatalf("versi aktif harus 2, dapat %d", doc.Version)
	}
	c.GET(api + "/me/consents").Expect(200).Data(&mine)
	if len(mine.Pending) != 1 {
		t.Fatalf("harus 1 pending, dapat %d", len(mine.Pending))
	}
	if err := e.Svc.Legal.RequireConsents(e.ctx, e.Q(), c.UserID); err == nil {
		t.Fatal("RequireConsents harus menolak sampai disetujui")
	}
	c.POST(api+"/me/consents", M{"document_id": d2.ID}).Expect(204)
	if err := e.Svc.Legal.RequireConsents(e.ctx, e.Q(), c.UserID); err != nil {
		t.Fatalf("setelah setuju harus lolos: %v", err)
	}
	// dokumen draf tidak bisa disetujui
	var d3 struct {
		ID string `json:"id"`
	}
	admin.POST(api+"/admin/legal", M{"kind": "refund", "locale": "id", "title": "r", "body": "b"}).Expect(201).Data(&d3)
	c.POST(api+"/me/consents", M{"document_id": d3.ID}).ExpectErr(400, "INVALID_STATE")
}

func TestCookieConsentDefaultsToEssentialOnly(t *testing.T) {
	e := NewEnv(t)
	c := e.Anon()
	var v struct {
		Analytics bool `json:"analytics"`
		Marketing bool `json:"marketing"`
	}
	c.GET(api+"/cookie-consent", "X-Cookie-Subject", "anon-subject-123").Expect(200).Data(&v)
	if v.Analytics || v.Marketing {
		t.Fatal("tanpa persetujuan harus hanya cookie esensial (T25)")
	}
	c.PUT(api+"/cookie-consent", M{"analytics": true, "marketing": false}, "X-Cookie-Subject", "anon-subject-123").Expect(200)
	c.GET(api+"/cookie-consent", "X-Cookie-Subject", "anon-subject-123").Expect(200).Data(&v)
	if !v.Analytics || v.Marketing {
		t.Fatalf("pilihan tidak tersimpan: %+v", v)
	}
	c.PUT(api+"/cookie-consent", M{"analytics": true}, "X-Cookie-Subject", "x").ExpectErr(400, "VALIDATION_FAILED")
}

func TestHealthEndpointsAndRequestID(t *testing.T) {
	e := NewEnv(t)
	r := e.Anon().GET("/healthz").Expect(200)
	if r.Header.Get("X-Request-Id") == "" {
		t.Fatal("X-Request-Id harus selalu ada")
	}
	r2 := e.Anon().GET("/healthz", "X-Request-Id", "req-abcdef123456").Expect(200)
	if r2.Header.Get("X-Request-Id") != "req-abcdef123456" {
		t.Fatal("X-Request-Id dari klien harus dipertahankan")
	}
}

func TestCORSOnlyForConfiguredOrigins(t *testing.T) {
	e := NewEnv(t)
	r := e.Anon().Do("OPTIONS", api+"/auth/login", nil, "Origin", "http://localhost:5174")
	if r.Code != 204 || r.Header.Get("Access-Control-Allow-Origin") != "http://localhost:5174" || r.Header.Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("CORS origin sah ditolak: %d %v", r.Code, r.Header)
	}
	r = e.Anon().Do("OPTIONS", api+"/auth/login", nil, "Origin", "http://evil.test")
	if r.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("origin asing tidak boleh mendapat header CORS")
	}
}

func TestAvatarSetAndClear(t *testing.T) {
	e := NewEnv(t)
	u := e.Verified("Foto")
	var m struct{ ID string }
	u.Upload(api+"/media", map[string]string{"kind": "avatar"}, "file", "a.png", pngBytes(600, 600)).Expect(201).Data(&m)
	var me struct {
		AvatarKey string `json:"avatar_key"`
	}
	u.PATCH(api+"/me", M{"avatar_asset_id": m.ID}).Expect(200).Data(&me)
	if me.AvatarKey != m.ID {
		t.Fatalf("avatar_key = %q, mau %q", me.AvatarKey, m.ID)
	}
	// aset orang lain / jenis lain ditolak
	other := e.Verified("Lain")
	other.PATCH(api+"/me", M{"avatar_asset_id": m.ID}).ExpectErr(403, "FORBIDDEN")
	var poster struct{ ID string }
	u.Upload(api+"/media", map[string]string{"kind": "photo"}, "file", "p.png", pngBytes(600, 600)).Expect(201).Data(&poster)
	u.PATCH(api+"/me", M{"avatar_asset_id": poster.ID}).ExpectErr(403, "FORBIDDEN")
	// hapus
	var cleared struct {
		AvatarKey string `json:"avatar_key"`
	}
	u.PATCH(api+"/me", M{"avatar_asset_id": ""}).Expect(200).Data(&cleared)
	if cleared.AvatarKey != "" {
		t.Fatalf("avatar harus terhapus: %q", cleared.AvatarKey)
	}
}
