// Package auth = primitif keamanan: hash sandi, JWT, enkripsi kolom, token acak, TOTP.
package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
)

// Box = AES-256-GCM untuk kolom *_enc (docs/12 §5). Nonce acak di depan ciphertext.
type Box struct{ aead cipher.AEAD }

// NewBox menerima kunci 32 byte dalam hex (ENC_KEY, 64 karakter).
func NewBox(hexKey string) (*Box, error) {
	key, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("ENC_KEY bukan hex valid: %w", err)
	}
	if len(key) != 32 {
		return nil, errors.New("ENC_KEY harus 32 byte")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

func (b *Box) Encrypt(plain []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, plain, nil), nil
}

func (b *Box) Decrypt(blob []byte) ([]byte, error) {
	ns := b.aead.NonceSize()
	if len(blob) < ns+b.aead.Overhead() {
		return nil, errors.New("ciphertext terlalu pendek")
	}
	return b.aead.Open(nil, blob[:ns], blob[ns:], nil)
}

// RandomBytes mengambil n byte acak kriptografis.
func RandomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand gagal: " + err.Error())
	}
	return b
}

// RandomToken menghasilkan token URL-safe dari n byte acak (untuk refresh/reset/klaim).
func RandomToken(n int) string { return base64.RawURLEncoding.EncodeToString(RandomBytes(n)) }

// HashToken = sha256 hex; token mentah tidak pernah disimpan.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ConstantTimeEqual membandingkan dua string tanpa kebocoran timing.
func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// RandomDigits menghasilkan angka acak n digit (OTP), seragam tanpa bias modulo.
func RandomDigits(n int) string {
	out := make([]byte, n)
	for i := range out {
		v, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			panic("crypto/rand gagal: " + err.Error())
		}
		out[i] = byte('0' + v.Int64())
	}
	return string(out)
}

// codeSet = tepat 32 simbol tanpa yang membingungkan (0/O, 1/I/L) → seragam tanpa bias karena 32 = 2^5.
const codeSet = "ABCDEFGHJKMNPQRSTUVWXYZ234567896"

// RandomCode menghasilkan n karakter acak dari codeSet (kode pemulihan, kode squad).
func RandomCode(n int) string {
	b := RandomBytes(n)
	out := make([]byte, n)
	for i := range b {
		out[i] = codeSet[int(b[i])&31]
	}
	return string(out)
}
