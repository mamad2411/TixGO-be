package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/platform"
	"tixgo/backend/internal/repository/sqlcgen"
)

// CatalogService = katalog publik: daftar/filter, detail, kalender, ketersediaan (docs/20 M2, M3, M4).
type CatalogService struct{ s *Services }

func newCatalogService(s *Services) *CatalogService { return &CatalogService{s: s} }

type EventFilter struct {
	Q        string
	City     string
	Genre    string
	DateFrom *time.Time
	DateTo   *time.Time
	PriceMin *int64
	PriceMax *int64
	Status   string // upcoming|onsale|soldout|"" (semua)
	Sort     string // date|new|popular
	Page     int
	PerPage  int
}

// Sale states
const (
	SaleUpcoming   = "upcoming"
	SaleOnsale     = "onsale"
	SaleFullBooked = "full_booked"
	SaleSoldOut    = "sold_out"
	SaleEnded      = "ended"
	SalePaused     = "paused"
	SaleCancelled  = "cancelled"
)

type EventCard struct {
	ID            string     `json:"id"`
	Slug          string     `json:"slug"`
	Title         string     `json:"title"`
	Genre         *string    `json:"genre"`
	PosterURL     string     `json:"poster_url,omitempty"`
	StartsAt      time.Time  `json:"starts_at"`
	TZ            string     `json:"tz"`
	VenueName     string     `json:"venue_name"`
	City          string     `json:"city"`
	MinPrice      *int64     `json:"min_price"`       // harga dasar termurah
	MinFinalPrice *int64     `json:"min_final_price"` // harga akhir termurah (termasuk biaya layanan) — F1
	SaleState     string     `json:"sale_state"`
	NextPhaseAt   *time.Time `json:"next_phase_at,omitempty"`
	ScheduleState string     `json:"schedule_state"`
	Remaining     int64      `json:"remaining"`
	AlmostGone    bool       `json:"almost_gone"`
	ArtistNames   []string   `json:"artist_names"`
	QueueRequired bool       `json:"queue_required"`
}

type listRow struct {
	ID            string     `db:"id"`
	Slug          string     `db:"slug"`
	Title         string     `db:"title"`
	Genre         *string    `db:"genre"`
	PosterKey     *string    `db:"poster_key"`
	StartsAt      time.Time  `db:"starts_at"`
	Status        string     `db:"status"`
	ScheduleState string     `db:"schedule_state"`
	SalesPaused   bool       `db:"sales_paused"`
	QueueRequired bool       `db:"queue_required"`
	FeeBP         int        `db:"service_fee_bp"`
	FeeFixed      int64      `db:"service_fee_fixed"`
	FeePayer      string     `db:"fee_payer"`
	VenueName     string     `db:"venue_name"`
	City          string     `db:"city"`
	TZ            string     `db:"tz"`
	MinPrice      *int64     `db:"min_price"`
	Capacity      int64      `db:"capacity"`
	Sold          int64      `db:"sold"`
	Held          int64      `db:"held"`
	ActivePhase   bool       `db:"active_phase"`
	NextPhaseAt   *time.Time `db:"next_phase_at"`
	LastPhaseEnd  *time.Time `db:"last_phase_end"`
}

