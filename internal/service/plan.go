package service

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	ics "github.com/arran4/golang-ical"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/repository/sqlcgen"
)

// PlanService = Rencana Hari-H (kalender .ics, rute, pengingat) dan rekomendasi berbasis aturan (docs/17 F9, M24/M25).
type PlanService struct{ s *Services }

func newPlanService(s *Services) *PlanService { return &PlanService{s: s} }

func (p *PlanService) addEvent(cal *ics.Calendar, ev sqlcgen.Event, v *sqlcgen.Venue) {
	e := cal.AddEvent(ev.ID + "@tixgo")
	e.SetDtStampTime(p.s.now())
	e.SetStartAt(ev.StartsAt)
	end := ev.StartsAt.Add(3 * time.Hour)
	if ev.EndsAt != nil {
		end = *ev.EndsAt
	}
	e.SetEndAt(end)
	e.SetSummary(ev.Title)
	pageURL := p.s.Cfg.SEOSiteURL + "/events/" + ev.Slug
	e.SetURL(pageURL)
	desc := pageURL
	if ev.DoorsAt != nil {
		desc = "Pintu dibuka: " + ev.DoorsAt.Format("2 Jan 2006 15:04 MST") + "\n" + desc
	}
	if ev.ScheduleState == "rescheduled" || ev.ScheduleState == "postponed" {
		desc = "Perhatian: jadwal berubah (" + ev.ScheduleState + ").\n" + desc
	}
	e.SetDescription(desc)
	if v != nil {
		loc := v.Name + ", " + v.City
		if v.Address != nil {
			loc = v.Name + ", " + *v.Address + ", " + v.City
		}
		e.SetLocation(loc)
		if v.Lat != nil && v.Lng != nil {
			e.SetGeo(*v.Lat, *v.Lng)
		}
	}
	if ev.ScheduleState == "cancelled" || ev.Status == stCancelled {
		e.SetStatus(ics.ObjectStatusCancelled)
	} else {
		e.SetStatus(ics.ObjectStatusConfirmed)
	}
	for _, before := range []time.Duration{24 * time.Hour, 2 * time.Hour} {
		a := e.AddAlarm()
		a.SetAction(ics.ActionDisplay)
		a.SetTrigger("-PT" + durISO(before))
		a.SetDescription(ev.Title)
	}
}

func durISO(d time.Duration) string {
	h := int(d.Hours())
	if h >= 24 && h%24 == 0 {
		return fmt.Sprintf("%dH", h) // iCalendar mendukung PT24H
	}
	return fmt.Sprintf("%dH", h)
}

func (p *PlanService) venueOf(ctx context.Context, ev sqlcgen.Event) *sqlcgen.Venue {
	v, err := p.s.Q.GetVenue(ctx, ev.VenueID)
	if err != nil {
		return nil
	}
	return &v
}

// EventICS: kalender satu event publik (tanpa login).
func (p *PlanService) EventICS(ctx context.Context, slug string) ([]byte, string, error) {
	ev, err := p.s.Catalog.EventBySlug(ctx, slug, "", "")
	if err != nil {
		return nil, "", err
	}
	cal := ics.NewCalendar()
	cal.SetProductId("-//TixGo//ID")
	cal.SetMethod(ics.MethodPublish)
	p.addEvent(cal, ev, p.venueOf(ctx, ev))
	return []byte(cal.Serialize()), ev.Slug + ".ics", nil
}

// MyICS: semua acara mendatang dari tiket valid milik pengguna.
func (p *PlanService) MyICS(ctx context.Context, userID string) ([]byte, error) {
	ids, err := p.s.Q.UpcomingTicketEvents(ctx, userID)
	if err != nil {
		return nil, err
	}
	cal := ics.NewCalendar()
	cal.SetProductId("-//TixGo//ID")
	cal.SetMethod(ics.MethodPublish)
	cal.SetName("Tiket TixGo")
	for _, id := range ids {
		ev, err := p.s.Q.GetEvent(ctx, id)
		if err == nil {
			p.addEvent(cal, ev, p.venueOf(ctx, ev))
		}
	}
	return []byte(cal.Serialize()), nil
}

