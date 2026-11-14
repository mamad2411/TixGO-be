package service

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/jobs"
	"tixgo/backend/internal/repository/sqlcgen"
)

// LifecycleService = duplikat, jadwal ulang, tunda, batal (docs/20 M37). Perubahan jadwal/pembatalan hanya owner + MFA.
type LifecycleService struct{ s *Services }

const jobCancelFanout = "event:cancel_fanout"

func newLifecycleService(s *Services) *LifecycleService {
	l := &LifecycleService{s: s}
	s.Reg.Handle(jobCancelFanout, l.handleFanout)
	return l
}

func (l *LifecycleService) guard(p *auth.Principal, sc OrgScope) error {
	if !sc.IsOwner() {
		return apperr.Forbidden("hanya pemilik organizer")
	}
	if !p.MFAPassed {
		return apperr.ForbiddenCode(apperr.CodeMFARequired, "verifikasi dua langkah diperlukan").With("setup", true)
	}
	return nil
}

type DuplicateInput struct {
	Title    string     `json:"title" binding:"omitempty,min=3,max=160"`
	StartsAt time.Time  `json:"starts_at" binding:"required"`
	EndsAt   *time.Time `json:"ends_at"`
}

// Duplicate menyalin info, tier, add-on, denah, dan kebijakan — BUKAN tanggal, fase, penjualan, status (selalu draft).
func (l *LifecycleService) Duplicate(ctx context.Context, p *auth.Principal, sc OrgScope, srcID string, in DuplicateInput) (*EventView, error) {
	if !in.StartsAt.After(l.s.now()) {
		return nil, apperr.Validation("starts_at harus di masa depan").With("field", "starts_at")
	}
	if in.EndsAt != nil && !in.EndsAt.After(in.StartsAt) {
		return nil, apperr.Validation("ends_at harus setelah starts_at").With("field", "ends_at")
	}
	var out sqlcgen.Event
	err := l.s.InTxRaw(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		src, err := l.s.Event.ownEvent(ctx, q, sc, srcID)
		if err != nil {
			return err
		}
		title := in.Title
		if title == "" {
			title = src.Title + " (Salinan)"
		}
		newID := id.New()
		if err := q.InsertEventCopy(ctx, sqlcgen.InsertEventCopyParams{NewID: newID, Slug: slugFor(title), Title: title, StartsAt: in.StartsAt, EndsAt: in.EndsAt, CreatedBy: &p.UserID, SrcID: srcID}); err != nil {
			return err
		}
		tierMap := map[string]string{}
		tiers, err := q.ListTiers(ctx, srcID)
		if err != nil {
			return err
		}
		for _, t := range tiers {
			nid := id.New()
			tierMap[t.ID] = nid
			if _, err := q.InsertTier(ctx, sqlcgen.InsertTierParams{ID: nid, EventID: newID, Name: t.Name, Price: t.Price, Quota: t.Quota, CompQuota: t.CompQuota, Sort: t.Sort, Color: t.Color}); err != nil {
				return err
			}
		}
		ads, err := q.ListAddons(ctx, sqlcgen.ListAddonsParams{EventID: srcID, OnlyActive: true})
		if err != nil {
			return err
		}
		for _, a := range ads {
			if _, err := q.InsertAddon(ctx, sqlcgen.InsertAddonParams{ID: id.New(), EventID: newID, Name: a.Name, Price: a.Price, Quota: a.Quota}); err != nil {
				return err
			}
		}
		secMap := map[string]string{}
		secs, err := q.ListSections(ctx, srcID)
		if err != nil {
			return err
		}
		for _, s := range secs {
			nid := id.New()
			secMap[s.ID] = nid
			var tid *string
			if s.TierID != nil {
				v := tierMap[*s.TierID]
				tid = &v
			}
			if _, err := q.InsertSection(ctx, sqlcgen.InsertSectionParams{ID: nid, EventID: newID, TierID: tid, Name: s.Name, SvgRef: s.SvgRef, Sort: s.Sort}); err != nil {
				return err
			}
		}
		seats, err := q.ListSeatsGeometry(ctx, srcID)
		if err != nil {
			return err
		}
		rows := make([][]any, 0, len(seats))
		for _, s := range seats {
			rows = append(rows, []any{id.New(), newID, secMap[s.SectionID], tierMap[s.TierID], s.RowLabel, s.SeatNo, s.Label, s.X, s.Y, s.Accessible, "available"})
		}
		if len(rows) > 0 {
			if _, err := tx.CopyFrom(ctx, pgx.Identifier{"seats"}, []string{"id", "event_id", "section_id", "tier_id", "row_label", "seat_no", "label", "x", "y", "accessible", "status"}, pgx.CopyFromRows(rows)); err != nil {
				return err
			}
		}
		out, err = q.GetEvent(ctx, newID)
		if err != nil {
			return err
		}
		return l.s.Audit.Log(ctx, q, p, "event.duplicate", "event", newID, nil, map[string]any{"from": srcID})
	})
	if err != nil {
		return nil, err
	}
	return l.s.Event.view(ctx, l.s.Q, out)
}

