package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/platform"
	"tixgo/backend/internal/repository/sqlcgen"
)

// EventService = manajemen event oleh organizer + moderasi admin (docs/06 M11, 20 M37, 11 §6).
type EventService struct{ s *Services }

func newEventService(s *Services) *EventService { return &EventService{s: s} }

type EventInput struct {
	VenueID         *string       `json:"venue_id" binding:"omitempty,len=26"`
	Title           *string       `json:"title" binding:"omitempty,max=200"`
	Description     *string       `json:"description"`
	Genre           *string       `json:"genre" binding:"omitempty,max=40"`
	PosterAssetID   *string       `json:"poster_asset_id" binding:"omitempty,len=26"`
	StartsAt        *time.Time    `json:"starts_at"`
	DoorsAt         *time.Time    `json:"doors_at"`
	EndsAt          *time.Time    `json:"ends_at"`
	Mode            *string       `json:"mode" binding:"omitempty,oneof=seated general"`
	QueueRequired   *bool         `json:"queue_required"`
	AdmitWindowSec  *int          `json:"admit_window_sec"`
	AdmitRate       *int          `json:"admit_rate"`
	MaxConcurrent   *int          `json:"max_concurrent"`
	QueuePreOpenMin *int          `json:"queue_pre_open_min"`
	HoldTTLSec      *int          `json:"hold_ttl_sec"`
	MaxPerOrder     *int          `json:"max_per_order"`
	MaxPerAccount   *int          `json:"max_per_account"`
	AgeMin          *int          `json:"age_min"`
	RefundPolicy    *RefundPolicy `json:"refund_policy"`
	ServiceFeeBP    *int          `json:"service_fee_bp"`
	ServiceFeeFixed *int64        `json:"service_fee_fixed"`
	FeePayer        *string       `json:"fee_payer" binding:"omitempty,oneof=buyer organizer"`
	TaxBP           *int          `json:"tax_bp"`
	Visibility      *string       `json:"visibility" binding:"omitempty,oneof=public unlisted password"`
	Password        *string       `json:"password" binding:"omitempty,max=128"`
	PublishAt       *time.Time    `json:"publish_at"`
	ArtistIDs       *[]string     `json:"artist_ids" binding:"omitempty,max=30,dive,len=26"`
}

type EventView struct {
	ID                string       `json:"id"`
	OrganizerID       string       `json:"organizer_id"`
	Slug              string       `json:"slug"`
	Title             string       `json:"title"`
	Description       *string      `json:"description"`
	Genre             *string      `json:"genre"`
	PosterURL         string       `json:"poster_url,omitempty"`
	Venue             *VenueView   `json:"venue"`
	StartsAt          time.Time    `json:"starts_at"`
	DoorsAt           *time.Time   `json:"doors_at"`
	EndsAt            *time.Time   `json:"ends_at"`
	Mode              string       `json:"mode"`
	Status            string       `json:"status"`
	ScheduleState     string       `json:"schedule_state"`
	PreviousStartsAt  *time.Time   `json:"previous_starts_at,omitempty"`
	Visibility        string       `json:"visibility"`
	QueueRequired     bool         `json:"queue_required"`
	AdmitWindowSec    int          `json:"admit_window_sec"`
	AdmitRate         int          `json:"admit_rate"`
	MaxConcurrent     int          `json:"max_concurrent"`
	QueuePreOpenMin   int          `json:"queue_pre_open_min"`
	HoldTTLSec        int          `json:"hold_ttl_sec"`
	MaxPerOrder       int          `json:"max_per_order"`
	MaxPerAccount     int          `json:"max_per_account"`
	AgeMin            *int         `json:"age_min"`
	RefundPolicy      RefundPolicy `json:"refund_policy"`
	ServiceFeeBP      int          `json:"service_fee_bp"`
	ServiceFeeFixed   int64        `json:"service_fee_fixed"`
	FeePayer          string       `json:"fee_payer"`
	TaxBP             int          `json:"tax_bp"`
	SalesPaused       bool         `json:"sales_paused"`
	SalesPausedReason *string      `json:"sales_paused_reason,omitempty"`
	PublishAt         *time.Time   `json:"publish_at"`
	PublishedAt       *time.Time   `json:"published_at"`
	ReviewNote        *string      `json:"review_note,omitempty"`
	Artists           []ArtistView `json:"artists"`
	Tiers             []TierView   `json:"tiers"`
	Phases            []PhaseView  `json:"phases"`
	CreatedAt         time.Time    `json:"created_at"`
}

// ---------- validasi ----------

// Statuses
const (
	stDraft     = "draft"
	stInReview  = "in_review"
	stApproved  = "approved"
	stPublished = "published"
	stPaused    = "paused"
	stCancelled = "cancelled"
	stFinished  = "finished"
)

