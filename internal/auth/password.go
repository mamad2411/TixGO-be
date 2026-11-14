package auth

import (
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/crypto/argon2"
)

// PasswordParams = parameter argon2id. Produksi: memori ≥ 64 MiB (OWASP: m=19-64MiB,t=2+,p=1).
type PasswordParams struct {
	MemoryKB uint32
	Time     uint32
	Threads  uint8
}

var DefaultPasswordParams = PasswordParams{MemoryKB: 64 * 1024, Time: 2, Threads: 2}

type PasswordHasher struct{ p PasswordParams }

func NewPasswordHasher(p PasswordParams) *PasswordHasher { return &PasswordHasher{p: p} }

const (
	saltLen = 16
	keyLen  = 32
)

// Hash menghasilkan string PHC: $argon2id$v=19$m=..,t=..,p=..$salt$hash
func (h *PasswordHasher) Hash(password string) string {
	salt := RandomBytes(saltLen)
	key := argon2.IDKey([]byte(password), salt, h.p.Time, h.p.MemoryKB, h.p.Threads, keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version,
		h.p.MemoryKB, h.p.Time, h.p.Threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

// Verify membandingkan sandi dengan hash PHC (constant-time).
func (h *PasswordHasher) Verify(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m, t uint32
	var p uint8
	var ver int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &ver); err != nil || ver != argon2.Version {
		return false
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// ValidatePasswordPolicy: ≥ 12 karakter, huruf dan angka (docs/06 M1-F1 AC1).
func ValidatePasswordPolicy(pw string) error {
	if len([]rune(pw)) < 12 {
		return errors.New("kata sandi minimal 12 karakter")
	}
	if len(pw) > 128 {
		return errors.New("kata sandi maksimal 128 karakter")
	}
	var letter, digit bool
	for _, r := range pw {
		switch {
		case unicode.IsLetter(r):
			letter = true
		case unicode.IsDigit(r):
			digit = true
		}
	}
	if !letter || !digit {
		return errors.New("kata sandi harus memuat huruf dan angka")
	}
	return nil
}