func (l *LifecycleService) liveEvent(ctx context.Context, q *sqlcgen.Queries, sc OrgScope, eventID string) (sqlcgen.Event, error) {
	ev, err := q.GetEventForUpdate(ctx, eventID)
	if err != nil || ev.OrganizerID != sc.OrganizerID {
		return ev, apperr.NotFound("event")
	}
	if ev.Status != stPublished && ev.Status != stPaused {
		return ev, apperr.Conflict(apperr.CodeInvalidState, "hanya event yang sudah terbit yang bisa diubah jadwalnya")
	}
	return ev, nil
}

type RescheduleInput struct {
	StartsAt time.Time  `json:"starts_at" binding:"required"`
	EndsAt   *time.Time `json:"ends_at"`
	DoorsAt  *time.Time `json:"doors_at"`
}

// Reschedule: tiket tetap berlaku; pembeli dapat refund penuh dalam jendela on_reschedule sejak pengumuman.
func (l *LifecycleService) Reschedule(ctx context.Context, p *auth.Principal, sc OrgScope, eventID string, in RescheduleInput) (*EventView, error) {
	if err := l.guard(p, sc); err != nil {
		return nil, err
	}
	if !in.StartsAt.After(l.s.now()) {
		return nil, apperr.Validation("tanggal baru harus di masa depan").With("field", "starts_at")
	}
	if in.EndsAt != nil && !in.EndsAt.After(in.StartsAt) {
		return nil, apperr.Validation("ends_at harus setelah starts_at").With("field", "ends_at")
	}
	return l.apply(ctx, p, sc, eventID, "rescheduled", "event.reschedule", func(q *sqlcgen.Queries, ev sqlcgen.Event) (sqlcgen.Event, error) {
		return q.SetEventSchedule(ctx, sqlcgen.SetEventScheduleParams{ID: ev.ID, StartsAt: in.StartsAt, EndsAt: in.EndsAt, DoorsAt: in.DoorsAt, State: "rescheduled", ChangedAt: ptr(l.s.now())})
	})
}

// Postpone: tanggal baru belum ditentukan; tiket tetap berlaku, jendela refund berjalan.
func (l *LifecycleService) Postpone(ctx context.Context, p *auth.Principal, sc OrgScope, eventID string) (*EventView, error) {
	if err := l.guard(p, sc); err != nil {
		return nil, err
	}
	return l.apply(ctx, p, sc, eventID, "postponed", "event.postpone", func(q *sqlcgen.Queries, ev sqlcgen.Event) (sqlcgen.Event, error) {
		return q.SetEventScheduleState(ctx, sqlcgen.SetEventScheduleStateParams{ID: ev.ID, State: "postponed", ChangedAt: ptr(l.s.now())})
	})
}

func (l *LifecycleService) apply(ctx context.Context, p *auth.Principal, sc OrgScope, eventID, state, action string, f func(*sqlcgen.Queries, sqlcgen.Event) (sqlcgen.Event, error)) (*EventView, error) {
	var out, before sqlcgen.Event
	err := l.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		ev, err := l.liveEvent(ctx, q, sc, eventID)
		if err != nil {
			return err
		}
		if ev.ScheduleState == "cancelled" {
			return apperr.Conflict(apperr.CodeInvalidState, "event sudah dibatalkan")
		}
		before = ev
		if out, err = f(q, ev); err != nil {
			return err
		}
		if err := l.s.Audit.Log(ctx, q, p, action, "event", ev.ID, map[string]any{"starts_at": before.StartsAt, "state": before.ScheduleState}, map[string]any{"starts_at": out.StartsAt, "state": state}); err != nil {
			return err
		}
		return emit(ctx, q, "event."+ev.ID, "event.schedule_changed", map[string]any{"state": state})
	})
	if err != nil {
		return nil, err
	}
	l.s.Catalog.InvalidateAvailability(ctx, eventID)
	if err := l.s.Jobs.Enqueue(ctx, jobCancelFanout, map[string]string{"event_id": eventID, "kind": state}, jobs.MaxRetry(5)); err != nil {
		l.s.Log.Error("enqueue notifikasi jadwal gagal", "event", eventID, "error", err)
	}
	return l.s.Event.view(ctx, l.s.Q, out)
}

// Cancel: event dibatalkan → penjualan berhenti, SEMUA order lunas di-refund penuh otomatis (idempoten), pemegang tiket diberi tahu.
func (l *LifecycleService) Cancel(ctx context.Context, p *auth.Principal, sc OrgScope, eventID, reason string) (*EventView, error) {
	if err := l.guard(p, sc); err != nil {
		return nil, err
	}
	return l.cancel(ctx, p, &sc, eventID, reason)
}

// AdminCancel: admin membatalkan event (pelanggaran kebijakan/penipuan) — refund massal & notifikasi sama seperti pembatalan organizer. Wajib MFA + alasan.
func (l *LifecycleService) AdminCancel(ctx context.Context, p *auth.Principal, eventID, reason string) (*EventView, error) {
	if !p.MFAPassed {
		return nil, apperr.ForbiddenCode(apperr.CodeMFARequired, "verifikasi dua langkah diperlukan").With("setup", true)
	}
	return l.cancel(ctx, p, nil, eventID, reason)
}

