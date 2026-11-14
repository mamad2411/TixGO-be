package service

import (
	"context"
	"strings"
	"time"
	"tixgo/backend/internal/metrics"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/platform"
	"tixgo/backend/internal/repository/sqlcgen"
)

// GateService = verifikasi scan online, sinkron offline, manifest, statistik live (docs/06 M10, 11 §4).
type GateService struct{ s *Services }

func newGateService(s *Services) *GateService { return &GateService{s: s} }

const (
	ScanOK        = "OK"
	ScanDuplicate = "DUPLICATE"
	ScanInvalid   = "INVALID"
	ScanExpired   = "EXPIRED_CODE"
	ScanWrongEv   = "WRONG_EVENT"
	ScanVoid      = "VOID"
	ScanDupOff    = "DUPLICATE_OFFLINE"
)

type ScanReq struct {
	Payload   string     `json:"payload" binding:"required,max=100"`
	EventID   string     `json:"event_id" binding:"required,len=26"`
	GateID    string     `json:"gate_id" binding:"omitempty,len=26"`
	ScanID    string     `json:"scan_id" binding:"omitempty,min=8,max=64"` // dibuat perangkat → idempoten
	ScannedAt *time.Time `json:"scanned_at"`
}

type ScanResult struct {
	Result    string     `json:"result"`
	TicketID  string     `json:"ticket_id,omitempty"`
	SeatLabel *string    `json:"seat_label,omitempty"`
	Attendee  *string    `json:"attendee_name,omitempty"`
	TierID    string     `json:"tier_id,omitempty"`
	FirstScan *time.Time `json:"first_scan_at,omitempty"`
	FirstGate *string    `json:"first_gate_id,omitempty"`
	ScanID    string     `json:"scan_id,omitempty"`
}

// authorize: staf yang ditugaskan ke event, atau anggota organizer pemilik event.
func (g *GateService) authorize(ctx context.Context, userID, eventID string) (sqlcgen.Event, error) {
	ev, err := g.s.Q.GetEvent(ctx, eventID)
	if err != nil {
		return ev, apperr.NotFound("event")
	}
	ok, err := g.s.Q.IsGateStaffFor(ctx, sqlcgen.IsGateStaffForParams{EventID: eventID, UserID: userID})
	if err != nil {
		return ev, err
	}
	if ok {
		return ev, nil
	}
	sc, _, err := g.s.Organizer.Membership(ctx, userID)
	if err != nil {
		return ev, err
	}
	if sc != nil && sc.OrganizerID == ev.OrganizerID {
		return ev, nil
	}
	return ev, apperr.NotFound("event") // 404: tidak membocorkan keberadaan event
}

func splitPayload(p string) (ticketID, code string, ok bool) {
	i := strings.IndexByte(p, '.')
	if i != 26 || len(p) < 28 {
		return "", "", false
	}
	return p[:i], p[i+1:], true
}

// Scan memverifikasi satu QR dan (bila OK) menandai tiket terpakai secara atomik.
func (g *GateService) Scan(ctx context.Context, p *auth.Principal, r ScanReq) (*ScanResult, error) {
	ev, err := g.authorize(ctx, p.UserID, r.EventID)
	if err != nil {
		return nil, err
	}
	// idempoten: scan_id yang sama mengembalikan hasil yang sama
	if r.ScanID != "" {
		if old, err := g.s.Q.GetGateScanByScanID(ctx, &r.ScanID); err == nil {
			return &ScanResult{Result: old.Result, TicketID: deref(old.TicketID), ScanID: r.ScanID}, nil
		}
	}
	now := g.s.now()
	at := now
	if r.ScannedAt != nil && r.ScannedAt.Before(now.Add(time.Minute)) {
		at = *r.ScannedAt
	}
	res, tk, err := g.evaluate(ctx, ev, r.Payload, at, now)
	if err != nil {
		return nil, err
	}
	res.ScanID = r.ScanID
	err = g.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		if res.Result == ScanOK {
			n, err := q.ClaimTicketEntry(ctx, sqlcgen.ClaimTicketEntryParams{ID: tk.ID, UsedAt: &at})
			if err != nil {
				return err
			}
			if n == 0 { // kalah balapan dari scan lain
				res.Result = ScanDuplicate
				g.fillFirst(ctx, q, res, tk.ID)
			}
		}
		return g.record(ctx, q, p.UserID, ev.ID, r, tk, res, at)
	})
	if err != nil {
		if platform.IsUniqueViolation(err) && r.ScanID != "" { // balapan scan_id
			if old, e2 := g.s.Q.GetGateScanByScanID(ctx, &r.ScanID); e2 == nil {
				return &ScanResult{Result: old.Result, TicketID: deref(old.TicketID), ScanID: r.ScanID}, nil
			}
		}
		return nil, err
	}
	metrics.Scans.WithLabelValues(res.Result).Inc()
	if res.Result == ScanOK {
		g.s.Passport.AwardForTicket(ctx, tk.ID)
		g.s.publish(ctx, "event."+ev.ID+".gate", "scan.ok", map[string]any{"ticket_id": tk.ID, "gate_id": r.GateID})
	}
	return res, nil
}

