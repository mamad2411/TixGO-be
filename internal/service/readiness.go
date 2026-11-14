package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"time"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/auth"
)

// ReadinessService = Kit Siap Perang: jam server, cek kesiapan, latihan antrean tanpa menyentuh DB produksi (docs/17 F3, M22).
type ReadinessService struct{ s *Services }

func newReadinessService(s *Services) *ReadinessService { return &ReadinessService{s: s} }

type ClockView struct {
	ServerMS  int64  `json:"server_ms"`
	ServerISO string `json:"server_time"`
	OffsetMS  *int64 `json:"offset_ms,omitempty"` // server − klien (positif: jam klien tertinggal)
}

func (r *ReadinessService) Clock(clientMS *int64) ClockView {
	now := r.s.now()
	v := ClockView{ServerMS: now.UnixMilli(), ServerISO: now.UTC().Format(time.RFC3339Nano)}
	if clientMS != nil {
		o := now.UnixMilli() - *clientMS
		v.OffsetMS = &o
	}
	return v
}

type Check struct {
	Key    string `json:"key"`
	Status string `json:"status"` // ok | warn | fail
	Hint   string `json:"hint,omitempty"`
}

type Readiness struct {
	Ready  bool    `json:"ready"`
	Checks []Check `json:"checks"`
}

// Check menjalankan semua pemeriksaan ≤ 5 dtk. clientMS opsional untuk selisih jam.
func (r *ReadinessService) Check(ctx context.Context, userID string, clientMS *int64) (*Readiness, error) {
	cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	u, err := r.s.Q.GetUserByID(cctx, userID)
	if err != nil {
		return nil, apperr.NotFound("akun")
	}
	out := &Readiness{Ready: true}
	add := func(key, status, hint string) {
		out.Checks = append(out.Checks, Check{key, status, hint})
		if status == "fail" {
			out.Ready = false
		}
	}
	if u.EmailVerifiedAt != nil {
		add("email_verified", "ok", "")
	} else {
		add("email_verified", "fail", "Verifikasi email dulu agar bisa membeli.")
	}
	if u.PhoneVerifiedAt != nil {
		add("phone_verified", "ok", "")
	} else {
		add("phone_verified", "warn", "Nomor HP terverifikasi mempercepat pemulihan akun dan beberapa event mewajibkannya.")
	}
	if pend, err := r.s.Legal.Pending(cctx, r.s.Q, userID); err == nil && len(pend) > 0 {
		add("legal_consents", "fail", "Setujui ketentuan terbaru sebelum membeli.")
	} else {
		add("legal_consents", "ok", "")
	}
	if r.s.Gateway.Name() == "unconfigured" {
		add("payments", "fail", "Pembayaran belum tersedia.")
	} else {
		add("payments", "ok", "Siapkan e-wallet/aplikasi bank dan pastikan saldo cukup.")
	}
	if subs, err := r.s.Q.ListPushSubs(cctx, userID); err == nil && len(subs) > 0 {
		add("notifications", "ok", "")
	} else {
		add("notifications", "warn", "Aktifkan notifikasi agar tahu saat antrean giliranmu.")
	}
	if clientMS != nil {
		off := r.s.now().UnixMilli() - *clientMS
		if off < 0 {
			off = -off
		}
		switch {
		case off <= 1000:
			add("clock", "ok", "")
		case off <= 5000:
			add("clock", "warn", fmt.Sprintf("Jam perangkat selisih %.1f detik; pakai jam server di halaman antrean.", float64(off)/1000))
		default:
			add("clock", "warn", "Jam perangkat jauh berbeda; countdown memakai jam server.")
		}
	}
	return out, nil
}

// ---------- latihan antrean (Redis saja) ----------

const (
	pracTTL      = 30 * time.Minute
	pracHoldTTL  = 120 * time.Second
	pracQueueMin = 800
	pracQueueMax = 4000
	pracRate     = 120 // orang/detik (simulasi)
)

type PracticeStatus struct {
	State     string     `json:"state"` // idle | waiting | admitted | holding | done
	Position  int        `json:"position,omitempty"`
	AheadOf   int        `json:"queue_size,omitempty"`
	ETASec    int        `json:"eta_sec,omitempty"`
	HoldUntil *time.Time `json:"hold_until,omitempty"`
	Seats     []string   `json:"seats,omitempty"`
	TookSec   int        `json:"took_sec,omitempty"`
	Practice  bool       `json:"practice"`
}

