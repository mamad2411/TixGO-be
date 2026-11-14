package service

import (
	"context"
	"fmt"
	"time"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/platform"
	"tixgo/backend/internal/repository/sqlcgen"
)

// TicketService = penerbitan e-ticket dinamis (QR berputar TOTP) dan daftar tiket (docs/20 M8, 11 §4).
type TicketService struct{ s *Services }

func newTicketService(s *Services) *TicketService { return &TicketService{s: s} }

// issue menerbitkan satu tiket per order_item DI DALAM transaksi pemanggil.
func (t *TicketService) issue(ctx context.Context, q *sqlcgen.Queries, ev sqlcgen.Event, o sqlcgen.Order) ([]sqlcgen.Ticket, error) {
	items, err := q.ListOrderItems(ctx, o.ID)
	if err != nil {
		return nil, err
	}
	out := make([]sqlcgen.Ticket, 0, len(items))
	for _, it := range items {
		n, err := q.NextEntryNumber(ctx)
		if err != nil {
			return nil, err
		}
		secret := auth.NewTOTPSecret()
		enc, err := t.s.Box.Encrypt([]byte(secret))
		if err != nil {
			return nil, err
		}
		name := it.AttendeeName
		tk, err := q.InsertTicket(ctx, sqlcgen.InsertTicketParams{
			ID: id.New(), OrderItemID: &it.ID, EventID: o.EventID, TierID: it.TierID, OwnerUserID: o.UserID,
			SeatID: it.SeatID, SeatLabel: it.SeatLabel, EntryNumber: strPtr(fmt.Sprintf("%06d", n)), AttendeeName: &name, SecretEnc: enc, Origin: "sale",
		})
		if err != nil {
			return nil, err
		}
		price := it.Price
		if err := q.SetTicketFacePrice(ctx, sqlcgen.SetTicketFacePriceParams{ID: tk.ID, FacePrice: &price}); err != nil {
			return nil, err
		}
		out = append(out, tk)
	}
	return out, nil
}

// secretOf mendekripsi secret TOTP tiket.
func (t *TicketService) secretOf(tk sqlcgen.Ticket) (string, error) {
	b, err := t.s.Box.Decrypt(tk.SecretEnc)
	return string(b), err
}

type TicketView struct {
	ID            string     `json:"id"`
	EventID       string     `json:"event_id"`
	EventTitle    string     `json:"event_title"`
	EventSlug     string     `json:"event_slug"`
	StartsAt      time.Time  `json:"starts_at"`
	DoorsAt       *time.Time `json:"doors_at"`
	TZ            string     `json:"tz"`
	VenueName     string     `json:"venue_name"`
	VenueAddress  *string    `json:"venue_address"`
	TierName      string     `json:"tier_name"`
	SeatLabel     *string    `json:"seat_label"`
	EntryNumber   *string    `json:"entry_number"`
	AttendeeName  *string    `json:"attendee_name"`
	Status        string     `json:"status"`
	Origin        string     `json:"origin"`
	QRAvailable   bool       `json:"qr_available"`
	EventStatus   string     `json:"event_status"`
	ScheduleState string     `json:"schedule_state"`
	UsedAt        *time.Time `json:"used_at,omitempty"`
	PosterURL     string     `json:"poster_url,omitempty"`
}

func (t *TicketService) List(ctx context.Context, userID, status string, limit, offset int) ([]TicketView, int, error) {
	rows, err := t.s.Q.ListUserTickets(ctx, sqlcgen.ListUserTicketsParams{OwnerUserID: userID, Status: strPtr(status), Lim: int32(limit), Off: int32(offset)})
	if err != nil {
		return nil, 0, err
	}
	n, err := t.s.Q.CountUserTickets(ctx, sqlcgen.CountUserTicketsParams{OwnerUserID: userID, Status: strPtr(status)})
	if err != nil {
		return nil, 0, err
	}
	out := make([]TicketView, 0, len(rows))
	for _, r := range rows {
		v := TicketView{ID: r.ID, EventID: r.EventID, EventTitle: r.EventTitle, EventSlug: r.EventSlug, StartsAt: r.StartsAt, DoorsAt: r.DoorsAt,
			TZ: r.VenueTz, VenueName: r.VenueName, VenueAddress: r.VenueAddress, TierName: r.TierName, SeatLabel: r.SeatLabel, EntryNumber: r.EntryNumber,
			AttendeeName: r.AttendeeName, Status: r.Status, Origin: r.Origin, QRAvailable: r.Status == "valid", EventStatus: r.EventStatus,
			ScheduleState: r.ScheduleState, UsedAt: r.UsedAt}
		if r.EventPosterKey != nil {
			v.PosterURL = MediaURL(*r.EventPosterKey, 800)
		}
		out = append(out, v)
	}
	return out, int(n), nil
}

func (t *TicketService) ownTicket(ctx context.Context, userID, ticketID string) (sqlcgen.Ticket, error) {
	tk, err := t.s.Q.GetTicket(ctx, ticketID)
	if err != nil {
		if platform.IsNoRows(err) {
			return tk, apperr.NotFound("tiket")
		}
		return tk, err
	}
	if tk.OwnerUserID != userID {
		return tk, apperr.NotFound("tiket")
	}
	return tk, nil
}

func (t *TicketService) Get(ctx context.Context, userID, ticketID string) (*TicketView, error) {
	tk, err := t.ownTicket(ctx, userID, ticketID)
	if err != nil {
		return nil, err
	}
	list, _, err := t.List(ctx, userID, "", 1000, 0)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].ID == tk.ID {
			return &list[i], nil
		}
	}
	return nil, apperr.NotFound("tiket")
}

type QRView struct {
	Payload    string    `json:"payload"`
	StepSec    int       `json:"step_sec"`
	ValidUntil time.Time `json:"valid_until"`
}

// QRPayload = ticket_id + "." + TOTP(secret, langkah). Berganti tiap TICKET_QR_STEP_SEC (screenshot cepat usang).
func (t *TicketService) QRPayload(ctx context.Context, userID, ticketID string) (*QRView, error) {
	tk, err := t.ownTicket(ctx, userID, ticketID)
	if err != nil {
		return nil, err
	}
	if tk.Status != "valid" {
		return nil, apperr.Conflict(apperr.CodeTicketInvalid, "tiket tidak aktif: "+tk.Status)
	}
	secret, err := t.secretOf(tk)
	if err != nil {
		return nil, err
	}
	step := uint(t.s.Cfg.TicketQRStepSec)
	now := t.s.now()
	code, err := auth.TOTPCode(secret, now, step)
	if err != nil {
		return nil, err
	}
	until := now.Truncate(time.Duration(step) * time.Second).Add(time.Duration(step) * time.Second)
	return &QRView{Payload: tk.ID + "." + code, StepSec: int(step), ValidUntil: until}, nil
}