func (l *LifecycleService) cancel(ctx context.Context, p *auth.Principal, sc *OrgScope, eventID, reason string) (*EventView, error) {
	if len([]rune(reason)) < 5 {
		return nil, apperr.Validation("alasan pembatalan wajib diisi").With("field", "reason")
	}
	var out sqlcgen.Event
	err := l.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		ev, err := q.GetEventForUpdate(ctx, eventID)
		if err != nil || (sc != nil && ev.OrganizerID != sc.OrganizerID) {
			return apperr.NotFound("event")
		}
		if ev.Status != stPublished && ev.Status != stPaused {
			return apperr.Conflict(apperr.CodeInvalidState, "hanya event terbit yang bisa dibatalkan")
		}
		if out, err = q.MarkEventCancelled(ctx, sqlcgen.MarkEventCancelledParams{ID: ev.ID, ChangedAt: ptr(l.s.now())}); err != nil {
			return err
		}
		if err := l.s.Audit.Log(ctx, q, p, "event.cancel", "event", ev.ID, map[string]any{"status": ev.Status}, map[string]any{"reason": reason}); err != nil {
			return err
		}
		return emit(ctx, q, "event."+ev.ID, "event.cancelled", map[string]any{})
	})
	if err != nil {
		return nil, err
	}
	l.s.Catalog.InvalidateAvailability(ctx, eventID)
	if n, _ := l.s.Q.SoldListingsForEvent(ctx, eventID); n > 0 { // resale terjual pada event batal: perlu penanganan finance (penjual mungkin sudah dibayar)
		_ = l.s.Q.InsertReconException(ctx, sqlcgen.InsertReconExceptionParams{ID: id.New(), Kind: "resale_on_cancelled_event", Detail: mustJSON(map[string]any{"event_id": eventID, "sold_listings": n})})
	}
	if err := l.s.Jobs.Enqueue(ctx, jobCancelFanout, map[string]string{"event_id": eventID, "kind": "cancelled"}, jobs.MaxRetry(10)); err != nil {
		l.s.Log.Error("enqueue fanout pembatalan gagal (sweep akan menangani refund)", "event", eventID, "error", err)
	}
	return l.s.Event.view(ctx, l.s.Q, out)
}

// handleFanout: refund massal (cancel) + notifikasi ke semua pemegang tiket. Aman diulang.
func (l *LifecycleService) handleFanout(ctx context.Context, payload []byte) error {
	p, err := jobs.Decode[map[string]string](payload)
	if err != nil {
		return nil
	}
	ev, err := l.s.Q.GetEvent(ctx, p["event_id"])
	if err != nil {
		return nil
	}
	kind := p["kind"]
	if kind == "cancelled" {
		if _, err := l.s.Refund.CancelEventRefunds(ctx, ev.ID); err != nil {
			return err
		}
		if _, err := l.s.Q.VoidUnrefundableTickets(ctx, ev.ID); err != nil {
			return err
		}
	}
	holders, err := l.s.Q.EventAudienceIDs(ctx, ev.ID)
	if err != nil {
		return err
	}
	pol := ParseRefundPolicy(ev.RefundPolicy, l.s.Cfg.RefundRescheduleWindowDays)
	window := l.s.now().Add(time.Duration(pol.OnReschedule.WindowDays) * 24 * time.Hour).Format("02 Jan 2006")
	for _, uid := range holders {
		// satu notifikasi per (event, kind, user): kunci dedup mencegah kirim ganda saat job diulang
		ok, _ := l.s.Redis.SetNX(ctx, "notified:"+ev.ID+":"+kind+":"+uid+":"+scheduleKey(ev), "1", 30*24*time.Hour).Result()
		if !ok {
			continue
		}
		data := map[string]any{"name": "", "event_title": ev.Title}
		tpl := ""
		switch kind {
		case "cancelled":
			tpl = "event_cancelled"
			data["refund_info"] = "Cek status refund di halaman pesananmu."
		case "rescheduled":
			tpl = "event_rescheduled"
			data["new_date"] = ev.StartsAt.Format("02 Jan 2006 15:04")
			data["window_until"] = window
		case "postponed":
			tpl = "event_postponed"
			data["window_until"] = window
		default:
			continue
		}
		l.s.Notifier.Send(ctx, NotifyReq{UserID: uid, Template: tpl, Data: data})
	}
	return nil
}

// SweepCancelled (scheduler): pastikan tidak ada order lunas tanpa refund pada event batal (mis. bayar-setelah-batal).
func (l *LifecycleService) SweepCancelled(ctx context.Context) (int, error) {
	ids, err := l.s.Q.CancelledEventsWithPaidOrders(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, eid := range ids {
		c, err := l.s.Refund.CancelEventRefunds(ctx, eid)
		if err != nil {
			return n, err
		}
		n += c
	}
	return n, nil
}

func scheduleKey(ev sqlcgen.Event) string {
	if ev.ScheduleChangedAt == nil {
		return "0"
	}
	return ev.ScheduleChangedAt.UTC().Format("20060102150405")
}
