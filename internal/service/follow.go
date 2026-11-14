package service

import (
	"context"
	"strings"
	"time"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/jobs"
	"tixgo/backend/internal/platform"
	"tixgo/backend/internal/repository/sqlcgen"
)

// FollowService = favorit, pengingat onsale, ikuti artis/venue/kota/genre + notifikasi event baru (docs/17 F8, 20 M24).
type FollowService struct{ s *Services }

const jobFollowNotify = "follow:notify"

func newFollowService(s *Services) *FollowService {
	f := &FollowService{s: s}
	s.Reg.Handle(jobFollowNotify, f.handleNotify)
	return f
}

func (f *FollowService) publishedEvent(ctx context.Context, eventID string) (sqlcgen.Event, error) {
	ev, err := f.s.Q.GetEvent(ctx, eventID)
	if err != nil || ev.Status != stPublished && ev.Status != stPaused || ev.Visibility != "public" {
		return ev, apperr.NotFound("event")
	}
	return ev, nil
}

func (f *FollowService) AddFavorite(ctx context.Context, userID, eventID string) error {
	if _, err := f.publishedEvent(ctx, eventID); err != nil {
		return err
	}
	return f.s.Q.AddFavorite(ctx, sqlcgen.AddFavoriteParams{UserID: userID, EventID: eventID})
}

func (f *FollowService) RemoveFavorite(ctx context.Context, userID, eventID string) error {
	return f.s.Q.RemoveFavorite(ctx, sqlcgen.RemoveFavoriteParams{UserID: userID, EventID: eventID})
}

func (f *FollowService) FavoriteIDs(ctx context.Context, userID string) ([]string, error) {
	return f.s.Q.ListFavoriteEventIDs(ctx, userID)
}

// SetReminder: "ingatkan saya" 1 jam sebelum fase penjualan pertama dimulai (docs/06 M2-F3 AC3).
func (f *FollowService) SetReminder(ctx context.Context, userID, eventID string) (time.Time, error) {
	if _, err := f.publishedEvent(ctx, eventID); err != nil {
		return time.Time{}, err
	}
	phases, err := f.s.Q.ListPhases(ctx, eventID)
	if err != nil {
		return time.Time{}, err
	}
	now := f.s.now()
	var first *sqlcgen.SalePhase
	for i := range phases {
		if phases[i].StartsAt.After(now) && (first == nil || phases[i].StartsAt.Before(first.StartsAt)) {
			first = &phases[i]
		}
	}
	if first == nil {
		return time.Time{}, apperr.Conflict(apperr.CodeSaleNotOpen, "tidak ada fase penjualan mendatang untuk diingatkan")
	}
	at := first.StartsAt.Add(-time.Hour)
	if at.Before(now) {
		at = now // sisa waktu < 1 jam: ingatkan segera pada siklus berikutnya
	}
	if err := f.s.Q.UpsertReminder(ctx, sqlcgen.UpsertReminderParams{UserID: userID, EventID: eventID, RemindAt: at}); err != nil {
		return time.Time{}, err
	}
	return at, nil
}

func (f *FollowService) RemoveReminder(ctx context.Context, userID, eventID string) error {
	return f.s.Q.DeleteReminder(ctx, sqlcgen.DeleteReminderParams{UserID: userID, EventID: eventID})
}

// RunReminders (worker): kirim pengingat yang jatuh tempo.
func (f *FollowService) RunReminders(ctx context.Context) (int, error) {
	due, err := f.s.Q.DueReminders(ctx, sqlcgen.DueRemindersParams{RemindAt: f.s.now(), Limit: 500})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, d := range due {
		ev, err := f.s.Q.GetEvent(ctx, d.EventID)
		if err == nil && (ev.Status == stPublished || ev.Status == stPaused) {
			phases, _ := f.s.Q.ListPhases(ctx, ev.ID)
			var when time.Time
			for _, p := range phases {
				if p.StartsAt.After(f.s.now().Add(-time.Minute)) && (when.IsZero() || p.StartsAt.Before(when)) {
					when = p.StartsAt
				}
			}
			ven, _ := f.s.Q.GetVenue(ctx, ev.VenueID)
			f.s.Notifier.Send(ctx, NotifyReq{UserID: d.UserID, Template: "reminder_onsale", Category: "reminders", Data: map[string]any{
				"name": "", "event_title": ev.Title, "when": formatWhen(when, ven.Tz), "link": f.s.Cfg.PublicWebURL + "/events/" + ev.Slug}})
			n++
		}
		if err := f.s.Q.MarkReminderSent(ctx, sqlcgen.MarkReminderSentParams(d)); err != nil {
			return n, err
		}
	}
	return n, nil
}

