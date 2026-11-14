package service

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"regexp"
	"strconv"
	"sync"
	"time"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/metrics"
	"tixgo/backend/internal/repository/sqlcgen"
)

// OpsService = control room onsale, maintenance, pengumuman, feature flag, status layanan (docs/20 M39, M44, M30).
type OpsService struct {
	s *Services

	mu      sync.Mutex
	mCache  *MaintenanceInfo
	mAt     time.Time
	flagsAt time.Time
	flags   map[string]sqlcgen.FeatureFlag
}

func newOpsService(s *Services) *OpsService { return &OpsService{s: s} }

var flagKeyRe = regexp.MustCompile(`^[a-z0-9_.]{3,64}$`)

const (
	maintKey    = "ops:maintenance"
	maintTTL    = 1500 * time.Millisecond // cache lokal: efek ≤ ~2 dtk lintas instance
	flagsTTL    = 5 * time.Second
	minAdmit    = 1
	maxAdmitCap = 5000
)

// ---------- jeda penjualan ----------

// SetSalesPaused = kill switch per event. Admin (any) atau owner organizer pemilik event. Efek instan di DB; cache ketersediaan dibuang.
func (o *OpsService) SetSalesPaused(ctx context.Context, p *auth.Principal, sc *OrgScope, eventID string, paused bool, reason string) (*EventView, error) {
	if paused && len([]rune(reason)) < 3 {
		return nil, apperr.Validation("alasan jeda wajib diisi").With("field", "reason")
	}
	var out sqlcgen.Event
	err := o.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		ev, err := q.GetEventForUpdate(ctx, eventID)
		if err != nil || (sc != nil && ev.OrganizerID != sc.OrganizerID) {
			return apperr.NotFound("event")
		}
		if ev.Status != stPublished && ev.Status != stPaused {
			return apperr.Conflict(apperr.CodeInvalidState, "hanya event terbit yang bisa dijeda")
		}
		var r *string
		if paused {
			r = &reason
		}
		if out, err = q.SetSalesPaused(ctx, sqlcgen.SetSalesPausedParams{ID: eventID, Paused: paused, Reason: r}); err != nil {
			return err
		}
		action := "sales.resume"
		if paused {
			action = "sales.pause"
		}
		if err := o.s.Audit.Log(ctx, q, p, action, "event", eventID, map[string]any{"paused": ev.SalesPaused}, map[string]any{"paused": paused, "reason": reason}); err != nil {
			return err
		}
		return emit(ctx, q, "event."+eventID, "sales.paused", map[string]any{"paused": paused, "reason": reason})
	})
	if err != nil {
		return nil, err
	}
	o.s.Catalog.InvalidateAvailability(ctx, eventID)
	return o.s.Event.view(ctx, o.s.Q, out)
}

// SetAdmitRate mengubah kecepatan admit saat berjalan (batas aman 1..5000), teraudit.
func (o *OpsService) SetAdmitRate(ctx context.Context, p *auth.Principal, eventID string, rate int) error {
	if rate < minAdmit || rate > maxAdmitCap {
		return apperr.Validation("admit_rate harus 1–"+strconv.Itoa(maxAdmitCap)).With("field", "rate")
	}
	return o.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		ev, err := q.GetEventForUpdate(ctx, eventID)
		if err != nil {
			return apperr.NotFound("event")
		}
		if _, err := q.SetEventAdmitRate(ctx, sqlcgen.SetEventAdmitRateParams{ID: eventID, Rate: rate}); err != nil {
			return err
		}
		return o.s.Audit.Log(ctx, q, p, "queue.admit_rate", "event", eventID, map[string]any{"rate": ev.AdmitRate}, map[string]any{"rate": rate})
	})
}

// ---------- dasbor live ----------