// field yang BOLEH berubah setelah event terbit (docs/20 M37 AC7: harga/tanggal lewat jalur khusus).
var publishedEditable = map[string]bool{
	"description": true, "genre": true, "poster_key": true, "doors_at": true, "max_per_order": true,
	"max_per_account": true, "admit_rate": true, "max_concurrent": true, "admit_window_sec": true,
	"queue_pre_open_min": true, "hold_ttl_sec": true, "visibility": true, "password_hash": true,
	"service_fee_bp": true, "service_fee_fixed": true, "fee_payer": true, "refund_policy": true, "queue_required": true,
}

// field sensitif: mengubahnya pada event "approved" mengembalikan ke draft (perlu tinjau ulang).
var sensitiveFields = map[string]bool{
	"title": true, "venue_id": true, "starts_at": true, "ends_at": true, "mode": true, "service_fee_bp": true,
	"service_fee_fixed": true, "fee_payer": true, "refund_policy": true, "age_min": true,
}

// field yang hanya boleh diubah pemilik organizer (anggota biasa tanpa hak harga/kebijakan; docs/12 §2).
var ownerOnlyFields = map[string]bool{
	"service_fee_bp": true, "service_fee_fixed": true, "fee_payer": true, "refund_policy": true, "tax_bp": true,
}

var eventColumns = map[string]bool{
	"venue_id": true, "title": true, "description": true, "genre": true, "poster_key": true, "starts_at": true, "doors_at": true,
	"ends_at": true, "mode": true, "queue_required": true, "admit_window_sec": true, "admit_rate": true, "max_concurrent": true,
	"queue_pre_open_min": true, "hold_ttl_sec": true, "max_per_order": true, "max_per_account": true, "age_min": true,
	"refund_policy": true, "service_fee_bp": true, "service_fee_fixed": true, "fee_payer": true, "tax_bp": true,
	"visibility": true, "password_hash": true, "publish_at": true, "status": true, "review_note": true,
}

func between(name string, v *int, lo, hi int) error {
	if v != nil && (*v < lo || *v > hi) {
		return apperr.Validation(fmt.Sprintf("%s harus %d–%d", name, lo, hi)).With("field", name)
	}
	return nil
}

func (e *EventService) validateInput(in EventInput) error {
	if in.Title != nil {
		if l := len([]rune(strings.TrimSpace(*in.Title))); l < 3 || l > 200 {
			return apperr.Validation("judul 3–200 karakter").With("field", "title")
		}
	}
	if in.Description != nil && len(*in.Description) > 20000 {
		return apperr.Validation("deskripsi maksimal 20.000 karakter").With("field", "description")
	}
	if in.Mode != nil && *in.Mode != "seated" && *in.Mode != "general" {
		return apperr.Validation("mode harus seated atau general").With("field", "mode")
	}
	if in.Visibility != nil && *in.Visibility != "public" && *in.Visibility != "unlisted" && *in.Visibility != "password" {
		return apperr.Validation("visibility harus public, unlisted, atau password").With("field", "visibility")
	}
	if in.FeePayer != nil && *in.FeePayer != "buyer" && *in.FeePayer != "organizer" {
		return apperr.Validation("fee_payer harus buyer atau organizer").With("field", "fee_payer")
	}
	if in.TaxBP != nil && *in.TaxBP != 0 {
		return apperr.Unprocessable(apperr.CodeValidation, "pajak belum didukung: menunggu keputusan konsultan pajak (Q13)").With("field", "tax_bp")
	}
	if in.ServiceFeeFixed != nil && (*in.ServiceFeeFixed < 0 || *in.ServiceFeeFixed > 1_000_000) {
		return apperr.Validation("service_fee_fixed 0–1.000.000").With("field", "service_fee_fixed")
	}
	for _, c := range []struct {
		n      string
		v      *int
		lo, hi int
	}{
		{"admit_window_sec", in.AdmitWindowSec, 30, 3600}, {"admit_rate", in.AdmitRate, 1, 5000},
		{"max_concurrent", in.MaxConcurrent, 1, 100000}, {"queue_pre_open_min", in.QueuePreOpenMin, 0, 1440},
		{"hold_ttl_sec", in.HoldTTLSec, 60, 3600}, {"max_per_order", in.MaxPerOrder, 1, 20},
		{"max_per_account", in.MaxPerAccount, 1, 50}, {"age_min", in.AgeMin, 0, 99}, {"service_fee_bp", in.ServiceFeeBP, 0, 5000},
	} {
		if err := between(c.n, c.v, c.lo, c.hi); err != nil {
			return err
		}
	}
	if in.Visibility != nil && *in.Visibility == "password" && (in.Password == nil || len(*in.Password) < 6) {
		return apperr.Validation("password event minimal 6 karakter").With("field", "password")
	}
	return nil
}

func slugFor(title string) string {
	return Slugify(title) + "-" + strings.ToLower(auth.RandomCode(5))
}