// lateral inventori + fase; dipakai daftar & kalender.
const listSelect = `
SELECT e.id, e.slug, e.title, e.genre, e.poster_key, e.starts_at, e.status, e.schedule_state, e.sales_paused, e.queue_required,
  e.service_fee_bp, e.service_fee_fixed, e.fee_payer, v.name AS venue_name, v.city, v.tz,
  (SELECT min(price) FROM tiers t WHERE t.event_id = e.id) AS min_price,
  inv.capacity, inv.sold, inv.held,
  ph.active AS active_phase, ph.next_at AS next_phase_at, ph.last_end AS last_phase_end
FROM events e
JOIN venues v ON v.id = e.venue_id
LEFT JOIN LATERAL (
  SELECT (CASE WHEN e.mode = 'general' THEN COALESCE((SELECT sum(quota) FROM tiers WHERE event_id = e.id), 0)
               ELSE (SELECT count(*) FROM seats WHERE event_id = e.id AND status <> 'blocked') END)::bigint AS capacity,
         (CASE WHEN e.mode = 'general' THEN COALESCE((SELECT sum(sold) FROM tiers WHERE event_id = e.id), 0)
               ELSE (SELECT count(*) FROM seats WHERE event_id = e.id AND status = 'sold') END)::bigint AS sold,
         (CASE WHEN e.mode = 'general' THEN COALESCE((SELECT sum(held) FROM tiers WHERE event_id = e.id), 0)
               ELSE (SELECT count(*) FROM seats WHERE event_id = e.id AND status = 'held') END)::bigint AS held
) inv ON true
LEFT JOIN LATERAL (
  SELECT bool_or(sp.starts_at <= $1 AND $1 < sp.ends_at) AS active,
         min(sp.starts_at) FILTER (WHERE sp.starts_at > $1) AS next_at,
         max(sp.ends_at) AS last_end
  FROM sale_phases sp WHERE sp.event_id = e.id
) ph ON true
`

type qb struct {
	args  []any
	where []string
}

func (b *qb) add(cond string, v any) {
	b.args = append(b.args, v)
	b.where = append(b.where, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(b.args))))
}

func (b *qb) raw(cond string) { b.where = append(b.where, cond) }

func saleState(r listRow, now time.Time) string {
	switch {
	case r.ScheduleState == "cancelled" || r.Status == stCancelled:
		return SaleCancelled
	case r.Status == stPaused || r.SalesPaused:
		return SalePaused
	case r.Status == stFinished:
		return SaleEnded
	}
	remaining := r.Capacity - r.Sold - r.Held
	if r.ActivePhase {
		switch {
		case r.Capacity > 0 && remaining <= 0 && r.Held > 0:
			return SaleFullBooked
		case r.Capacity > 0 && remaining <= 0:
			return SaleSoldOut
		}
		return SaleOnsale
	}
	if r.NextPhaseAt != nil {
		return SaleUpcoming
	}
	if r.Capacity > 0 && r.Sold >= r.Capacity {
		return SaleSoldOut
	}
	return SaleEnded
}

func almostGone(capacity, remaining int64) bool {
	if capacity <= 0 || remaining <= 0 {
		return false
	}
	th := capacity / 10
	if th < 5 {
		th = 5
	}
	return remaining <= th
}

func (c *CatalogService) card(ctx context.Context, r listRow, artists map[string][]string, now time.Time) EventCard {
	cfg := FeeConfig{BP: r.FeeBP, Fixed: r.FeeFixed, Payer: r.FeePayer}
	card := EventCard{ID: r.ID, Slug: r.Slug, Title: r.Title, Genre: r.Genre, StartsAt: r.StartsAt, TZ: r.TZ, VenueName: r.VenueName, City: r.City,
		MinPrice: r.MinPrice, SaleState: saleState(r, now), NextPhaseAt: r.NextPhaseAt, ScheduleState: r.ScheduleState, QueueRequired: r.QueueRequired,
		ArtistNames: artists[r.ID]}
	if card.ArtistNames == nil {
		card.ArtistNames = []string{}
	}
	if r.PosterKey != nil {
		card.PosterURL = MediaURL(*r.PosterKey, 800)
	}
	if r.MinPrice != nil {
		f := cfg.FinalPrice(*r.MinPrice)
		card.MinFinalPrice = &f
	}
	card.Remaining = r.Capacity - r.Sold - r.Held
	if card.Remaining < 0 {
		card.Remaining = 0
	}
	card.AlmostGone = card.SaleState == SaleOnsale && almostGone(r.Capacity, card.Remaining)
	return card
}

