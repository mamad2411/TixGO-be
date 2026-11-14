package service

import (
	"context"
	"strings"
	"time"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/repository/sqlcgen"
)

// PassportService = Concert Passport (docs/10 §4, M16): stempel dari tiket `used`, poin, badge, tukar poin ke akses presale fase fan.
// Poin TIDAK memengaruhi urutan antrean (keadilan) — hanya membuka fase fan yang sudah diumumkan.
type PassportService struct{ s *Services }

func newPassportService(s *Services) *PassportService { return &PassportService{s: s} }

const (
	pointsBase     = 100
	pointsNewCity  = 50
	pointsNewGenre = 50
	redeemCost     = 500
)

// AwardForTicket dipanggil setelah scan OK. Idempoten per (user, tiket, alasan); mati bila pengguna opt-out.
func (p *PassportService) AwardForTicket(ctx context.Context, ticketID string) {
	if err := p.award(ctx, ticketID); err != nil {
		p.s.Log.Error("passport: pemberian poin gagal", "ticket", ticketID, "error", err)
	}
}

func (p *PassportService) award(ctx context.Context, ticketID string) error {
	tk, err := p.s.Q.GetTicket(ctx, ticketID)
	if err != nil {
		return err
	}
	if out, _ := p.s.Q.PassportOptedOut(ctx, tk.OwnerUserID); out {
		return nil
	}
	ev, err := p.s.Q.GetEvent(ctx, tk.EventID)
	if err != nil {
		return err
	}
	v, err := p.s.Q.GetVenue(ctx, ev.VenueID)
	if err != nil {
		return err
	}
	genre := ""
	if ev.Genre != nil {
		genre = *ev.Genre
	}
	prior, err := p.s.Q.PassportPrior(ctx, sqlcgen.PassportPriorParams{City: v.City, Genre: genre, UserID: tk.OwnerUserID, TicketID: ticketID})
	if err != nil {
		return err
	}
	evID, tID := &ev.ID, &ticketID
	add := func(pts int, reason string) {
		_, _ = p.s.Q.AwardPoints(ctx, sqlcgen.AwardPointsParams{UserID: tk.OwnerUserID, EventID: evID, TicketID: tID, Points: pts, Reason: reason})
	}
	add(pointsBase, "attend")
	if prior.SameCity == 0 {
		add(pointsNewCity, "new_city")
	}
	if genre != "" && prior.SameGenre == 0 {
		add(pointsNewGenre, "new_genre")
	}
	if prior.Shows == 0 {
		_, _ = p.s.Q.AwardBadge(ctx, sqlcgen.AwardBadgeParams{UserID: tk.OwnerUserID, Badge: "first_show"})
	}
	// venue distinct termasuk tiket ini bila venue-nya baru
	venues := int(prior.Venues)
	if n, err := p.s.Q.PassportPrior(ctx, sqlcgen.PassportPriorParams{City: "", Genre: "", UserID: tk.OwnerUserID, TicketID: ""}); err == nil {
		venues = int(n.Venues)
	}
	if venues >= 5 {
		_, _ = p.s.Q.AwardBadge(ctx, sqlcgen.AwardBadgeParams{UserID: tk.OwnerUserID, Badge: "five_venues"})
	}
	if early, _ := p.s.Q.TicketWasPresale(ctx, ticketID); early {
		_, _ = p.s.Q.AwardBadge(ctx, sqlcgen.AwardBadgeParams{UserID: tk.OwnerUserID, Badge: "early_bird"})
	}
	return nil
}

type PassportStamp struct {
	TicketID string     `json:"ticket_id"`
	EventID  string     `json:"event_id"`
	Title    string     `json:"title"`
	Slug     string     `json:"slug"`
	Genre    *string    `json:"genre,omitempty"`
	Venue    string     `json:"venue"`
	City     string     `json:"city"`
	UsedAt   *time.Time `json:"used_at"`
}

