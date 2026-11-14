package service

import (
	"context"
	"time"
	"tixgo/backend/internal/apperr"

	"tixgo/backend/internal/repository/sqlcgen"
)

// AnalyticsService = funnel produk agregat tanpa PII (docs/20 M46). Penghitung disangga Redis lalu di-flush ke DB
// agar jalur panas (antrean/hold) tidak menulis ke Postgres tiap permintaan.
type AnalyticsService struct{ s *Services }

func newAnalyticsService(s *Services) *AnalyticsService { return &AnalyticsService{s: s} }

var funnelStages = map[string]bool{"view": true, "queue_join": true, "admitted": true, "hold": true, "paid": true, "ticket": true}

func (a *AnalyticsService) hashKey(eventID, stage string, day time.Time) string {
	return "fun:" + day.Format("2006-01-02") + ":" + eventID
}

// Track menambah penghitung funnel (best-effort; kegagalan tidak boleh mengganggu alur utama).
func (a *AnalyticsService) Track(ctx context.Context, eventID, stage string) {
	a.TrackN(ctx, eventID, stage, 1)
}

func (a *AnalyticsService) TrackN(ctx context.Context, eventID, stage string, n int64) {
	if !funnelStages[stage] {
		return
	}
	if err := a.s.Redis.HIncrBy(ctx, a.hashKey(eventID, stage, a.s.now()), stage, n).Err(); err != nil {
		a.s.Log.Error("funnel track gagal", "error", err)
		return
	}
	a.s.Redis.SAdd(ctx, "fun:keys", a.hashKey(eventID, stage, a.s.now()))
}

// Flush memindahkan penghitung Redis ke funnel_daily (dipanggil worker).
func (a *AnalyticsService) Flush(ctx context.Context) (int, error) {
	keys, err := a.s.Redis.SMembers(ctx, "fun:keys").Result()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, k := range keys {
		// k = fun:YYYY-MM-DD:EVENTID
		if len(k) < 4+10+1+26 {
			a.s.Redis.SRem(ctx, "fun:keys", k)
			continue
		}
		day, err := time.Parse("2006-01-02", k[4:14])
		if err != nil {
			a.s.Redis.SRem(ctx, "fun:keys", k)
			continue
		}
		eventID := k[15:]
		vals, err := a.s.Redis.HGetAll(ctx, k).Result()
		if err != nil {
			return n, err
		}
		for stage, v := range vals {
			var c int64
			for _, ch := range v {
				c = c*10 + int64(ch-'0')
			}
			if c == 0 {
				continue
			}
			if err := a.s.Q.IncFunnel(ctx, sqlcgen.IncFunnelParams{Day: day, EventID: eventID, Stage: stage, Count: c}); err != nil {
				return n, err
			}
			a.s.Redis.HIncrBy(ctx, k, stage, -c)
			n++
		}
	}
	return n, nil
}

// ---------- laporan organizer (M19) & dasbor bisnis (M46) ----------

type SeriesPoint struct {
	T       time.Time `json:"t"`
	Orders  int       `json:"orders"`
	Tickets int       `json:"tickets"`
	Revenue int64     `json:"revenue"`
}

func (a *AnalyticsService) window(from, to *time.Time, def time.Duration) (time.Time, time.Time) {
	end := a.s.now().Add(time.Hour)
	if to != nil {
		end = *to
	}
	start := end.Add(-def)
	if from != nil {
		start = *from
	}
	return start, end
}

// Sales: deret penjualan per jam/hari untuk event milik organizer.
func (a *AnalyticsService) Sales(ctx context.Context, sc OrgScope, eventID, bucket string, from, to *time.Time) ([]SeriesPoint, error) {
	if bucket != "hour" && bucket != "day" {
		return nil, apperr.Validation("bucket harus hour atau day").With("field", "bucket")
	}
	if _, err := a.s.Event.ownEvent(ctx, a.s.Q, sc, eventID); err != nil {
		return nil, err
	}
	f, t := a.window(from, to, 30*24*time.Hour)
	if t.Sub(f) > 366*24*time.Hour || !t.After(f) {
		return nil, apperr.Validation("rentang waktu tidak valid (maks 366 hari)").With("field", "from")
	}
	rows, err := a.s.Q.SalesSeries(ctx, sqlcgen.SalesSeriesParams{Bucket: bucket, EventID: eventID, FromTs: &f, ToTs: &t})
	if err != nil {
		return nil, err
	}
	out := make([]SeriesPoint, 0, len(rows))
	for _, r := range rows {
		out = append(out, SeriesPoint{T: r.Bucket, Orders: int(r.Orders), Tickets: int(r.Tickets), Revenue: r.Revenue})
	}
	return out, nil
}