func (c *CatalogService) artistNames(ctx context.Context, ids []string) (map[string][]string, error) {
	out := map[string][]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := c.s.Pool.Query(ctx, `SELECT ea.event_id, a.name FROM event_artists ea JOIN artists a ON a.id = ea.artist_id
		WHERE ea.event_id = ANY($1) ORDER BY ea.event_id, ea.billing_order, a.name`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var eid, name string
		if err := rows.Scan(&eid, &name); err != nil {
			return nil, err
		}
		out[eid] = append(out[eid], name)
	}
	return out, rows.Err()
}

// List: hanya event terbit & publik (unlisted/password tidak muncul di daftar).
func (c *CatalogService) List(ctx context.Context, f EventFilter) ([]EventCard, int, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PerPage < 1 || f.PerPage > 100 {
		f.PerPage = 20
	}
	now := c.s.now()
	b := &qb{args: []any{now}}
	b.raw("e.status IN ('published','paused')")
	b.raw("e.visibility = 'public'")
	b.raw("EXISTS (SELECT 1 FROM organizers org WHERE org.id = e.organizer_id AND org.status = 'active')") // organizer ditangguhkan = tak tampil
	b.raw("e.schedule_state <> 'cancelled'")
	if f.Q != "" {
		q := "%" + escapeLike(strings.TrimSpace(f.Q)) + "%"
		b.args = append(b.args, q)
		n := len(b.args)
		b.raw(fmt.Sprintf(`(e.title ILIKE $%[1]d OR v.name ILIKE $%[1]d OR EXISTS (SELECT 1 FROM event_artists ea JOIN artists a ON a.id = ea.artist_id WHERE ea.event_id = e.id AND a.name ILIKE $%[1]d))`, n))
	}
	if f.City != "" {
		b.add("lower(v.city) = lower(?)", f.City)
	}
	if f.Genre != "" {
		b.add("lower(e.genre) = lower(?)", f.Genre)
	}
	if f.DateFrom != nil {
		b.add("e.starts_at >= ?", *f.DateFrom)
	}
	if f.DateTo != nil {
		b.add("e.starts_at < ?", *f.DateTo)
	}
	if f.PriceMin != nil {
		b.add("EXISTS (SELECT 1 FROM tiers t WHERE t.event_id = e.id AND t.price >= ?)", *f.PriceMin)
	}
	if f.PriceMax != nil {
		b.add("EXISTS (SELECT 1 FROM tiers t WHERE t.event_id = e.id AND t.price <= ?)", *f.PriceMax)
	}
	switch f.Status {
	case "upcoming":
		b.raw("NOT COALESCE(ph.active, false) AND ph.next_at IS NOT NULL")
	case "onsale":
		b.raw("COALESCE(ph.active, false) AND inv.capacity - inv.sold - inv.held > 0")
	case "soldout":
		b.raw("inv.capacity > 0 AND inv.capacity - inv.sold - inv.held <= 0")
	case "":
	default:
		return nil, 0, apperr.Validation("status harus upcoming, onsale, atau soldout").With("field", "status")
	}
	where := " WHERE " + strings.Join(b.where, " AND ")
	order := "e.starts_at ASC, e.id"
	switch f.Sort {
	case "", "date":
	case "new":
		order = "e.published_at DESC NULLS LAST, e.id"
	case "popular":
		order = "inv.sold DESC, e.starts_at ASC, e.id"
	default:
		return nil, 0, apperr.Validation("sort harus date, new, atau popular").With("field", "sort")
	}
	var total int
	if err := c.s.Pool.QueryRow(ctx, "SELECT count(*) FROM ("+listSelect+where+") x", b.args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args := append(append([]any{}, b.args...), f.PerPage, (f.Page-1)*f.PerPage)
	sql := listSelect + where + fmt.Sprintf(" ORDER BY %s LIMIT $%d OFFSET $%d", order, len(args)-1, len(args))
	rows, err := c.s.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, err
	}
	lr, err := pgx.CollectRows(rows, pgx.RowToStructByName[listRow])
	if err != nil {
		return nil, 0, err
	}
	ids := make([]string, 0, len(lr))
	for _, r := range lr {
		ids = append(ids, r.ID)
	}
	arts, err := c.artistNames(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	out := make([]EventCard, 0, len(lr))
	for _, r := range lr {
		out = append(out, c.card(ctx, r, arts, now))
	}
	return out, total, nil
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// CardsByIDs untuk favorit/rekomendasi (urutan sesuai ids).
func (c *CatalogService) CardsByIDs(ctx context.Context, ids []string) ([]EventCard, error) {
	if len(ids) == 0 {
		return []EventCard{}, nil
	}
	now := c.s.now()
	rows, err := c.s.Pool.Query(ctx, listSelect+" WHERE e.id = ANY($2) AND e.status IN ('published','paused') AND e.visibility = 'public' AND EXISTS (SELECT 1 FROM organizers org WHERE org.id = e.organizer_id AND org.status = 'active')", now, ids)
	if err != nil {
		return nil, err
	}
	lr, err := pgx.CollectRows(rows, pgx.RowToStructByName[listRow])
	if err != nil {
		return nil, err
	}
	arts, err := c.artistNames(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := map[string]EventCard{}
	for _, r := range lr {
		byID[r.ID] = c.card(ctx, r, arts, now)
	}
	out := make([]EventCard, 0, len(ids))
	for _, id := range ids {
		if cd, ok := byID[id]; ok {
			out = append(out, cd)
		}
	}
	return out, nil
}

// ---------- detail ----------

type PublicTier struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Price      int64   `json:"price"`
	Fee        int64   `json:"fee"`         // biaya layanan per tiket yang ditanggung pembeli
	FinalPrice int64   `json:"final_price"` // harga akhir per tiket (F1)
	Color      *string `json:"color"`
	Capacity   int64   `json:"capacity"`
	Available  int64   `json:"available"`
	Held       int64   `json:"held"` // "N sedang membayar" (F4)
	Status     string  `json:"status"`
}

type PublicEvent struct {
	EventCard
	Description       *string      `json:"description"`
	DoorsAt           *time.Time   `json:"doors_at"`
	EndsAt            *time.Time   `json:"ends_at"`
	Venue             *VenueView   `json:"venue"`
	Artists           []ArtistView `json:"artists"`
	Tiers             []PublicTier `json:"tiers"`
	Phases            []PhaseView  `json:"phases"`
	Mode              string       `json:"mode"`
	Visibility        string       `json:"visibility"`
	AgeMin            *int         `json:"age_min"`
	MaxPerOrder       int          `json:"max_per_order"`
	MaxPerAccount     int          `json:"max_per_account"`
	QueueOpensAt      *time.Time   `json:"queue_opens_at,omitempty"`
	RefundPolicy      RefundPolicy `json:"refund_policy"`
	RefundSummary     string       `json:"refund_summary"`
	PreviousStartsAt  *time.Time   `json:"previous_starts_at,omitempty"`
	SalesPausedReason *string      `json:"sales_paused_reason,omitempty"`
	Addons            []AddonView  `json:"addons"`
	HasSeatmap        bool         `json:"has_seatmap"`
}

func tierStatus(capacity, available, held int64) string {
	switch {
	case capacity <= 0:
		return "unavailable"
	case available > 0 && almostGone(capacity, available):
		return "almost_gone"
	case available > 0:
		return "available"
	case held > 0:
		return SaleFullBooked
	default:
		return SaleSoldOut
	}
}

// lookup menerima slug atau ID (ULID huruf besar; slug selalu huruf kecil sehingga tidak bisa bentrok).
func (c *CatalogService) lookup(ctx context.Context, key string) (sqlcgen.Event, error) {
	if id.Valid(key) {
		return c.s.Q.GetEvent(ctx, key)
	}
	return c.s.Q.GetEventBySlug(ctx, key)
}

// Detail memuat event berdasarkan slug. password: untuk visibility=password.
func (c *CatalogService) Detail(ctx context.Context, slug, password, ip string) (*PublicEvent, error) {
	ev, err := c.lookup(ctx, slug)
	if err != nil {
		if platform.IsNoRows(err) {
			return nil, apperr.NotFound("event")
		}
		return nil, err
	}
	if ev.Status != stPublished && ev.Status != stPaused && ev.Status != stFinished && ev.Status != stCancelled {
		return nil, apperr.NotFound("event")
	}
	if org, err := c.s.Q.GetOrganizer(ctx, ev.OrganizerID); err != nil || org.Status != "active" {
		return nil, apperr.NotFound("event") // organizer ditangguhkan: halaman event disembunyikan
	}
	if ev.Visibility == "password" {
		if ok, ra, err := c.s.Limiter.Allow(ctx, "evpw:"+ev.ID+":"+ip, 10, time.Minute); err == nil && !ok {
			return nil, apperr.TooMany("terlalu banyak percobaan password").With("retry_after", int(ra.Seconds())+1)
		}
		if ev.PasswordHash == nil || password == "" || !c.s.Hasher.Verify(password, *ev.PasswordHash) {
			return nil, apperr.ForbiddenCode(apperr.CodeForbidden, "event ini dilindungi password").With("password_required", true)
		}
	}
	now := c.s.now()
	ven, err := c.s.Q.GetVenue(ctx, ev.VenueID)
	if err != nil {
		return nil, err
	}
	phases, err := c.s.Q.ListPhases(ctx, ev.ID)
	if err != nil {
		return nil, err
	}
	avail, err := c.s.Q.TierAvailability(ctx, ev.ID)
	if err != nil {
		return nil, err
	}
	cfg := feeConfigOf(ev)
	var capacity, sold, held int64
	var minPrice *int64
	tiers := make([]PublicTier, 0, len(avail))
	for _, a := range avail {
		remaining := a.Capacity - a.Sold - a.Held
		if remaining < 0 {
			remaining = 0
		}
		capacity, sold, held = capacity+a.Capacity, sold+a.Sold, held+a.Held
		tiers = append(tiers, PublicTier{ID: a.ID, Name: a.Name, Price: a.Price, Fee: cfg.BuyerFee(a.Price), FinalPrice: cfg.FinalPrice(a.Price),
			Color: a.Color, Capacity: a.Capacity, Available: remaining, Held: a.Held, Status: tierStatus(a.Capacity, remaining, a.Held)})
		if minPrice == nil || a.Price < *minPrice {
			p := a.Price
			minPrice = &p
		}
	}
	lr := listRow{ID: ev.ID, Slug: ev.Slug, Title: ev.Title, Genre: ev.Genre, PosterKey: ev.PosterKey, StartsAt: ev.StartsAt, Status: ev.Status,
		ScheduleState: ev.ScheduleState, SalesPaused: ev.SalesPaused, QueueRequired: ev.QueueRequired, FeeBP: ev.ServiceFeeBp, FeeFixed: ev.ServiceFeeFixed,
		FeePayer: ev.FeePayer, VenueName: ven.Name, City: ven.City, TZ: ven.Tz, MinPrice: minPrice, Capacity: capacity, Sold: sold, Held: held}
	var queueOpens *time.Time
	for _, p := range phases {
		if !now.Before(p.StartsAt) && now.Before(p.EndsAt) {
			lr.ActivePhase = true
		}
		if p.StartsAt.After(now) && (lr.NextPhaseAt == nil || p.StartsAt.Before(*lr.NextPhaseAt)) {
			t := p.StartsAt
			lr.NextPhaseAt = &t
		}
		if ev.QueueRequired && p.EndsAt.After(now) {
			qo := p.QueueOpenAt
			if qo == nil {
				t := p.StartsAt.Add(-time.Duration(ev.QueuePreOpenMin) * time.Minute)
				qo = &t
			}
			if queueOpens == nil || qo.Before(*queueOpens) {
				queueOpens = qo
			}
		}
	}
	arts, err := c.s.Q.ListEventArtists(ctx, ev.ID)
	if err != nil {
		return nil, err
	}
	names := map[string][]string{}
	artists := make([]ArtistView, 0, len(arts))
	for _, a := range arts {
		names[ev.ID] = append(names[ev.ID], a.Name)
		av := ArtistView{ID: a.ID, Slug: a.Slug, Name: a.Name, Bio: a.Bio}
		if a.ImageKey != nil {
			av.ImageURL = MediaURL(*a.ImageKey, 800)
		}
		artists = append(artists, av)
	}
	pol := ParseRefundPolicy(ev.RefundPolicy, c.s.Cfg.RefundRescheduleWindowDays)
	pv := &PublicEvent{
		EventCard: c.card(ctx, lr, names, now), Description: ev.Description, DoorsAt: ev.DoorsAt, EndsAt: ev.EndsAt, Venue: ptr(venueView(ven)),
		Artists: artists, Tiers: tiers, Mode: ev.Mode, Visibility: ev.Visibility, AgeMin: ev.AgeMin, MaxPerOrder: ev.MaxPerOrder,
		MaxPerAccount: ev.MaxPerAccount, QueueOpensAt: queueOpens, RefundPolicy: pol, RefundSummary: pol.Summary("id"),
		PreviousStartsAt: ev.PreviousStartsAt, SalesPausedReason: ev.SalesPausedReason,
	}
	for _, p := range phases {
		pv.Phases = append(pv.Phases, phaseView(p))
	}
	if pv.Phases == nil {
		pv.Phases = []PhaseView{}
	}
	addons, err := c.s.Q.ListAddons(ctx, sqlcgen.ListAddonsParams{EventID: ev.ID, OnlyActive: true})
	if err != nil {
		return nil, err
	}
	pv.Addons = make([]AddonView, 0, len(addons))
	for _, a := range addons {
		pv.Addons = append(pv.Addons, addonView(a))
	}
	if ev.Mode == "seated" {
		n, _ := c.s.Q.CountEventSeats(ctx, ev.ID)
		pv.HasSeatmap = n > 0
	}
	return pv, nil
}

// EventBySlug memuat event untuk operasi lanjutan (denah, ketersediaan) dengan pemeriksaan visibilitas.
func (c *CatalogService) EventBySlug(ctx context.Context, slug, password, ip string) (sqlcgen.Event, error) {
	ev, err := c.lookup(ctx, slug)
	if err != nil || (ev.Status != stPublished && ev.Status != stPaused && ev.Status != stFinished) {
		return ev, apperr.NotFound("event")
	}
	if ev.Visibility == "password" {
		if ev.PasswordHash == nil || password == "" || !c.s.Hasher.Verify(password, *ev.PasswordHash) {
			return ev, apperr.ForbiddenCode(apperr.CodeForbidden, "event ini dilindungi password").With("password_required", true)
		}
	}
	return ev, nil
}

// ---------- ketersediaan ----------

type TierAvail struct {
	ID        string `json:"id"`
	Available int64  `json:"available"`
	Held      int64  `json:"held"`
	Sold      int64  `json:"sold"`
	Status    string `json:"status"`
}

type Availability struct {
	Tiers            []TierAvail       `json:"tiers"`
	UnavailableSeats map[string]string `json:"unavailable_seats,omitempty"`
	GeneratedAt      time.Time         `json:"generated_at"`
}

// Availability: status kursi/kuota. Cache Redis 1 detik (hanya untuk TAMPILAN; keputusan hold tetap ke DB).
func (c *CatalogService) Availability(ctx context.Context, ev sqlcgen.Event) (*Availability, error) {
	key := "avail:" + ev.ID
	if b, err := c.s.Redis.Get(ctx, key).Bytes(); err == nil {
		var cached Availability
		if jsonUnmarshal(b, &cached) == nil {
			return &cached, nil
		}
	}
	tiers, err := c.s.Q.TierAvailability(ctx, ev.ID)
	if err != nil {
		return nil, err
	}
	out := &Availability{GeneratedAt: c.s.now()}
	for _, t := range tiers {
		rem := t.Capacity - t.Sold - t.Held
		if rem < 0 {
			rem = 0
		}
		out.Tiers = append(out.Tiers, TierAvail{ID: t.ID, Available: rem, Held: t.Held, Sold: t.Sold, Status: tierStatus(t.Capacity, rem, t.Held)})
	}
	if out.Tiers == nil {
		out.Tiers = []TierAvail{}
	}
	if ev.Mode == "seated" {
		rows, err := c.s.Q.ListSeatStatuses(ctx, ev.ID)
		if err != nil {
			return nil, err
		}
		out.UnavailableSeats = make(map[string]string, len(rows))
		for _, r := range rows {
			out.UnavailableSeats[r.ID] = r.Status
		}
	}
	if b, err := jsonMarshal(out); err == nil {
		c.s.Redis.Set(ctx, key, b, time.Second)
	}
	return out, nil
}

// InvalidateAvailability dipanggil setelah perubahan stok agar tampilan segera segar.
func (c *CatalogService) InvalidateAvailability(ctx context.Context, eventID string) {
	c.s.Redis.Del(ctx, "avail:"+eventID)
}

// ---------- kalender & filter ----------

type CalendarDay struct {
	Date   string      `json:"date"`
	Events []EventCard `json:"events"`
}

func (c *CatalogService) Calendar(ctx context.Context, month time.Time) ([]CalendarDay, error) {
	from := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
	to := from.AddDate(0, 1, 2)
	cards, _, err := c.List(ctx, EventFilter{DateFrom: &from, DateTo: &to, PerPage: 100, Page: 1})
	if err != nil {
		return nil, err
	}
	days := map[string][]EventCard{}
	for _, cd := range cards {
		loc, err := time.LoadLocation(cd.TZ)
		if err != nil {
			loc = time.UTC
		}
		l := cd.StartsAt.In(loc)
		if l.Year() != month.Year() || l.Month() != month.Month() {
			continue
		}
		d := l.Format("2006-01-02")
		days[d] = append(days[d], cd)
	}
	keys := make([]string, 0, len(days))
	for k := range days {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]CalendarDay, 0, len(keys))
	for _, k := range keys {
		out = append(out, CalendarDay{Date: k, Events: days[k]})
	}
	return out, nil
}

type FilterOptions struct {
	Cities []string `json:"cities"`
	Genres []string `json:"genres"`
}

func (c *CatalogService) Filters(ctx context.Context) (*FilterOptions, error) {
	cities, err := c.s.Q.ListCities(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := c.s.Pool.Query(ctx, `SELECT DISTINCT genre FROM events WHERE status IN ('published','paused') AND visibility='public' AND genre IS NOT NULL AND genre <> '' ORDER BY genre`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	genres := []string{}
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return nil, err
		}
		genres = append(genres, g)
	}
	if cities == nil {
		cities = []string{}
	}
	return &FilterOptions{Cities: cities, Genres: genres}, rows.Err()
}

// ArtistEvents: event publik mendatang milik artis.
func (c *CatalogService) ArtistEvents(ctx context.Context, artistID string) ([]EventCard, error) {
	rows, err := c.s.Pool.Query(ctx, `SELECT ea.event_id FROM event_artists ea JOIN events e ON e.id = ea.event_id
		WHERE ea.artist_id = $1 AND e.status IN ('published','paused') AND e.visibility='public' ORDER BY e.starts_at`, artistID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return c.CardsByIDs(ctx, ids)
}