// ownEvent memuat event dan memastikan milik organizer pemanggil (selain itu → 404, tidak membocorkan keberadaan).
func (e *EventService) ownEvent(ctx context.Context, q *sqlcgen.Queries, sc OrgScope, eventID string) (sqlcgen.Event, error) {
	ev, err := q.GetEvent(ctx, eventID)
	if err != nil {
		if platform.IsNoRows(err) {
			return ev, apperr.NotFound("event")
		}
		return ev, err
	}
	if ev.OrganizerID != sc.OrganizerID {
		return ev, apperr.NotFound("event")
	}
	return ev, nil
}

func (e *EventService) requireOrgActive(ctx context.Context, q *sqlcgen.Queries, orgID string) (sqlcgen.Organizer, error) {
	org, err := q.GetOrganizer(ctx, orgID)
	if err != nil {
		return org, err
	}
	if org.Status != "active" {
		return org, apperr.ForbiddenCode(apperr.CodeBlocked, "organizer ditangguhkan")
	}
	return org, nil
}

// ---------- Create ----------

func (e *EventService) Create(ctx context.Context, p *auth.Principal, sc OrgScope, in EventInput) (*EventView, error) {
	if sc.Role == "gate_staff" {
		return nil, apperr.Forbidden("peran gate tidak dapat membuat event")
	}
	if err := e.validateInput(in); err != nil {
		return nil, err
	}
	if in.VenueID == nil || in.Title == nil || in.StartsAt == nil || in.Mode == nil {
		return nil, apperr.Validation("venue_id, title, starts_at, dan mode wajib")
	}
	if in.StartsAt.Before(e.s.now()) {
		return nil, apperr.Validation("starts_at harus di masa depan").With("field", "starts_at")
	}
	if err := checkTimes(*in.StartsAt, in.DoorsAt, in.EndsAt); err != nil {
		return nil, err
	}
	if !sc.IsOwner() && (in.RefundPolicy != nil || in.ServiceFeeBP != nil || in.ServiceFeeFixed != nil || in.FeePayer != nil) {
		return nil, apperr.Forbidden("harga, biaya, dan kebijakan refund hanya dapat diatur pemilik organizer")
	}
	policy := DefaultRefundPolicy(e.s.Cfg.RefundRescheduleWindowDays)
	if in.RefundPolicy != nil {
		policy = *in.RefundPolicy
		if policy.OnReschedule.WindowDays == 0 {
			policy.OnReschedule.WindowDays = e.s.Cfg.RefundRescheduleWindowDays
		}
	}
	if err := policy.Validate(*in.StartsAt); err != nil {
		return nil, err
	}
	var out sqlcgen.Event
	err := e.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := e.requireOrgActive(ctx, q, sc.OrganizerID); err != nil {
			return err
		}
		if _, err := q.GetVenue(ctx, *in.VenueID); err != nil {
			return apperr.Validation("venue tidak ditemukan").With("field", "venue_id")
		}
		poster := (*string)(nil)
		if in.PosterAssetID != nil {
			if err := e.s.Media.Owned(ctx, p.UserID, *in.PosterAssetID, "poster"); err != nil {
				return err
			}
			poster = in.PosterAssetID
		}
		var pwHash *string
		if in.Password != nil && *in.Password != "" {
			h := e.s.Hasher.Hash(*in.Password)
			pwHash = &h
		}
		vis := derefOr(in.Visibility, "public")
		if vis != "password" {
			pwHash = nil
		}
		arg := sqlcgen.InsertEventParams{
			ID: id.New(), OrganizerID: sc.OrganizerID, VenueID: *in.VenueID, Title: strings.TrimSpace(*in.Title),
			Description: in.Description, Genre: in.Genre, PosterKey: poster, StartsAt: *in.StartsAt, DoorsAt: in.DoorsAt, EndsAt: in.EndsAt,
			Mode: *in.Mode, QueueRequired: derefOr(in.QueueRequired, false),
			AdmitWindowSec: derefOr(in.AdmitWindowSec, e.s.Cfg.QueueAdmitWindowSec), AdmitRate: derefOr(in.AdmitRate, e.s.Cfg.QueueAdmitRate),
			MaxConcurrent: derefOr(in.MaxConcurrent, 500), QueuePreOpenMin: derefOr(in.QueuePreOpenMin, e.s.Cfg.QueuePreOpenMin),
			HoldTtlSec: derefOr(in.HoldTTLSec, e.s.Cfg.HoldTTLSec), MaxPerOrder: derefOr(in.MaxPerOrder, 6), MaxPerAccount: derefOr(in.MaxPerAccount, 4),
			AgeMin: in.AgeMin, RefundPolicy: policy.JSON(), ServiceFeeBp: derefOr(in.ServiceFeeBP, 0), ServiceFeeFixed: derefOr(in.ServiceFeeFixed, int64(0)),
			FeePayer: derefOr(in.FeePayer, "buyer"), TaxBp: 0, Visibility: vis, PasswordHash: pwHash, PublishAt: in.PublishAt, CreatedBy: strPtr(p.UserID),
		}
		var err error
		for i := 0; i < 5; i++ {
			arg.Slug = slugFor(arg.Title)
			out, err = q.InsertEvent(ctx, arg)
			if err == nil || !platform.IsUniqueViolation(err) {
				break
			}
		}
		if err != nil {
			return err
		}
		if in.ArtistIDs != nil {
			if err := e.setArtists(ctx, q, out.ID, *in.ArtistIDs); err != nil {
				return err
			}
		}
		return e.s.Audit.Log(ctx, q, p, "event.create", "event", out.ID, nil, map[string]any{"title": out.Title, "mode": out.Mode})
	})
	if err != nil {
		return nil, err
	}
	return e.view(ctx, e.s.Q, out)
}

