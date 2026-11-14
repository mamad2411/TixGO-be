package auth

import (
	"strings"
	"testing"
	"time"

	"tixgo/backend/internal/clock"
)

func TestRandomCodeAlwaysFullLengthAndUnambiguous(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 20000; i++ { // regresi: versi lama panic ±5% karena memotong string pendek
		c := RandomCode(10)
		if len(c) != 10 {
			t.Fatalf("panjang %d", len(c))
		}
		if strings.ContainsAny(c, "0OIL1_-") {
			t.Fatalf("karakter ambigu: %s", c)
		}
		seen[c] = true
	}
	if len(seen) < 19990 {
		t.Fatalf("terlalu banyak duplikat: %d unik dari 20000", len(seen))
	}
}

func TestPasswordHashVerifyAndPolicy(t *testing.T) {
	h := NewPasswordHasher(PasswordParams{MemoryKB: 1024, Time: 1, Threads: 1})
	enc := h.Hash("Correct-Horse-9!")
	if !strings.HasPrefix(enc, "$argon2id$v=19$") {
		t.Fatalf("format PHC salah: %s", enc)
	}
	if !h.Verify("Correct-Horse-9!", enc) || h.Verify("salah", enc) {
		t.Fatal("verify salah")
	}
	if h.Hash("Correct-Horse-9!") == enc {
		t.Fatal("salt harus acak")
	}
	if h.Verify("x", "bukan-hash") || h.Verify("x", "$argon2id$v=19$m=1,t=1,p=1$!!$!!") {
		t.Fatal("hash rusak tidak boleh lolos")
	}
	for pw, ok := range map[string]bool{"pendek1": false, "hurufsajahurufsaja": false, "1234567890123456": false, "Cukup-Panjang-1": true} {
		if (ValidatePasswordPolicy(pw) == nil) != ok {
			t.Errorf("policy(%q) = %v, mau %v", pw, !ok, ok)
		}
	}
}

func TestBoxRoundTripAndTamper(t *testing.T) {
	b, err := NewBox(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	ct, _ := b.Encrypt([]byte("rahasia"))
	pt, err := b.Decrypt(ct)
	if err != nil || string(pt) != "rahasia" {
		t.Fatalf("round trip: %q %v", pt, err)
	}
	ct[len(ct)-1] ^= 1
	if _, err := b.Decrypt(ct); err == nil {
		t.Fatal("ciphertext diubah harus gagal (GCM)")
	}
	if _, err := NewBox("pendek"); err == nil {
		t.Fatal("kunci pendek harus ditolak")
	}
}

func TestTokensAccessAndAdmission(t *testing.T) {
	clk := clock.NewFake(time.Now())
	tk := NewTokens(strings.Repeat("k", 40), 15*time.Minute, clk)
	a, _, _ := tk.IssueAccess("U1", "customer", "S1", true)
	p, err := tk.ParseAccess(a)
	if err != nil || p.UserID != "U1" || p.Role != "customer" || p.SessionID != "S1" || !p.MFAPassed {
		t.Fatalf("p=%+v err=%v", p, err)
	}
	// token admission tidak boleh dipakai sebagai access (audience berbeda)
	ad, _, _ := tk.IssueAdmission("U1", "E1", time.Minute)
	if _, err := tk.ParseAccess(ad); err == nil {
		t.Fatal("admission token tidak boleh berlaku sebagai access token")
	}
	u, e, err := tk.ParseAdmission(ad)
	if err != nil || u != "U1" || e != "E1" {
		t.Fatalf("admission: %s %s %v", u, e, err)
	}
	clk.Advance(2 * time.Minute)
	if _, _, err := tk.ParseAdmission(ad); err != ErrTokenExpired {
		t.Fatalf("harus kedaluwarsa: %v", err)
	}
	// secret lain
	other := NewTokens(strings.Repeat("z", 40), time.Minute, clk)
	if _, err := other.ParseAccess(a); err == nil {
		t.Fatal("secret berbeda harus ditolak")
	}
}

func TestTOTPWindow(t *testing.T) {
	s := NewTOTPSecret()
	now := time.Now()
	c, _ := TOTPCode(s, now, 30)
	if !TOTPValidate(c, s, now, 30, 1) {
		t.Fatal("kode saat ini harus valid")
	}
	if !TOTPValidate(c, s, now.Add(30*time.Second), 30, 1) {
		t.Fatal("toleransi ±1 langkah")
	}
	if TOTPValidate(c, s, now.Add(5*time.Minute), 30, 1) {
		t.Fatal("kode lama (screenshot) harus ditolak")
	}
}

func TestOTPDigits(t *testing.T) {
	for i := 0; i < 500; i++ {
		d := RandomDigits(6)
		if len(d) != 6 || strings.Trim(d, "0123456789") != "" {
			t.Fatalf("OTP salah: %q", d)
		}
	}
}
