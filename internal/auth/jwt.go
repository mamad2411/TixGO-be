package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"tixgo/backend/internal/clock"
)

// Principal = identitas terautentikasi yang dibawa request.
type Principal struct {
	UserID        string
	Role          string
	SessionID     string // family_id refresh token; dipakai untuk pencabutan sesi
	MFAPassed     bool
	OrganizerID   string // diisi middleware bila user adalah anggota organizer
	OrganizerRole string // owner|member|gate_staff
}

type accessClaims struct {
	Role string `json:"role"`
	SID  string `json:"sid"`
	MFA  bool   `json:"mfa,omitempty"`
	jwt.RegisteredClaims
}

type admissionClaims struct {
	Event string `json:"evt"`
	jwt.RegisteredClaims
}

var (
	ErrTokenExpired = errors.New("token kedaluwarsa")
	ErrTokenInvalid = errors.New("token tidak valid")
)

// Tokens menerbitkan dan memverifikasi JWT (HS256) untuk akses dan admission antrean.
type Tokens struct {
	secret    []byte
	accessTTL time.Duration
	clk       clock.Clock
}

func NewTokens(secret string, accessTTL time.Duration, clk clock.Clock) *Tokens {
	return &Tokens{secret: []byte(secret), accessTTL: accessTTL, clk: clk}
}

func (t *Tokens) AccessTTL() time.Duration { return t.accessTTL }

func (t *Tokens) IssueAccess(userID, role, sessionID string, mfa bool) (string, time.Time, error) {
	now := t.clk.Now()
	exp := now.Add(t.accessTTL)
	claims := accessClaims{
		Role: role, SID: sessionID, MFA: mfa,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: userID, Issuer: "tixgo", Audience: jwt.ClaimStrings{"access"},
			IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(exp),
		},
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(t.secret)
	return s, exp, err
}

func (t *Tokens) ParseAccess(token string) (*Principal, error) {
	var c accessClaims
	if err := t.parse(token, "access", &c); err != nil {
		return nil, err
	}
	return &Principal{UserID: c.Subject, Role: c.Role, SessionID: c.SID, MFAPassed: c.MFA}, nil
}

// IssueAdmission menerbitkan token admission antrean (docs/04 §3): berlaku sebatas jendela beli.
func (t *Tokens) IssueAdmission(userID, eventID string, ttl time.Duration) (string, time.Time, error) {
	now := t.clk.Now()
	exp := now.Add(ttl)
	claims := admissionClaims{
		Event: eventID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: userID, Issuer: "tixgo", Audience: jwt.ClaimStrings{"admission"},
			IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(exp),
		},
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(t.secret)
	return s, exp, err
}

// ParseAdmission mengembalikan (userID, eventID).
func (t *Tokens) ParseAdmission(token string) (string, string, error) {
	var c admissionClaims
	if err := t.parse(token, "admission", &c); err != nil {
		return "", "", err
	}
	return c.Subject, c.Event, nil
}

func (t *Tokens) parse(token, aud string, dst jwt.Claims) error {
	_, err := jwt.ParseWithClaims(token, dst, func(tk *jwt.Token) (any, error) {
		return t.secret, nil
	},
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithAudience(aud),
		jwt.WithIssuer("tixgo"),
		jwt.WithTimeFunc(t.clk.Now),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return ErrTokenExpired
		}
		return fmt.Errorf("%w: %v", ErrTokenInvalid, err)
	}
	return nil
}