func (g *GateService) record(ctx context.Context, q *sqlcgen.Queries, staff, eventID string, r ScanReq, tk *sqlcgen.Ticket, res *ScanResult, at time.Time) error {
	var tid, gid, sid, detail *string
	if tk != nil {
		tid = &tk.ID
	}
	if r.GateID != "" {
		gid = &r.GateID
	}
	if r.ScanID != "" {
		sid = &r.ScanID
	}
	if res.Result != ScanOK {
		d := res.Result
		detail = &d
	}
	_, err := q.InsertGateScan(ctx, sqlcgen.InsertGateScanParams{ID: id.New(), ScanID: sid, TicketID: tid, EventID: eventID, GateID: gid,
		StaffUserID: &staff, Result: res.Result, Detail: detail, ScannedAt: at})
	return err
}

func (g *GateService) fillFirst(ctx context.Context, q *sqlcgen.Queries, res *ScanResult, ticketID string) {
	if first, err := q.OKScanForTicket(ctx, &ticketID); err == nil {
		t := first.ScannedAt
		res.FirstScan, res.FirstGate = &t, first.GateID
	}
}

// evaluate menentukan hasil tanpa efek samping.
func (g *GateService) evaluate(ctx context.Context, ev sqlcgen.Event, payload string, at, now time.Time) (*ScanResult, *sqlcgen.Ticket, error) {
	tid, code, ok := splitPayload(payload)
	if !ok {
		return &ScanResult{Result: ScanInvalid}, nil, nil
	}
	tk, err := g.s.Q.GetTicket(ctx, tid)
	if err != nil {
		if platform.IsNoRows(err) {
			return &ScanResult{Result: ScanInvalid}, nil, nil
		}
		return nil, nil, err
	}
	res := &ScanResult{TicketID: tk.ID, SeatLabel: tk.SeatLabel, Attendee: tk.AttendeeName, TierID: tk.TierID}
	if tk.EventID != ev.ID {
		res.Result, res.TicketID = ScanWrongEv, ""
		res.SeatLabel, res.Attendee, res.TierID = nil, nil, ""
		return res, nil, nil
	}
	secret, err := g.s.Ticket.secretOf(tk)
	if err != nil {
		return nil, nil, err
	}
	step := uint(g.s.Cfg.TicketQRStepSec)
	if !auth.TOTPValidate(code, secret, at, step, 1) {
		res.Result = ScanExpired
		return res, &tk, nil
	}
	switch tk.Status {
	case "valid":
		res.Result = ScanOK
	case "used":
		res.Result = ScanDuplicate
		g.fillFirst(ctx, g.s.Q, res, tk.ID)
	default:
		res.Result = ScanVoid
	}
	return res, &tk, nil
}

// ---------- offline ----------

type ManifestTicket struct {
	ID       string  `json:"id"`
	Status   string  `json:"status"`
	Seat     *string `json:"seat_label,omitempty"`
	Attendee *string `json:"attendee_name,omitempty"`
	TierID   string  `json:"tier_id"`
	Secret   string  `json:"secret"`
}

type Manifest struct {
	EventID   string           `json:"event_id"`
	StepSec   int              `json:"step_sec"`
	IssuedAt  time.Time        `json:"issued_at"`
	ExpiresAt time.Time        `json:"expires_at"`
	Tickets   []ManifestTicket `json:"tickets"`
}

// Manifest = paket data offline (secret TOTP per tiket) hanya untuk staf yang ditugaskan; diaudit; berlaku sampai acara usai.
func (g *GateService) Manifest(ctx context.Context, p *auth.Principal, eventID string) (*Manifest, error) {
	ev, err := g.authorize(ctx, p.UserID, eventID)
	if err != nil {
		return nil, err
	}
	rows, err := g.s.Q.GateManifest(ctx, ev.ID)
	if err != nil {
		return nil, err
	}
	now := g.s.now()
	end := ev.StartsAt.Add(24 * time.Hour)
	if ev.EndsAt != nil {
		end = ev.EndsAt.Add(6 * time.Hour)
	}
	m := &Manifest{EventID: ev.ID, StepSec: g.s.Cfg.TicketQRStepSec, IssuedAt: now, ExpiresAt: end, Tickets: make([]ManifestTicket, 0, len(rows))}
	for _, r := range rows {
		sec, err := g.s.Box.Decrypt(r.SecretEnc)
		if err != nil {
			return nil, err
		}
		m.Tickets = append(m.Tickets, ManifestTicket{ID: r.ID, Status: r.Status, Seat: r.SeatLabel, Attendee: r.AttendeeName, TierID: r.TierID, Secret: string(sec)})
	}
	_ = g.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		return g.s.Audit.Log(ctx, q, p, "gate.manifest", "event", ev.ID, nil, map[string]any{"tickets": len(m.Tickets)})
	})
	return m, nil
}

type OfflineScan struct {
	ScanID    string    `json:"scan_id" binding:"required,min=8,max=64"`
	TicketID  string    `json:"ticket_id" binding:"required,len=26"`
	GateID    string    `json:"gate_id" binding:"omitempty,len=26"`
	ScannedAt time.Time `json:"scanned_at" binding:"required"`
	Result    string    `json:"result" binding:"omitempty,max=32"` // hasil lokal perangkat (informasi)
}