type ControlRoom struct {
	EventID       string            `json:"event_id"`
	Title         string            `json:"title"`
	Status        string            `json:"status"`
	SalesPaused   bool              `json:"sales_paused"`
	AdmitRate     int               `json:"admit_rate"`
	Queue         map[string]int64  `json:"queue"`
	ActiveHolds   int               `json:"active_holds"`
	PendingOrders int               `json:"pending_orders"`
	PaidOrders    int               `json:"paid_orders"`
	Tickets       int               `json:"tickets_issued"`
	HTTP          metrics.HTTPStats `json:"http"`
	Gateway       string            `json:"gateway"`
	OpenRecon     int               `json:"open_recon"`
	PendingOutbox int               `json:"pending_outbox"`
	Maintenance   *MaintenanceInfo  `json:"maintenance,omitempty"`
}

func (o *OpsService) ControlRoom(ctx context.Context, eventID string) (*ControlRoom, error) {
	ev, err := o.s.Q.GetEvent(ctx, eventID)
	if err != nil {
		return nil, apperr.NotFound("event")
	}
	c, err := o.s.Q.EventLiveCounts(ctx, eventID)
	if err != nil {
		return nil, err
	}
	qs, _ := o.s.Queue.Stats(ctx, eventID)
	recon, _ := o.s.Q.OpenReconCount(ctx)
	ob, _ := o.s.Q.PendingOutboxCount(ctx)
	gw := "ok"
	if o.s.Gateway.Name() == "unconfigured" {
		gw = "unconfigured"
	}
	return &ControlRoom{EventID: ev.ID, Title: ev.Title, Status: ev.Status, SalesPaused: ev.SalesPaused, AdmitRate: ev.AdmitRate, Queue: qs,
		ActiveHolds: int(c.ActiveHolds), PendingOrders: int(c.PendingOrders), PaidOrders: int(c.PaidOrders), Tickets: int(c.TicketsIssued),
		HTTP: metrics.HTTPSnapshot(), Gateway: gw, OpenRecon: int(recon), PendingOutbox: int(ob), Maintenance: o.Maintenance(ctx)}, nil
}

// ---------- maintenance ----------

type MaintenanceInfo struct {
	Message string    `json:"message"`
	Since   time.Time `json:"since"`
	ETA     *string   `json:"eta,omitempty"`
}

// Maintenance mengembalikan info bila aktif. Dibaca tiap request → cache lokal singkat; kegagalan Redis = dianggap tidak maintenance (fail-open: tidak mengunci pembeli karena Redis goyang).
func (o *OpsService) Maintenance(ctx context.Context) *MaintenanceInfo {
	o.mu.Lock()
	if time.Since(o.mAt) < maintTTL {
		m := o.mCache
		o.mu.Unlock()
		return m
	}
	o.mu.Unlock()
	var m *MaintenanceInfo
	if b, err := o.s.Redis.Get(ctx, maintKey).Bytes(); err == nil {
		var mi MaintenanceInfo
		if json.Unmarshal(b, &mi) == nil {
			m = &mi
		}
	}
	o.mu.Lock()
	o.mCache, o.mAt = m, time.Now()
	o.mu.Unlock()
	return m
}

func (o *OpsService) SetMaintenance(ctx context.Context, p *auth.Principal, on bool, message string, eta string) error {
	if on {
		if len([]rune(message)) < 5 {
			return apperr.Validation("pesan maintenance wajib diisi").With("field", "message")
		}
		mi := MaintenanceInfo{Message: message, Since: o.s.now()}
		if eta != "" {
			mi.ETA = &eta
		}
		b, _ := json.Marshal(mi)
		if err := o.s.Redis.Set(ctx, maintKey, b, 0).Err(); err != nil {
			return err
		}
	} else if err := o.s.Redis.Del(ctx, maintKey).Err(); err != nil {
		return err
	}
	o.mu.Lock()
	o.mAt = time.Time{}
	o.mu.Unlock()
	return o.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		return o.s.Audit.Log(ctx, q, p, "ops.maintenance", "system", "maintenance", nil, map[string]any{"on": on, "message": message})
	})
}