func derefOr[T any](p *T, def T) T {
	if p == nil {
		return def
	}
	return *p
}

func checkTimes(starts time.Time, doors, ends *time.Time) error {
	if doors != nil && doors.After(starts) {
		return apperr.Validation("doors_at tidak boleh setelah starts_at").With("field", "doors_at")
	}
	if ends != nil && !ends.After(starts) {
		return apperr.Validation("ends_at harus setelah starts_at").With("field", "ends_at")
	}
	return nil
}

func (e *EventService) setArtists(ctx context.Context, q *sqlcgen.Queries, eventID string, artistIDs []string) error {
	if len(artistIDs) > 30 {
		return apperr.Validation("maksimal 30 artis").With("field", "artist_ids")
	}
	if err := q.ClearEventArtists(ctx, eventID); err != nil {
		return err
	}
	seen := map[string]bool{}
	for i, aid := range artistIDs {
		if seen[aid] {
			continue
		}
		seen[aid] = true
		if _, err := q.GetArtist(ctx, aid); err != nil {
			return apperr.Validation("artis tidak ditemukan").With("field", "artist_ids").With("artist_id", aid)
		}
		if err := q.AddEventArtist(ctx, sqlcgen.AddEventArtistParams{EventID: eventID, ArtistID: aid, BillingOrder: i}); err != nil {
			return err
		}
	}
	return nil
}

// ---------- Update ----------