type pracState struct {
	StartMS int64    `json:"start_ms"`
	Size    int      `json:"size"`
	HoldMS  int64    `json:"hold_ms,omitempty"`
	Seats   []string `json:"seats,omitempty"`
	DoneMS  int64    `json:"done_ms,omitempty"`
}

func pracKey(uid string) string { return "practice:" + uid }

func (r *ReadinessService) load(ctx context.Context, uid string) (*pracState, error) {
	b, err := r.s.Redis.Get(ctx, pracKey(uid)).Bytes()
	if err != nil {
		return nil, nil
	}
	var st pracState
	if json.Unmarshal(b, &st) != nil {
		return nil, nil
	}
	return &st, nil
}

func (r *ReadinessService) save(ctx context.Context, uid string, st *pracState) error {
	b, _ := json.Marshal(st)
	return r.s.Redis.Set(ctx, pracKey(uid), b, pracTTL).Err()
}

func (r *ReadinessService) status(st *pracState) PracticeStatus {
	out := PracticeStatus{Practice: true, State: "idle"}
	if st == nil {
		return out
	}
	now := r.s.now().UnixMilli()
	pos := st.Size - int(float64(now-st.StartMS)/1000*pracRate)
	switch {
	case st.DoneMS > 0:
		out.State, out.Seats, out.TookSec = "done", st.Seats, int((st.DoneMS-st.StartMS)/1000)
	case st.HoldMS > 0 && now < st.HoldMS:
		t := time.UnixMilli(st.HoldMS)
		out.State, out.Seats, out.HoldUntil = "holding", st.Seats, &t
	case st.HoldMS > 0:
		out.State = "admitted" // hold latihan habis → boleh coba lagi
	case pos > 0:
		out.State, out.Position, out.AheadOf, out.ETASec = "waiting", pos, st.Size, pos/pracRate+1
	default:
		out.State = "admitted"
	}
	return out
}

func (r *ReadinessService) PracticeStart(ctx context.Context, userID string) (*PracticeStatus, error) {
	if ok, ra, err := r.s.Limiter.Allow(ctx, "practice:"+userID, 20, time.Hour); err == nil && !ok {
		return nil, apperr.TooMany("terlalu sering memulai latihan").With("retry_after", int(ra.Seconds())+1)
	}
	st := &pracState{StartMS: r.s.now().UnixMilli(), Size: pracQueueMin + rand.Intn(pracQueueMax-pracQueueMin)}
	if err := r.save(ctx, userID, st); err != nil {
		return nil, err
	}
	s := r.status(st)
	return &s, nil
}

func (r *ReadinessService) PracticeStatus(ctx context.Context, userID string) (*PracticeStatus, error) {
	st, err := r.load(ctx, userID)
	if err != nil {
		return nil, err
	}
	s := r.status(st)
	return &s, nil
}

func (r *ReadinessService) PracticeHold(ctx context.Context, userID string, qty int) (*PracticeStatus, error) {
	st, _ := r.load(ctx, userID)
	if st == nil || r.status(st).State != "admitted" {
		return nil, apperr.Conflict(apperr.CodeNotAdmitted, "belum giliranmu di antrean latihan")
	}
	if qty < 1 || qty > 4 {
		return nil, apperr.Validation("qty 1–4").With("field", "qty")
	}
	st.HoldMS = r.s.now().Add(pracHoldTTL).UnixMilli()
	st.Seats = nil
	for i := 0; i < qty; i++ {
		st.Seats = append(st.Seats, fmt.Sprintf("LATIHAN-%c%d", 'A'+rand.Intn(6), 1+rand.Intn(20)))
	}
	if err := r.save(ctx, userID, st); err != nil {
		return nil, err
	}
	s := r.status(st)
	return &s, nil
}

func (r *ReadinessService) PracticePay(ctx context.Context, userID string) (*PracticeStatus, error) {
	st, _ := r.load(ctx, userID)
	if st == nil || r.status(st).State != "holding" {
		return nil, apperr.Conflict(apperr.CodeHoldExpired, "tidak ada hold latihan aktif")
	}
	st.DoneMS = r.s.now().UnixMilli()
	if err := r.save(ctx, userID, st); err != nil {
		return nil, err
	}
	s := r.status(st)
	return &s, nil
}

var _ = auth.RandomToken