// ---------- pengumuman ----------

type AnnouncementInput struct {
	Level    string     `json:"level" binding:"required,oneof=info warning incident maintenance"`
	BodyID   string     `json:"body_id" binding:"required,min=3,max=500"`
	BodyEN   string     `json:"body_en" binding:"omitempty,max=500"`
	StartsAt *time.Time `json:"starts_at"`
	EndsAt   *time.Time `json:"ends_at"`
}

func (o *OpsService) CreateAnnouncement(ctx context.Context, p *auth.Principal, in AnnouncementInput) (*sqlcgen.Announcement, error) {
	start := o.s.now()
	if in.StartsAt != nil {
		start = *in.StartsAt
	}
	if in.EndsAt != nil && !in.EndsAt.After(start) {
		return nil, apperr.Validation("ends_at harus setelah starts_at").With("field", "ends_at")
	}
	var out sqlcgen.Announcement
	err := o.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		out, err = q.InsertAnnouncement(ctx, sqlcgen.InsertAnnouncementParams{ID: id.New(), Level: in.Level, BodyID: in.BodyID, BodyEn: strPtr(in.BodyEN), StartsAt: start, EndsAt: in.EndsAt, CreatedBy: &p.UserID})
		if err != nil {
			return err
		}
		return o.s.Audit.Log(ctx, q, p, "announcement.create", "announcement", out.ID, nil, in)
	})
	return &out, err
}

func (o *OpsService) EndAnnouncement(ctx context.Context, p *auth.Principal, annID string) error {
	return o.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		n, err := q.EndAnnouncement(ctx, sqlcgen.EndAnnouncementParams{ID: annID, Now: ptr(o.s.now())})
		if err != nil {
			return err
		}
		if n == 0 {
			return apperr.NotFound("pengumuman aktif")
		}
		return o.s.Audit.Log(ctx, q, p, "announcement.end", "announcement", annID, nil, nil)
	})
}

type AnnouncementView struct {
	ID       string     `json:"id"`
	Level    string     `json:"level"`
	Body     string     `json:"body"`
	StartsAt time.Time  `json:"starts_at"`
	EndsAt   *time.Time `json:"ends_at,omitempty"`
}

func (o *OpsService) Active(ctx context.Context, locale string) ([]AnnouncementView, error) {
	rows, err := o.s.Q.ActiveAnnouncements(ctx, o.s.now())
	if err != nil {
		return nil, err
	}
	out := make([]AnnouncementView, 0, len(rows))
	for _, r := range rows {
		b := r.BodyID
		if locale == "en" && r.BodyEn != nil {
			b = *r.BodyEn
		}
		out = append(out, AnnouncementView{ID: r.ID, Level: r.Level, Body: b, StartsAt: r.StartsAt, EndsAt: r.EndsAt})
	}
	return out, nil
}

func (o *OpsService) AllAnnouncements(ctx context.Context) ([]sqlcgen.Announcement, error) {
	return o.s.Q.ListAnnouncements(ctx, 100)
}

// ---------- feature flags ----------

type FlagInput struct {
	Enabled    bool `json:"enabled"`
	RolloutPct int  `json:"rollout_pct" binding:"min=0,max=100"`
}

func (o *OpsService) SetFlag(ctx context.Context, p *auth.Principal, key string, in FlagInput) (*sqlcgen.FeatureFlag, error) {
	if !flagKeyRe.MatchString(key) {
		return nil, apperr.Validation("key flag: huruf kecil, angka, _ atau . (3–64)").With("field", "key")
	}
	var out sqlcgen.FeatureFlag
	err := o.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		out, err = q.UpsertFlag(ctx, sqlcgen.UpsertFlagParams{Key: key, Enabled: in.Enabled, RolloutPct: in.RolloutPct, UpdatedBy: &p.UserID})
		if err != nil {
			return err
		}
		return o.s.Audit.Log(ctx, q, p, "flag.set", "flag", key, nil, in)
	})
	o.mu.Lock()
	o.flagsAt = time.Time{}
	o.mu.Unlock()
	return &out, err
}