func (e *EventService) Update(ctx context.Context, p *auth.Principal, sc OrgScope, eventID string, in EventInput) (*EventView, error) {
	if sc.Role == "gate_staff" {
		return nil, apperr.Forbidden("peran gate tidak dapat mengubah event")
	}
	if err := e.validateInput(in); err != nil {
		return nil, err
	}
	var out sqlcgen.Event
	err := e.s.InTxRaw(ctx, func(q *sqlcgen.Queries, tx pgxTx) error {
		ev, err := q.GetEventForUpdate(ctx, eventID)
		if err != nil || ev.OrganizerID != sc.OrganizerID {
			return apperr.NotFound("event")
		}
		if _, err := e.requireOrgActive(ctx, q, sc.OrganizerID); err != nil {
			return err
		}
		if ev.Status == stInReview {
			return apperr.Conflict(apperr.CodeInvalidState, "event sedang ditinjau; tarik pengajuan dulu untuk mengubah")
		}
		if ev.Status == stCancelled || ev.Status == stFinished {
			return apperr.Conflict(apperr.CodeInvalidState, "event "+ev.Status+" tidak dapat diubah")
		}
		fields := map[string]any{}
		start := ev.StartsAt
		if in.Title != nil {
			fields["title"] = strings.TrimSpace(*in.Title)
		}
		if in.Description != nil {
			fields["description"] = *in.Description
		}
		if in.Genre != nil {
			fields["genre"] = *in.Genre
		}
		if in.VenueID != nil && *in.VenueID != ev.VenueID {
			if _, err := q.GetVenue(ctx, *in.VenueID); err != nil {
				return apperr.Validation("venue tidak ditemukan").With("field", "venue_id")
			}
			fields["venue_id"] = *in.VenueID
		}
		if in.Mode != nil && *in.Mode != ev.Mode {
			n, _ := q.CountEventSeats(ctx, eventID)
			tiers, _ := q.ListTiers(ctx, eventID)
			if n > 0 || len(tiers) > 0 {
				return apperr.Conflict(apperr.CodeInvalidState, "mode tidak dapat diubah setelah ada tier/kursi")
			}
			fields["mode"] = *in.Mode
		}
		if in.StartsAt != nil && !in.StartsAt.Equal(ev.StartsAt) {
			if in.StartsAt.Before(e.s.now()) {
				return apperr.Validation("starts_at harus di masa depan").With("field", "starts_at")
			}
			fields["starts_at"] = *in.StartsAt
			start = *in.StartsAt
		}
		doors, ends := ev.DoorsAt, ev.EndsAt
		if in.DoorsAt != nil {
			fields["doors_at"], doors = *in.DoorsAt, in.DoorsAt
		}
		if in.EndsAt != nil {
			fields["ends_at"], ends = *in.EndsAt, in.EndsAt
		}
		if err := checkTimes(start, doors, ends); err != nil {
			return err
		}
		setInt := func(col string, v *int) {
			if v != nil {
				fields[col] = *v
			}
		}
		if in.QueueRequired != nil {
			fields["queue_required"] = *in.QueueRequired
		}
		setInt("admit_window_sec", in.AdmitWindowSec)
		setInt("admit_rate", in.AdmitRate)
		setInt("max_concurrent", in.MaxConcurrent)
		setInt("queue_pre_open_min", in.QueuePreOpenMin)
		setInt("hold_ttl_sec", in.HoldTTLSec)
		setInt("max_per_order", in.MaxPerOrder)
		setInt("max_per_account", in.MaxPerAccount)
		setInt("age_min", in.AgeMin)
		setInt("service_fee_bp", in.ServiceFeeBP)
		if in.ServiceFeeFixed != nil {
			fields["service_fee_fixed"] = *in.ServiceFeeFixed
		}
		if in.FeePayer != nil {
			fields["fee_payer"] = *in.FeePayer
		}
		if in.RefundPolicy != nil {
			pol := *in.RefundPolicy
			if pol.OnReschedule.WindowDays == 0 {
				pol.OnReschedule.WindowDays = e.s.Cfg.RefundRescheduleWindowDays
			}
			if err := pol.Validate(start); err != nil {
				return err
			}
			fields["refund_policy"] = pol.JSON()
		} else if in.StartsAt != nil {
			if err := ParseRefundPolicy(ev.RefundPolicy, e.s.Cfg.RefundRescheduleWindowDays).Validate(start); err != nil {
				return err
			}
		}
		if in.PosterAssetID != nil {
			if err := e.s.Media.Owned(ctx, p.UserID, *in.PosterAssetID, "poster"); err != nil {
				return err
			}
			fields["poster_key"] = *in.PosterAssetID
		}
		if in.PublishAt != nil {
			fields["publish_at"] = *in.PublishAt
		}
		if in.Visibility != nil {
			fields["visibility"] = *in.Visibility
			if *in.Visibility == "password" {
				if in.Password == nil {
					return apperr.Validation("password wajib untuk visibility password").With("field", "password")
				}
				fields["password_hash"] = e.s.Hasher.Hash(*in.Password)
			} else {
				fields["password_hash"] = nil
			}
		} else if in.Password != nil && ev.Visibility == "password" {
			fields["password_hash"] = e.s.Hasher.Hash(*in.Password)
		}
		// aturan per status
		if !sc.IsOwner() {
			for f := range fields {
				if ownerOnlyFields[f] {
					return apperr.Forbidden("harga, biaya, dan kebijakan refund hanya dapat diatur pemilik organizer")
				}
			}
		}
		if ev.Status == stPublished || ev.Status == stPaused {
			var bad []string
			for f := range fields {
				if !publishedEditable[f] {
					bad = append(bad, f)
				}
			}
			if len(bad) > 0 {
				return apperr.Conflict(apperr.CodeInvalidState, "field ini tidak dapat diubah setelah terbit (gunakan jadwal ulang/tier)").With("fields", bad)
			}
		}
		if ev.Status == stApproved {
			for f := range fields {
				if sensitiveFields[f] {
					fields["status"] = stDraft
					fields["review_note"] = "perubahan pada data sensitif; ajukan ulang untuk ditinjau"
					break
				}
			}
		}
		if len(fields) > 0 {
			if _, err := dynUpdate(ctx, tx, "events", "id", eventID, eventColumns, fields, true); err != nil {
				return err
			}
		}
		if in.ArtistIDs != nil {
			if err := e.setArtists(ctx, q, eventID, *in.ArtistIDs); err != nil {
				return err
			}
		}
		out, err = q.GetEvent(ctx, eventID)
		if err != nil {
			return err
		}
		names := make([]string, 0, len(fields))
		for f := range fields {
			names = append(names, f)
		}
		return e.s.Audit.Log(ctx, q, p, "event.update", "event", eventID, nil, map[string]any{"fields": names})
	})
	if err != nil {
		return nil, err
	}
	return e.view(ctx, e.s.Q, out)
}

// ---------- Baca ----------

func (e *EventService) Get(ctx context.Context, sc OrgScope, eventID string) (*EventView, error) {
	ev, err := e.ownEvent(ctx, e.s.Q, sc, eventID)
	if err != nil {
		return nil, err
	}
	return e.view(ctx, e.s.Q, ev)
}