type DayPlan struct {
	EventTitle  string     `json:"event_title"`
	StartsAt    time.Time  `json:"starts_at"`
	DoorsAt     *time.Time `json:"doors_at,omitempty"`
	ArriveBy    time.Time  `json:"arrive_by"`
	Venue       string     `json:"venue"`
	Address     *string    `json:"address,omitempty"`
	MapsURL     string     `json:"maps_url,omitempty"`
	RouteURL    string     `json:"route_url,omitempty"`
	Seat        *string    `json:"seat_label,omitempty"`
	Tips        []string   `json:"tips"`
	CalendarURL string     `json:"calendar_url"`
}

// Plan untuk satu tiket (milik pemanggil).
func (p *PlanService) Plan(ctx context.Context, userID, ticketID string) (*DayPlan, error) {
	tk, err := p.s.Ticket.ownTicket(ctx, userID, ticketID)
	if err != nil {
		return nil, err
	}
	ev, err := p.s.Q.GetEvent(ctx, tk.EventID)
	if err != nil {
		return nil, apperr.NotFound("event")
	}
	v := p.venueOf(ctx, ev)
	arrive := ev.StartsAt.Add(-time.Hour)
	if ev.DoorsAt != nil {
		arrive = *ev.DoorsAt
	}
	plan := &DayPlan{EventTitle: ev.Title, StartsAt: ev.StartsAt, DoorsAt: ev.DoorsAt, ArriveBy: arrive, Seat: tk.SeatLabel, CalendarURL: "/api/v1/events/" + ev.Slug + "/calendar.ics",
		Tips: []string{"Buka tiket sebelum berangkat; QR berputar tiap beberapa detik.", "Unduh tiket saat ada sinyal agar bisa dibuka offline.", "Bawa identitas bila event berbatas usia."}}
	if ev.AgeMin != nil {
		plan.Tips = append(plan.Tips, fmt.Sprintf("Event ini untuk usia minimal %d tahun.", *ev.AgeMin))
	}
	if v != nil {
		plan.Venue, plan.Address = v.Name+", "+v.City, v.Address
		q := url.QueryEscape(v.Name + " " + v.City)
		if v.Lat != nil && v.Lng != nil {
			q = fmt.Sprintf("%f,%f", *v.Lat, *v.Lng)
		}
		plan.MapsURL = "https://www.google.com/maps/search/?api=1&query=" + q
		plan.RouteURL = "https://www.google.com/maps/dir/?api=1&destination=" + q
	}
	return plan, nil
}

// ---------- rekomendasi berbasis aturan (dapat dijelaskan) ----------

type Recommendation struct {
	Event   EventCard `json:"event"`
	Reasons []string  `json:"reasons"`
	Score   int       `json:"score"`
}

func (p *PlanService) Recommendations(ctx context.Context, userID string) ([]Recommendation, error) {
	rows, err := p.s.Q.RecommendedEvents(ctx, userID)
	if err != nil {
		return nil, err
	}
	type scored struct {
		id      string
		reasons []string
		score   int
	}
	var list []scored
	for _, r := range rows {
		var rs []string
		sc := 0
		if r.ByArtist {
			rs, sc = append(rs, "Karena kamu mengikuti artisnya"), sc+5
		}
		if r.ByVenue {
			rs, sc = append(rs, "Di venue yang kamu ikuti"), sc+3
		}
		if r.ByGenre {
			rs, sc = append(rs, "Genre yang kamu ikuti"), sc+3
		}
		if r.ByCity {
			rs, sc = append(rs, "Di kota yang kamu ikuti"), sc+2
		}
		if r.ByHistory {
			rs, sc = append(rs, "Mirip konser yang pernah kamu hadiri"), sc+2
		}
		if sc > 0 {
			list = append(list, scored{r.ID, rs, sc})
		}
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].score > list[j].score })
	if len(list) > 20 {
		list = list[:20]
	}
	ids := make([]string, len(list))
	for i, l := range list {
		ids[i] = l.id
	}
	cards, err := p.s.Catalog.CardsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	by := map[string]EventCard{}
	for _, c := range cards {
		by[c.ID] = c
	}
	out := make([]Recommendation, 0, len(list))
	for _, l := range list {
		if c, ok := by[l.id]; ok {
			out = append(out, Recommendation{Event: c, Reasons: l.reasons, Score: l.score})
		}
	}
	return out, nil
}

var _ = strings.TrimSpace