type FunnelStage struct {
	Stage    string  `json:"stage"`
	Count    int64   `json:"count"`
	FromPrev float64 `json:"conversion_from_prev"` // 0..1
}

var funnelOrder = []string{"view", "queue_join", "admitted", "hold", "paid", "ticket"}

func orderedFunnel(totals map[string]int64) []FunnelStage {
	out := make([]FunnelStage, 0, len(funnelOrder))
	var prev int64
	for i, s := range funnelOrder {
		c := totals[s]
		fs := FunnelStage{Stage: s, Count: c}
		if i > 0 && prev > 0 {
			fs.FromPrev = float64(c) / float64(prev)
		}
		out = append(out, fs)
		if c > 0 || i == 0 {
			prev = c
		}
	}
	return out
}

func (a *AnalyticsService) EventFunnel(ctx context.Context, sc OrgScope, eventID string, from, to *time.Time) ([]FunnelStage, error) {
	if _, err := a.s.Event.ownEvent(ctx, a.s.Q, sc, eventID); err != nil {
		return nil, err
	}
	f, t := a.window(from, to, 30*24*time.Hour)
	rows, err := a.s.Q.FunnelForEvent(ctx, sqlcgen.FunnelForEventParams{EventID: eventID, Day: f, Day_2: t})
	if err != nil {
		return nil, err
	}
	m := map[string]int64{}
	for _, r := range rows {
		m[r.Stage] = r.Total
	}
	return orderedFunnel(m), nil
}

type GatePoint struct {
	T       time.Time `json:"t"`
	GateID  string    `json:"gate_id"`
	Entered int       `json:"entered"`
}

// GateFlow: arus masuk per 5 menit per pintu (Live Gate Heatmap, docs/10 §8).
func (a *AnalyticsService) GateFlow(ctx context.Context, sc OrgScope, eventID string, from, to *time.Time) ([]GatePoint, error) {
	ev, err := a.s.Event.ownEvent(ctx, a.s.Q, sc, eventID)
	if err != nil {
		return nil, err
	}
	def := ev.StartsAt.Add(-3 * time.Hour)
	f, t := a.window(from, to, 12*time.Hour)
	if from == nil {
		f = def
	}
	if to == nil {
		t = ev.StartsAt.Add(12 * time.Hour)
	}
	rows, err := a.s.Q.GateFlow(ctx, sqlcgen.GateFlowParams{EventID: eventID, FromTs: f, ToTs: t})
	if err != nil {
		return nil, err
	}
	out := make([]GatePoint, 0, len(rows))
	for _, r := range rows {
		out = append(out, GatePoint{T: r.Bucket, GateID: r.GateID, Entered: int(r.Entered)})
	}
	return out, nil
}

type BusinessDashboard struct {
	Funnel         []FunnelStage    `json:"funnel"`
	MedianCheckout float64          `json:"median_checkout_sec"`
	PaidOrders     int              `json:"paid_orders"`
	PaymentMethods []map[string]any `json:"payment_methods"`
}

// Business: konversi per tahap (agregat tanpa PII), median waktu checkout, kegagalan per metode bayar (M46 AC3).
func (a *AnalyticsService) Business(ctx context.Context, from, to *time.Time) (*BusinessDashboard, error) {
	f, t := a.window(from, to, 30*24*time.Hour)
	fr, err := a.s.Q.GlobalFunnel(ctx, sqlcgen.GlobalFunnelParams{FromDay: f, ToDay: t})
	if err != nil {
		return nil, err
	}
	m := map[string]int64{}
	for _, r := range fr {
		m[r.Stage] = r.Total
	}
	med, err := a.s.Q.MedianCheckoutSeconds(ctx, sqlcgen.MedianCheckoutSecondsParams{FromTs: &f, ToTs: &t})
	if err != nil {
		return nil, err
	}
	pm, err := a.s.Q.PaymentOutcomeByMethod(ctx, sqlcgen.PaymentOutcomeByMethodParams{FromTs: f, ToTs: t})
	if err != nil {
		return nil, err
	}
	d := &BusinessDashboard{Funnel: orderedFunnel(m), MedianCheckout: med.MedianSec, PaidOrders: int(med.N), PaymentMethods: []map[string]any{}}
	for _, r := range pm {
		fail := 0.0
		if r.Total > 0 {
			fail = float64(r.Failed+r.Expired) / float64(r.Total)
		}
		d.PaymentMethods = append(d.PaymentMethods, map[string]any{"method": r.Method, "total": r.Total, "settled": r.Settled, "failed": r.Failed, "expired": r.Expired, "failure_rate": fail})
	}
	return d, nil
}