func (e *EventService) List(ctx context.Context, sc OrgScope, status, q string, limit, offset int) ([]EventView, int, error) {
	rows, err := e.s.Q.ListOrganizerEvents(ctx, sqlcgen.ListOrganizerEventsParams{Org: sc.OrganizerID, Status: strPtr(status), Q: strPtr(q), Lim: int32(limit), Off: int32(offset)})
	if err != nil {
		return nil, 0, err
	}
	n, err := e.s.Q.CountOrganizerEvents(ctx, sqlcgen.CountOrganizerEventsParams{Org: sc.OrganizerID, Status: strPtr(status), Q: strPtr(q)})
	if err != nil {
		return nil, 0, err
	}
	out := make([]EventView, 0, len(rows))
	for _, r := range rows {
		v, err := e.view(ctx, e.s.Q, r)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *v)
	}
	return out, int(n), nil
}

func (e *EventService) view(ctx context.Context, q *sqlcgen.Queries, ev sqlcgen.Event) (*EventView, error) {
	v := &EventView{
		ID: ev.ID, OrganizerID: ev.OrganizerID, Slug: ev.Slug, Title: ev.Title, Description: ev.Description, Genre: ev.Genre,
		StartsAt: ev.StartsAt, DoorsAt: ev.DoorsAt, EndsAt: ev.EndsAt, Mode: ev.Mode, Status: ev.Status, ScheduleState: ev.ScheduleState,
		PreviousStartsAt: ev.PreviousStartsAt, Visibility: ev.Visibility, QueueRequired: ev.QueueRequired, AdmitWindowSec: ev.AdmitWindowSec,
		AdmitRate: ev.AdmitRate, MaxConcurrent: ev.MaxConcurrent, QueuePreOpenMin: ev.QueuePreOpenMin, HoldTTLSec: ev.HoldTtlSec,
		MaxPerOrder: ev.MaxPerOrder, MaxPerAccount: ev.MaxPerAccount, AgeMin: ev.AgeMin,
		RefundPolicy: ParseRefundPolicy(ev.RefundPolicy, e.s.Cfg.RefundRescheduleWindowDays), ServiceFeeBP: ev.ServiceFeeBp,
		ServiceFeeFixed: ev.ServiceFeeFixed, FeePayer: ev.FeePayer, TaxBP: ev.TaxBp, SalesPaused: ev.SalesPaused,
		SalesPausedReason: ev.SalesPausedReason, PublishAt: ev.PublishAt, PublishedAt: ev.PublishedAt, ReviewNote: ev.ReviewNote, CreatedAt: ev.CreatedAt,
	}
	if ev.PosterKey != nil {
		v.PosterURL = MediaURL(*ev.PosterKey, 800)
	}
	if ven, err := q.GetVenue(ctx, ev.VenueID); err == nil {
		vv := venueView(ven)
		v.Venue = &vv
	}
	arts, err := q.ListEventArtists(ctx, ev.ID)
	if err != nil {
		return nil, err
	}
	v.Artists = make([]ArtistView, 0, len(arts))
	for _, a := range arts {
		av := ArtistView{ID: a.ID, Slug: a.Slug, Name: a.Name, Bio: a.Bio}
		if a.ImageKey != nil {
			av.ImageURL = MediaURL(*a.ImageKey, 800)
		}
		v.Artists = append(v.Artists, av)
	}
	if v.Tiers, err = e.s.Tier.list(ctx, q, ev.ID); err != nil {
		return nil, err
	}
	if v.Phases, err = e.s.Phase.list(ctx, q, ev.ID); err != nil {
		return nil, err
	}
	return v, nil
}

// ---------- Hapus & siklus status ----------

func (e *EventService) Delete(ctx context.Context, p *auth.Principal, sc OrgScope, eventID string) error {
	if !sc.IsOwner() {
		return apperr.Forbidden("hanya pemilik organizer")
	}
	return e.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		ev, err := e.ownEvent(ctx, q, sc, eventID)
		if err != nil {
			return err
		}
		if ev.Status != stDraft {
			return apperr.Conflict(apperr.CodeInvalidState, "hanya event draft yang dapat dihapus")
		}
		if err := q.DeleteEvent(ctx, eventID); err != nil {
			if platform.IsForeignKeyViolation(err) {
				return apperr.Conflict(apperr.CodeInvalidState, "event sudah memiliki data terkait")
			}
			return err
		}
		return e.s.Audit.Log(ctx, q, p, "event.delete", "event", eventID, map[string]any{"title": ev.Title}, nil)
	})
}

