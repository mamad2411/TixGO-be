package auth

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret = 20 byte acak, base32 (untuk tiket dan MFA).
func NewTOTPSecret() string {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand gagal: " + err.Error())
	}
	return b32.EncodeToString(b)
}

func totpOpts(period uint) totp.ValidateOpts {
	return totp.ValidateOpts{Period: period, Skew: 0, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1}
}

// TOTPCode menghitung kode untuk waktu t dengan langkah period detik (docs/11 §4).
func TOTPCode(secret string, t time.Time, period uint) (string, error) {
	return totp.GenerateCodeCustom(secret, t, totpOpts(period))
}

// TOTPValidate menerima kode pada langkah saat ini ± skew langkah.
func TOTPValidate(code, secret string, t time.Time, period uint, skew int) bool {
	for d := -skew; d <= skew; d++ {
		c, err := TOTPCode(secret, t.Add(time.Duration(d)*time.Duration(period)*time.Second), period)
		if err != nil {
			return false
		}
		if ConstantTimeEqual(c, code) {
			return true
		}
	}
	return false
}

// TOTPProvisioningURI untuk MFA aplikasi authenticator.
func TOTPProvisioningURI(issuer, account, secret string) string {
	return fmt.Sprintf("otpauth://totp/%s:%s?secret=%s&issuer=%s&algorithm=SHA1&digits=6&period=30", issuer, account, secret, issuer)
}