type SyncResult struct {
	Accepted  int `json:"accepted"`
	Duplicate int `json:"duplicate"` // scan ulang (scan_id sama)
	Conflicts int `json:"conflicts"` // DUPLICATE_OFFLINE
	Rejected  int `json:"rejected"`
}

// Sync menerima antrean scan offline. Aturan (11 §4): scan dengan scanned_at TERAWAL untuk tiket yang sama menang OK;
// lainnya DUPLICATE_OFFLINE. Idempoten per scan_id.
func (g *GateService) Sync(ctx context.Context, p *auth.Principal, eventID string, scans []OfflineScan) (*SyncResult, error) {
	ev, err := g.authorize(ctx, p.UserID, eventID)
	if err != nil {
		return nil, err
	}
	out := &SyncResult{}
	now := g.s.now()
	for _, sc := range scans {
		if _, err := g.s.Q.GetGateScanByScanID(ctx, &sc.ScanID); err == nil {
			out.Duplicate++
			continue
		}
		at := sc.ScannedAt
		if at.After(now.Add(5 * time.Minute)) {
			at = now
		}
		var verdict string
		err := g.s.InTx(ctx, func(q *sqlcgen.Queries) error {
			tk, err := q.GetTicketForUpdate(ctx, sc.TicketID)
			if err != nil || tk.EventID != ev.ID {
				verdict = ScanInvalid
				return g.record(ctx, q, p.UserID, ev.ID, ScanReq{ScanID: sc.ScanID, GateID: sc.GateID}, nil, &ScanResult{Result: ScanInvalid}, at)
			}
			req := ScanReq{ScanID: sc.ScanID, GateID: sc.GateID}
			switch tk.Status {
			case "valid":
				verdict = ScanOK
				if _, err := q.ClaimTicketEntry(ctx, sqlcgen.ClaimTicketEntryParams{ID: tk.ID, UsedAt: &at}); err != nil {
					return err
				}
			case "used":
				first, err := q.OKScanForTicket(ctx, &tk.ID)
				switch {
				case err != nil: // used tanpa catatan OK (data lama) → catat sebagai duplikat
					verdict = ScanDupOff
				case at.Before(first.ScannedAt): // scan ini lebih awal → ia yang berhak OK
					d := "digantikan scan offline yang lebih awal"
					if err := q.DemoteScan(ctx, sqlcgen.DemoteScanParams{ID: first.ID, Detail: &d}); err != nil {
						return err
					}
					if err := q.SetTicketUsedAt(ctx, sqlcgen.SetTicketUsedAtParams{ID: tk.ID, UsedAt: &at}); err != nil {
						return err
					}
					verdict = ScanOK
				default:
					verdict = ScanDupOff
				}
			default:
				verdict = ScanVoid
			}
			return g.record(ctx, q, p.UserID, ev.ID, req, &tk, &ScanResult{Result: verdict}, at)
		})
		if err != nil {
			if platform.IsUniqueViolation(err) {
				out.Duplicate++
				continue
			}
			return nil, err
		}
		switch verdict {
		case ScanOK:
			g.s.Passport.AwardForTicket(ctx, sc.TicketID)
			out.Accepted++
		case ScanDupOff:
			out.Conflicts++
		default:
			out.Rejected++
		}
	}
	return out, nil
}

// ---------- ringkasan live ----------

type GateSummary struct {
	Issued      int            `json:"issued"`
	Entered     int            `json:"entered"`
	Remaining   int            `json:"remaining"`
	ByResult    map[string]int `json:"by_result"`
	ByGate      map[string]int `json:"ok_by_gate"`
	RecentFails []ScanResult   `json:"recent_failures"`
}

func (g *GateService) Summary(ctx context.Context, p *auth.Principal, eventID string) (*GateSummary, error) {
	ev, err := g.authorize(ctx, p.UserID, eventID)
	if err != nil {
		return nil, err
	}
	c, err := g.s.Q.EventTicketCounts(ctx, ev.ID)
	if err != nil {
		return nil, err
	}
	rows, err := g.s.Q.GateStats(ctx, ev.ID)
	if err != nil {
		return nil, err
	}
	out := &GateSummary{Issued: int(c.Issued), Entered: int(c.Used), Remaining: int(c.Issued - c.Used), ByResult: map[string]int{}, ByGate: map[string]int{}, RecentFails: []ScanResult{}}
	for _, r := range rows {
		out.ByResult[r.Result] += int(r.N)
		if r.Result == ScanOK {
			out.ByGate[r.GateID] += int(r.N)
		}
	}
	fails, err := g.s.Q.RecentFailedScans(ctx, sqlcgen.RecentFailedScansParams{EventID: ev.ID, Limit: 20})
	if err != nil {
		return nil, err
	}
	for _, f := range fails {
		out.RecentFails = append(out.RecentFails, ScanResult{Result: f.Result, TicketID: deref(f.TicketID)})
	}
	return out, nil
}