// readiness memeriksa kelengkapan sebelum diajukan (docs/20 M37 AC6).
func (e *EventService) readiness(ctx context.Context, q *sqlcgen.Queries, ev sqlcgen.Event, org sqlcgen.Organizer) ([]string, error) {
	var missing []string
	if org.Status != "active" {
		missing = append(missing, "organizer_active")
	}
	if !ev.StartsAt.After(e.s.now()) {
		missing = append(missing, "starts_at_future")
	}
	tiers, err := q.ListTiers(ctx, ev.ID)
	if err != nil {
		return nil, err
	}
	if len(tiers) == 0 {
		missing = append(missing, "tiers")
	}
	paid := false
	if ev.Mode == "general" {
		for _, t := range tiers {
			if t.Quota <= 0 {
				missing = append(missing, "tier_quota:"+t.Name)
			}
		}
	} else {
		n, err := q.CountEventSeats(ctx, ev.ID)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			missing = append(missing, "seatmap")
		}
		avail, err := q.TierAvailability(ctx, ev.ID)
		if err != nil {
			return nil, err
		}
		for _, a := range avail {
			if a.Capacity == 0 {
				missing = append(missing, "tier_seats:"+a.Name)
			}
		}
	}
	for _, t := range tiers {
		if t.Price > 0 {
			paid = true
		}
	}
	if paid && org.KybStatus != "approved" {
		missing = append(missing, "kyb_approved")
	}
	pol := ParseRefundPolicy(ev.RefundPolicy, e.s.Cfg.RefundRescheduleWindowDays)
	if err := pol.Validate(ev.StartsAt); err != nil {
		missing = append(missing, "refund_policy")
	}
	return missing, nil
}

func (e *EventService) Submit(ctx context.Context, p *auth.Principal, sc OrgScope, eventID string) (*EventView, error) {
	if !sc.IsOwner() {
		return nil, apperr.Forbidden("hanya pemilik organizer yang dapat mengajukan")
	}
	var out sqlcgen.Event
	err := e.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		ev, err := q.GetEventForUpdate(ctx, eventID)
		if err != nil || ev.OrganizerID != sc.OrganizerID {
			return apperr.NotFound("event")
		}
		if ev.Status != stDraft {
			return apperr.Conflict(apperr.CodeInvalidState, "hanya event draft yang dapat diajukan")
		}
		org, err := e.requireOrgActive(ctx, q, sc.OrganizerID)
		if err != nil {
			return err
		}
		missing, err := e.readiness(ctx, q, ev, org)
		if err != nil {
			return err
		}
		if len(missing) > 0 {
			return apperr.Unprocessable(apperr.CodeValidation, "event belum lengkap untuk diajukan").With("missing", missing)
		}
		out, err = q.SetEventStatus(ctx, sqlcgen.SetEventStatusParams{ID: eventID, Status: stInReview})
		if err != nil {
			return err
		}
		return e.s.Audit.Log(ctx, q, p, "event.submit", "event", eventID, map[string]any{"from": ev.Status}, map[string]any{"to": stInReview})
	})
	if err != nil {
		return nil, err
	}
	return e.view(ctx, e.s.Q, out)
}

func (e *EventService) Withdraw(ctx context.Context, p *auth.Principal, sc OrgScope, eventID string) (*EventView, error) {
	var out sqlcgen.Event
	err := e.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		ev, err := q.GetEventForUpdate(ctx, eventID)
		if err != nil || ev.OrganizerID != sc.OrganizerID {
			return apperr.NotFound("event")
		}
		if ev.Status != stInReview {
			return apperr.Conflict(apperr.CodeInvalidState, "event tidak sedang ditinjau")
		}
		out, err = q.SetEventStatus(ctx, sqlcgen.SetEventStatusParams{ID: eventID, Status: stDraft})
		if err != nil {
			return err
		}
		return e.s.Audit.Log(ctx, q, p, "event.withdraw", "event", eventID, nil, nil)
	})
	if err != nil {
		return nil, err
	}
	return e.view(ctx, e.s.Q, out)
}

// AdminReview: approve → approved; reject → kembali ke draft + catatan (docs/20 M12 AC1).
func (e *EventService) AdminReview(ctx context.Context, p *auth.Principal, eventID, decision, note string) (*EventView, error) {
	if decision != "approve" && decision != "reject" {
		return nil, apperr.Validation("decision harus approve atau reject").With("field", "decision")
	}
	if decision == "reject" && strings.TrimSpace(note) == "" {
		return nil, apperr.Validation("alasan penolakan wajib").With("field", "note")
	}
	var out sqlcgen.Event
	var ownerID string
	err := e.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		ev, err := q.GetEventForUpdate(ctx, eventID)
		if err != nil {
			return apperr.NotFound("event")
		}
		if ev.Status != stInReview {
			return apperr.Conflict(apperr.CodeInvalidState, "event tidak dalam status in_review")
		}
		to := stApproved
		if decision == "reject" {
			to = stDraft
		}
		out, err = q.SetEventStatus(ctx, sqlcgen.SetEventStatusParams{ID: eventID, Status: to, Note: strPtr(note)})
		if err != nil {
			return err
		}
		org, err := q.GetOrganizer(ctx, ev.OrganizerID)
		if err != nil {
			return err
		}
		ownerID = org.OwnerUserID
		return e.s.Audit.Log(ctx, q, p, "event.review", "event", eventID, nil, map[string]any{"decision": decision, "note": note})
	})
	if err != nil {
		return nil, err
	}
	e.s.Notifier.Send(ctx, NotifyReq{UserID: ownerID, Template: "event_review_update", Data: map[string]any{
		"name": "", "event_title": out.Title, "status": out.Status, "note": note}})
	return e.view(ctx, e.s.Q, out)
}