func (o *OpsService) loadFlags(ctx context.Context) map[string]sqlcgen.FeatureFlag {
	o.mu.Lock()
	if o.flags != nil && time.Since(o.flagsAt) < flagsTTL {
		f := o.flags
		o.mu.Unlock()
		return f
	}
	o.mu.Unlock()
	rows, err := o.s.Q.ListFlags(ctx)
	if err != nil {
		o.s.Log.Error("muat feature flag gagal", "error", err)
		o.mu.Lock()
		defer o.mu.Unlock()
		return o.flags
	}
	m := make(map[string]sqlcgen.FeatureFlag, len(rows))
	for _, r := range rows {
		m[r.Key] = r
	}
	o.mu.Lock()
	o.flags, o.flagsAt = m, time.Now()
	o.mu.Unlock()
	return m
}

// Enabled: flag aktif untuk subject (user/anon) menurut persentase rollout (hash stabil per key+subject). Flag tak dikenal = false.
func (o *OpsService) Enabled(ctx context.Context, key, subject string) bool {
	f, ok := o.loadFlags(ctx)[key]
	if !ok || !f.Enabled {
		return false
	}
	if f.RolloutPct >= 100 {
		return true
	}
	if f.RolloutPct <= 0 || subject == "" {
		return false
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(key + ":" + subject))
	return int(h.Sum32()%100) < f.RolloutPct
}

func (o *OpsService) ListFlags(ctx context.Context) ([]sqlcgen.FeatureFlag, error) {
	return o.s.Q.ListFlags(ctx)
}

// FlagsFor = peta flag untuk klien (hanya key + boolean).
func (o *OpsService) FlagsFor(ctx context.Context, subject string) map[string]bool {
	out := map[string]bool{}
	for k := range o.loadFlags(ctx) {
		out[k] = o.Enabled(ctx, k, subject)
	}
	return out
}

// ---------- status layanan ----------

type ComponentStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"` // operational | degraded | down
}

type ServiceStatus struct {
	Overall       string             `json:"overall"`
	Components    []ComponentStatus  `json:"components"`
	Announcements []AnnouncementView `json:"announcements"`
	Maintenance   *MaintenanceInfo   `json:"maintenance,omitempty"`
}

func (o *OpsService) Status(ctx context.Context) (*ServiceStatus, error) {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	st := &ServiceStatus{Overall: "operational", Components: []ComponentStatus{}}
	add := func(name string, err error) {
		s := "operational"
		if err != nil {
			s = "down"
			st.Overall = "degraded"
		}
		st.Components = append(st.Components, ComponentStatus{Name: name, Status: s})
	}
	add("database", o.s.Pool.Ping(cctx))
	add("cache", o.s.Redis.Ping(cctx).Err())
	gw := "operational"
	if o.s.Gateway.Name() == "unconfigured" {
		gw = "degraded"
		st.Overall = "degraded"
	}
	st.Components = append(st.Components, ComponentStatus{Name: "payments", Status: gw})
	st.Announcements, _ = o.Active(ctx, "id")
	for _, a := range st.Announcements {
		if a.Level == "incident" {
			st.Overall = "degraded"
		}
	}
	if st.Maintenance = o.Maintenance(ctx); st.Maintenance != nil {
		st.Overall = "maintenance"
	}
	return st, nil
}

// ResetCache membuang cache lokal (tes & setelah perubahan konfigurasi).
func (o *OpsService) ResetCache() {
	o.mu.Lock()
	o.mAt, o.flagsAt = time.Time{}, time.Time{}
	o.mu.Unlock()
}
