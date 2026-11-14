package service

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/repository/sqlcgen"
)

// Fraud = blacklist + sinyal risiko (docs/12 §4, 20 M12). Aturan sengaja sederhana dan dapat dijelaskan.
type Fraud struct{ s *Services }

func newFraud(s *Services) *Fraud { return &Fraud{s: s} }

// hashValue menyamakan bentuk nilai sebelum di-hash (blacklist tidak menyimpan PII mentah).
func hashValue(v string) string { return auth.HashToken(strings.ToLower(strings.TrimSpace(v))) }

func (f *Fraud) IsBlacklisted(ctx context.Context, kind, value string) bool {
	if value == "" {
		return false
	}
	ok, err := f.s.Q.IsBlacklisted(ctx, sqlcgen.IsBlacklistedParams{Kind: kind, ValueHash: hashValue(value)})
	if err != nil {
		f.s.Log.Error("cek blacklist gagal", "error", err)
		return false // gagal terbuka hanya untuk pemeriksaan ini; sinyal dicatat di log
	}
	return ok
}

// CheckAccess menolak akun/telepon/perangkat/IP yang diblokir.
func (f *Fraud) CheckAccess(ctx context.Context, userID, phone, ip, device string) error {
	for _, c := range []struct{ kind, val string }{{"user", userID}, {"phone", phone}, {"ip", ip}, {"device", device}} {
		if f.IsBlacklisted(ctx, c.kind, c.val) {
			return apperr.ForbiddenCode(apperr.CodeBlocked, "akses ditolak")
		}
	}
	return nil
}

// Signal mencatat sinyal risiko (score > 0 = makin mencurigakan).
func (f *Fraud) Signal(ctx context.Context, userID, eventID, kind string, score int, meta map[string]any) {
	var mb []byte
	if meta != nil {
		mb, _ = json.Marshal(meta)
	}
	err := f.s.Q.InsertRiskSignal(ctx, sqlcgen.InsertRiskSignalParams{
		ID: id.New(), UserID: strPtr(userID), EventID: strPtr(eventID), Kind: kind, Score: score, Meta: mb,
	})
	if err != nil {
		f.s.Log.Error("catat sinyal risiko gagal", "error", err)
	}
}

// Score = jumlah skor 1 jam terakhir.
func (f *Fraud) Score(ctx context.Context, userID string) int {
	n, err := f.s.Q.SumRiskScore(ctx, sqlcgen.SumRiskScoreParams{UserID: strPtr(userID), CreatedAt: f.s.now().Add(-time.Hour)})
	if err != nil {
		return 0
	}
	return int(n)
}

// Risk level untuk keputusan: <30 lolos, 30–69 tantangan, >=70 blok (docs/12 §4; nilai awal (T)).
const (
	RiskChallenge = 30
	RiskBlock     = 70
)

// Decision memutuskan tindakan atas skor.
func (f *Fraud) Decision(ctx context.Context, userID string) string {
	switch sc := f.Score(ctx, userID); {
	case sc >= RiskBlock:
		return "block"
	case sc >= RiskChallenge:
		return "challenge"
	default:
		return "allow"
	}
}

// RecordJoin mencatat sinyal multi-akun: banyak akun berbeda dari IP/perangkat yang sama masuk antrean event yang sama.
func (f *Fraud) RecordJoin(ctx context.Context, eventID, userID string) {
	m := reqmetaFrom(ctx)
	for _, c := range []struct{ kind, val string }{{"ip", m.IP}, {"device", m.DeviceID}} {
		if c.val == "" {
			continue
		}
		key := "fraud:join:" + eventID + ":" + c.kind + ":" + hashValue(c.val)
		pipe := f.s.Redis.TxPipeline()
		pipe.SAdd(ctx, key, userID)
		card := pipe.SCard(ctx, key)
		pipe.Expire(ctx, key, 10*time.Minute)
		if _, err := pipe.Exec(ctx); err != nil {
			return
		}
		if n := card.Val(); n > 5 {
			f.Signal(ctx, userID, eventID, "multi_account_"+c.kind, 20, map[string]any{"accounts": n})
		}
	}
}