func formatWhen(t time.Time, tz string) string {
	if t.IsZero() {
		return "segera"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	return t.In(loc).Format("02 Jan 2006 15:04 MST")
}

// ---- follow ----

var followKinds = map[string]bool{"artist": true, "venue": true, "city": true, "genre": true}

const maxFollows = 200

func (f *FollowService) Follow(ctx context.Context, userID, kind, target string) error {
	if !followKinds[kind] {
		return apperr.Validation("kind harus artist, venue, city, atau genre").With("field", "kind")
	}
	target = strings.TrimSpace(target)
	if target == "" || len(target) > 80 {
		return apperr.Validation("target wajib (maks 80 karakter)").With("field", "target")
	}
	switch kind {
	case "artist":
		a, err := f.s.Q.GetArtistBySlug(ctx, target)
		if err != nil {
			a, err = f.s.Q.GetArtist(ctx, target)
		}
		if err != nil {
			return apperr.NotFound("artis")
		}
		target = a.ID
	case "venue":
		if _, err := f.s.Q.GetVenue(ctx, target); err != nil {
			return apperr.NotFound("venue")
		}
	}
	cur, err := f.s.Q.ListFollows(ctx, userID)
	if err != nil {
		return err
	}
	if len(cur) >= maxFollows {
		return apperr.Conflict(apperr.CodeLimitExceeded, "maksimal 200 yang diikuti")
	}
	return f.s.Q.AddFollow(ctx, sqlcgen.AddFollowParams{UserID: userID, Kind: kind, Target: target})
}

func (f *FollowService) Unfollow(ctx context.Context, userID, kind, target string) error {
	if kind == "artist" {
		if a, err := f.s.Q.GetArtistBySlug(ctx, target); err == nil {
			target = a.ID
		}
	}
	return f.s.Q.RemoveFollow(ctx, sqlcgen.RemoveFollowParams{UserID: userID, Kind: kind, Target: target})
}

func (f *FollowService) List(ctx context.Context, userID string) ([]sqlcgen.ListFollowsRow, error) {
	return f.s.Q.ListFollows(ctx, userID)
}

// NotifyNewEvent mengantrekan fan-out notifikasi "event baru" ke pengikut (tidak memblokir publish).
func (f *FollowService) NotifyNewEvent(ctx context.Context, ev sqlcgen.Event) {
	if ev.Visibility != "public" {
		return
	}
	if err := f.s.Jobs.Enqueue(ctx, jobFollowNotify, map[string]string{"event_id": ev.ID}, jobs.Unique("follow:"+ev.ID, 24*time.Hour)); err != nil {
		f.s.Log.Error("enqueue follow notify gagal", "error", err)
	}
}

func (f *FollowService) handleNotify(ctx context.Context, payload []byte) error {
	p, err := jobs.Decode[map[string]string](payload)
	if err != nil {
		return nil
	}
	ev, err := f.s.Q.GetEvent(ctx, p["event_id"])
	if err != nil {
		return err
	}
	ven, err := f.s.Q.GetVenue(ctx, ev.VenueID)
	if err != nil {
		return err
	}
	arts, err := f.s.Q.ListEventArtists(ctx, ev.ID)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(arts))
	names := map[string]string{}
	for _, a := range arts {
		ids = append(ids, a.ID)
		names[a.ID] = a.Name
	}
	users, err := f.s.Q.FollowersOf(ctx, sqlcgen.FollowersOfParams{ArtistIds: ids, VenueID: ev.VenueID, City: ven.City, Genre: deref(ev.Genre)})
	if err != nil {
		return err
	}
	reason := "Kami memberi tahu karena kamu mengikuti " + ven.City + "."
	if len(arts) > 0 {
		reason = "Kami memberi tahu karena kamu mengikuti " + arts[0].Name + "."
	}
	for _, uid := range users {
		f.s.Notifier.Send(ctx, NotifyReq{UserID: uid, Template: "follow_new_event", Category: "follows", Data: map[string]any{
			"name": "", "event_title": ev.Title, "reason": reason, "link": f.s.Cfg.PublicWebURL + "/events/" + ev.Slug}})
	}
	return nil
}

var _ = platform.IsNoRows

// RunEventReminders (scheduler, tiap jam): pengingat H-1 ke pemegang tiket aktif — sekali per (event, pengguna);
// menghormati preferensi kategori "reminders". Dedup lewat Redis (3 hari) sehingga aman bila loop berjalan ulang.
func (f *FollowService) RunEventReminders(ctx context.Context) (int, error) {
	now := f.s.now()
	evs, err := f.s.Q.EventsStartingBetween(ctx, sqlcgen.EventsStartingBetweenParams{FromTs: now.Add(20 * time.Hour), ToTs: now.Add(28 * time.Hour)})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, ev := range evs {
		holders, err := f.s.Q.TicketHolderIDs(ctx, ev.ID)
		if err != nil {
			return n, err
		}
		loc := time.UTC
		if l, err := time.LoadLocation(ev.VenueTz); err == nil {
			loc = l
		}
		doors := ev.StartsAt
		if ev.DoorsAt != nil {
			doors = *ev.DoorsAt
		}
		for _, uid := range holders {
			if ok, _ := f.s.Redis.SetNX(ctx, "evrem:"+ev.ID+":"+uid, "1", 72*time.Hour).Result(); !ok {
				continue
			}
			f.s.Notifier.Send(ctx, NotifyReq{UserID: uid, Template: "reminder_event", Category: "reminders", Data: map[string]any{
				"name": "", "event_title": ev.Title, "when": "besok, " + ev.StartsAt.In(loc).Format("2 Jan 2006 15:04"), "venue": ev.VenueName,
				"doors": doors.In(loc).Format("15:04"), "tickets_url": f.s.Cfg.PublicWebURL + "/tiket"}})
			f.s.Notifier.SendPush(ctx, uid, "reminders", ev.Title+" besok", "Buka tiketmu sebelum berangkat.", "/tickets")
			n++
		}
	}
	return n, nil
}