// Publish: approved → published (pemilik). Bila belum ada fase penjualan, dibuat fase publik default.
func (e *EventService) Publish(ctx context.Context, p *auth.Principal, sc OrgScope, eventID string) (*EventView, error) {
	if !sc.IsOwner() {
		return nil, apperr.Forbidden("hanya pemilik organizer")
	}
	return e.publish(ctx, p, sc.OrganizerID, eventID)
}

func (e *EventService) publish(ctx context.Context, p *auth.Principal, orgID, eventID string) (*EventView, error) {
	var out sqlcgen.Event
	err := e.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		ev, err := q.GetEventForUpdate(ctx, eventID)
		if err != nil || (orgID != "" && ev.OrganizerID != orgID) {
			return apperr.NotFound("event")
		}
		if ev.Status != stApproved {
			return apperr.Conflict(apperr.CodeInvalidState, "event harus berstatus approved untuk diterbitkan")
		}
		org, err := e.requireOrgActive(ctx, q, ev.OrganizerID)
		if err != nil {
			return err
		}
		missing, err := e.readiness(ctx, q, ev, org)
		if err != nil {
			return err
		}
		if len(missing) > 0 {
			return apperr.Unprocessable(apperr.CodeValidation, "event belum lengkap untuk diterbitkan").With("missing", missing)
		}
		phases, err := q.ListPhases(ctx, eventID)
		if err != nil {
			return err
		}
		if len(phases) == 0 {
			now := e.s.now()
			end := ev.StartsAt
			if ev.EndsAt != nil {
				end = *ev.EndsAt
			}
			if _, err := q.InsertPhase(ctx, sqlcgen.InsertPhaseParams{ID: id.New(), EventID: eventID, Name: "Penjualan umum", Audience: "public", StartsAt: now, EndsAt: end}); err != nil {
				return err
			}
		}
		out, err = q.SetEventStatus(ctx, sqlcgen.SetEventStatusParams{ID: eventID, Status: stPublished})
		if err != nil {
			return err
		}
		return e.s.Audit.Log(ctx, q, p, "event.publish", "event", eventID, nil, nil)
	})
	if err != nil {
		return nil, err
	}
	e.s.Follow.NotifyNewEvent(ctx, out)
	return e.view(ctx, e.s.Q, out)
}

// RunScheduledPublish (worker): menerbitkan event approved yang publish_at-nya tiba.
func (e *EventService) RunScheduledPublish(ctx context.Context) (int, error) {
	rows, err := e.s.Q.ListScheduledPublish(ctx, ptr(e.s.now()))
	if err != nil {
		return 0, err
	}
	n := 0
	for _, ev := range rows {
		if _, err := e.publish(ctx, nil, ev.OrganizerID, ev.ID); err != nil {
			e.s.Log.Error("publish terjadwal gagal", "event", ev.ID, "error", err)
			continue
		}
		n++
	}
	return n, nil
}

// RunFinish (worker): event yang sudah lewat → finished.
func (e *EventService) RunFinish(ctx context.Context) (int, error) {
	ids, err := e.s.Q.ListEventsToFinish(ctx, ptr(e.s.now()))
	if err != nil {
		return 0, err
	}
	for _, eid := range ids {
		if _, err := e.s.Q.SetEventStatus(ctx, sqlcgen.SetEventStatusParams{ID: eid, Status: stFinished}); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

// AdminList untuk antrean moderasi.
func (e *EventService) AdminList(ctx context.Context, status string, limit, offset int) ([]EventView, int, error) {
	rows, err := e.s.Q.ListEventsForReview(ctx, sqlcgen.ListEventsForReviewParams{Status: strPtr(status), Lim: int32(limit), Off: int32(offset)})
	if err != nil {
		return nil, 0, err
	}
	n, err := e.s.Q.CountEventsForReview(ctx, strPtr(status))
	if err != nil {
		return nil, 0, err
	}
	out := make([]EventView, 0, len(rows))
	for _, r := range rows {
		v, err := e.view(ctx, e.s.Q, r)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *v)
	}
	return out, int(n), nil
}

func (e *EventService) AdminGet(ctx context.Context, eventID string) (*EventView, error) {
	ev, err := e.s.Q.GetEvent(ctx, eventID)
	if err != nil {
		return nil, apperr.NotFound("event")
	}
	return e.view(ctx, e.s.Q, ev)
}

var _ = json.Marshal
