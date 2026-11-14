package service

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"strconv"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/repository/sqlcgen"
)

// SeatAssist = "Best available" + anti-orphan (docs/10 §5) dan Prioritas Pilihan (M23).
type SeatAssist struct{ s *Services }

func newSeatAssist(s *Services) *SeatAssist { return &SeatAssist{s: s} }

type SeatSuggestion struct {
	SeatIDs    []string `json:"seat_ids"`
	Labels     []string `json:"labels"`
	TierID     string   `json:"tier_id"`
	Contiguous bool     `json:"contiguous"` // satu baris, nomor berurutan
	Orphans    int      `json:"orphans"`    // kursi tunggal tersisa akibat pilihan ini
}

type assistSeat struct {
	id, label  string
	no         float64
	x          float64
	accessible bool
	free       bool
}

func seatNum(s string) float64 {
	if n, err := strconv.Atoi(s); err == nil {
		return float64(n)
	}
	return math.MaxFloat64
}

// Suggest memilih n kursi terbaik pada satu tier. Aturan skor (kecil = baik):
//  1. harus satu baris & berurutan bila ada (kontigu);
//  2. tidak meninggalkan kursi tunggal (orphan) di kiri/kanan — penalti besar;
//  3. baris lebih depan (urutan label) lebih baik; lalu dekat tengah baris.
//
// Jika tak ada jendela berurutan, kembalikan n kursi bebas terbaik (non-kontigu) dengan Contiguous=false.
func (a *SeatAssist) Suggest(ctx context.Context, ev sqlcgen.Event, tierID string, n int, accessible, allowOrphan bool) (*SeatSuggestion, error) {
	if n < 1 || n > 50 {
		return nil, apperr.Validation("qty 1–50").With("field", "qty")
	}
	rows, err := a.s.Q.SeatsForAssist(ctx, sqlcgen.SeatsForAssistParams{EventID: ev.ID, TierID: tierID})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, apperr.Unprocessable(apperr.CodeValidation, "tier ini tidak memiliki kursi bernomor")
	}
	type key struct{ sec, row string }
	groups := map[key][]assistSeat{}
	var order []key
	for _, r := range rows {
		k := key{r.SectionID, r.RowLabel}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		x := float64(0)
		if r.X != nil {
			x = *r.X
		}
		groups[k] = append(groups[k], assistSeat{id: r.ID, label: r.Label, no: seatNum(r.SeatNo), x: x, accessible: r.Accessible, free: r.Status == "available"})
	}
	type cand struct {
		ids, labels []string
		score       float64
		orphans     int
	}
	var best *cand
	for rowIdx, k := range order {
		seats := groups[k]
		sort.SliceStable(seats, func(i, j int) bool { return seats[i].no < seats[j].no })
		mid := (seats[0].no + seats[len(seats)-1].no) / 2
		for i := 0; i+n <= len(seats); i++ {
			ok := true
			for j := i; j < i+n; j++ {
				if !seats[j].free || (accessible && !seats[j].accessible) {
					ok = false
					break
				}
				if j > i && seats[j].no-seats[j-1].no != 1 { // kursi berurutan secara nomor
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
			orphans := 0
			if i > 0 && seats[i-1].free && (i-2 < 0 || !seats[i-2].free || seats[i-1].no-seats[i-2].no != 1) {
				orphans++
			}
			if e := i + n; e < len(seats) && seats[e].free && (e+1 >= len(seats) || !seats[e+1].free || seats[e+1].no-seats[e].no != 1) {
				orphans++
			}
			center := (seats[i].no + seats[i+n-1].no) / 2
			score := float64(rowIdx)*1.0 + math.Abs(center-mid)*0.05 + float64(orphans)*100
			if best == nil || score < best.score {
				c := &cand{score: score, orphans: orphans}
				for j := i; j < i+n; j++ {
					c.ids = append(c.ids, seats[j].id)
					c.labels = append(c.labels, seats[j].label)
				}
				best = c
			}
		}
	}
	if best != nil && (best.orphans == 0 || allowOrphan) {
		return &SeatSuggestion{SeatIDs: best.ids, Labels: best.labels, TierID: tierID, Contiguous: true, Orphans: best.orphans}, nil
	}
	if best != nil { // satu-satunya jendela meninggalkan orphan → tetap tawarkan, tetapi diberi tahu
		return &SeatSuggestion{SeatIDs: best.ids, Labels: best.labels, TierID: tierID, Contiguous: true, Orphans: best.orphans}, nil
	}
	// non-kontigu: ambil n kursi bebas terdepan
	var ids, labels []string
	for _, k := range order {
		for _, s := range groups[k] {
			if s.free && (!accessible || s.accessible) {
				ids, labels = append(ids, s.id), append(labels, s.label)
				if len(ids) == n {
					return &SeatSuggestion{SeatIDs: ids, Labels: labels, TierID: tierID}, nil
				}
			}
		}
	}
	return nil, apperr.Conflict(apperr.CodeSeatTaken, "kursi bebas tidak cukup untuk jumlah ini")
}

// ---------- prioritas pilihan (M23) ----------

type Preferences struct {
	Tiers       []string `json:"tiers" binding:"required,min=1,max=6,dive,len=26"` // urutan prioritas
	Accessible  bool     `json:"accessible"`
	AllowOrphan bool     `json:"allow_orphan"`
}

func (a *SeatAssist) SetPrefs(ctx context.Context, userID, eventID string, p Preferences) error {
	tiers, err := a.s.Q.ListTiers(ctx, eventID)
	if err != nil {
		return err
	}
	valid := map[string]bool{}
	for _, t := range tiers {
		valid[t.ID] = true
	}
	for _, t := range p.Tiers {
		if !valid[t] {
			return apperr.Validation("tier tidak ada pada event ini").With("field", "tiers")
		}
	}
	b, _ := json.Marshal(p)
	return a.s.Q.UpsertSeatPrefs(ctx, sqlcgen.UpsertSeatPrefsParams{UserID: userID, EventID: eventID, Prefs: b})
}

func (a *SeatAssist) GetPrefs(ctx context.Context, userID, eventID string) (*Preferences, error) {
	raw, err := a.s.Q.GetSeatPrefs(ctx, sqlcgen.GetSeatPrefsParams{UserID: userID, EventID: eventID})
	if err != nil {
		return &Preferences{Tiers: []string{}}, nil
	}
	var p Preferences
	if json.Unmarshal(raw, &p) != nil {
		return &Preferences{Tiers: []string{}}, nil
	}
	return &p, nil
}

// AutoHold: satu permintaan — coba tier sesuai urutan prioritas; kursi bernomor → best-available, GA → qty. Berhenti pada hold pertama yang berhasil.
func (a *SeatAssist) AutoHold(ctx context.Context, userID, eventKey string, qty int, admissionToken string) (*HoldView, error) {
	ev, err := a.s.Catalog.lookup(ctx, eventKey)
	if err != nil {
		return nil, apperr.NotFound("event")
	}
	prefs, _ := a.GetPrefs(ctx, userID, ev.ID)
	tiers := prefs.Tiers
	if len(tiers) == 0 {
		all, err := a.s.Q.ListTiers(ctx, ev.ID)
		if err != nil {
			return nil, err
		}
		for _, t := range all {
			tiers = append(tiers, t.ID)
		}
	}
	var last error = apperr.Conflict(apperr.CodeSeatTaken, "tidak ada tier yang tersedia")
	for _, tid := range tiers {
		for attempt := 0; attempt < 3; attempt++ { // kalah balapan → hitung ulang saran
			req := HoldRequest{TierID: tid, Qty: qty}
			if ev.Mode == "seated" {
				sg, err := a.Suggest(ctx, ev, tid, qty, prefs.Accessible, prefs.AllowOrphan)
				if err != nil {
					last = err
					break
				}
				req = HoldRequest{SeatIDs: sg.SeatIDs}
			}
			hv, err := a.s.Hold.Create(ctx, userID, eventKey, req, admissionToken)
			if err == nil {
				return hv, nil
			}
			last = err
			ae, ok := err.(*apperr.Error)
			if !ok {
				return nil, err
			}
			if ae.Code == apperr.CodeSoldOut {
				break // tier habis → coba tier berikutnya
			}
			if ae.Code != apperr.CodeSeatTaken {
				return nil, err // galat lain (antrean, jeda, limit) berlaku untuk semua tier
			}
		}
	}
	return nil, last
}