type PassportView struct {
	Enabled    bool            `json:"enabled"`
	Points     int             `json:"points"`
	Badges     []string        `json:"badges"`
	Stamps     []PassportStamp `json:"stamps"`
	RedeemCost int             `json:"redeem_cost"`
}

// Mine: profil Passport privat (hanya pemilik; tidak ada endpoint publik).
func (p *PassportService) Mine(ctx context.Context, userID string) (*PassportView, error) {
	out, _ := p.s.Q.PassportOptedOut(ctx, userID)
	v := &PassportView{Enabled: !out, Badges: []string{}, Stamps: []PassportStamp{}, RedeemCost: redeemCost}
	if out {
		return v, nil
	}
	bal, err := p.s.Q.PointsBalance(ctx, userID)
	if err != nil {
		return nil, err
	}
	v.Points = int(bal)
	bs, err := p.s.Q.ListBadges(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, b := range bs {
		v.Badges = append(v.Badges, b.Badge)
	}
	st, err := p.s.Q.PassportStamps(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, s := range st {
		v.Stamps = append(v.Stamps, PassportStamp{TicketID: s.TicketID, EventID: s.EventID, Title: s.Title, Slug: s.Slug, Genre: s.Genre, Venue: s.Venue, City: s.City, UsedAt: s.UsedAt})
	}
	return v, nil
}

// Redeem menukar poin dengan akses fase fan (presale) sebuah event.
func (p *PassportService) Redeem(ctx context.Context, userID, eventKey string) error {
	if out, _ := p.s.Q.PassportOptedOut(ctx, userID); out {
		return apperr.Conflict(apperr.CodeInvalidState, "Passport dinonaktifkan")
	}
	ev, err := p.s.Catalog.lookup(ctx, eventKey)
	if err != nil || (ev.Status != stPublished && ev.Status != stPaused) {
		return apperr.NotFound("event")
	}
	return p.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		ph, err := q.FanPhaseForEvent(ctx, sqlcgen.FanPhaseForEventParams{EventID: ev.ID, EndsAt: p.s.now()})
		if err != nil {
			return apperr.Unprocessable(apperr.CodeValidation, "event ini tidak punya fase fan yang bisa dibuka")
		}
		if has, _ := q.HasPhaseAccess(ctx, sqlcgen.HasPhaseAccessParams{PhaseID: ph.ID, UserID: userID}); has {
			return apperr.Conflict(apperr.CodeConflict, "kamu sudah punya akses fase ini")
		}
		// kunci baris poin pengguna agar dua penukaran bersamaan tak melewati saldo
		if _, err := q.GetUserByIDForUpdate(ctx, userID); err != nil {
			return err
		}
		bal, err := q.PointsBalance(ctx, userID)
		if err != nil {
			return err
		}
		if int(bal) < redeemCost {
			return apperr.Unprocessable(apperr.CodeValidation, "poin belum cukup").With("needed", redeemCost).With("have", bal)
		}
		if err := q.SpendPoints(ctx, sqlcgen.SpendPointsParams{UserID: userID, EventID: &ev.ID, Points: -redeemCost, Reason: "redeem:" + ev.ID}); err != nil {
			return err
		}
		if _, err := q.GrantPassportAccess(ctx, sqlcgen.GrantPassportAccessParams{PhaseID: ph.ID, UserID: userID}); err != nil {
			return err
		}
		return p.s.Audit.LogSystem(ctx, q, "passport.redeem", "event", ev.ID, map[string]any{"user": userID})
	})
}

// Delete menghapus semua data Passport dan mematikan pemberian poin berikutnya (AC4).
func (p *PassportService) Delete(ctx context.Context, userID string) error {
	return p.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		if err := q.DeletePassportData(ctx, userID); err != nil {
			return err
		}
		if err := q.DeletePassportBadges(ctx, userID); err != nil {
			return err
		}
		return q.SetPassportOptOut(ctx, userID)
	})
}

func (p *PassportService) Enable(ctx context.Context, userID string) error {
	return p.s.Q.ClearPassportOptOut(ctx, userID)
}

var _ = strings.ToLower
