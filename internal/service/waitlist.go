package service

import (
	"context"
	"time"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/platform"
	"tixgo/backend/internal/repository/sqlcgen"
)

// WaitlistService = daftar tunggu kursi/kuota habis; tiket yang kembali ditawarkan berurutan dengan hold eksklusif
// selama jendela beli (docs/17 F11, 20 M15). Mengurangi calo: kursi lepas tidak jatuh ke publik sebelum waitlist habis.
type WaitlistService struct{ s *Services }

func newWaitlistService(s *Services) *WaitlistService { return &WaitlistService{s: s} }

const offerWindow = 10 * time.Minute // sesuai jendela beli GOERS/tiket.com (T)

type WaitlistView struct {
	ID           string     `json:"id"`
	EventID      string     `json:"event_id"`
	EventTitle   string     `json:"event_title"`
	EventSlug    string     `json:"event_slug"`
	TierID       string     `json:"tier_id"`
	TierName     string     `json:"tier_name"`
	Qty          int        `json:"qty"`
	Status       string     `json:"status"`
	OfferedUntil *time.Time `json:"offered_until,omitempty"`
	HoldID       *string    `json:"hold_id,omitempty"`
}

func (w *WaitlistService) Join(ctx context.Context, userID, eventKey, tierID string, qty int) (*WaitlistView, error) {
	ev, err := w.s.Catalog.lookup(ctx, eventKey)
	if err != nil || ev.Status != stPublished {
		return nil, apperr.NotFound("event")
	}
	if qty < 1 || qty > ev.MaxPerOrder {
		return nil, apperr.Validation("qty harus 1 sampai batas per pesanan").With("field", "qty").With("max", ev.MaxPerOrder)
	}
	if _, err := w.s.Auth.RequireVerified(ctx, w.s.Q, userID); err != nil {
		return nil, err
	}
	tier, err := w.s.Q.GetTier(ctx, tierID)
	if err != nil || tier.EventID != ev.ID {
		return nil, apperr.NotFound("tier")
	}
	av, err := w.s.Q.TierAvailability(ctx, ev.ID)
	if err != nil {
		return nil, err
	}
	for _, a := range av {
		if a.ID == tierID && a.Capacity-a.Sold-a.Held > 0 {
			return nil, apperr.Conflict(apperr.CodeInvalidState, "tiket masih tersedia; beli langsung")
		}
		if a.ID == tierID && a.Capacity == 0 {
			return nil, apperr.Conflict(apperr.CodeInvalidState, "tier tidak memiliki kapasitas")
		}
	}
	row, err := w.s.Q.WaitlistJoin(ctx, sqlcgen.WaitlistJoinParams{ID: id.New(), EventID: ev.ID, TierID: tierID, UserID: userID, Qty: qty})
	if err != nil {
		return nil, err
	}
	return &WaitlistView{ID: row.ID, EventID: ev.ID, EventTitle: ev.Title, EventSlug: ev.Slug, TierID: tierID, TierName: tier.Name, Qty: row.Qty, Status: row.Status}, nil
}

func (w *WaitlistService) Leave(ctx context.Context, userID, eventKey, tierID string) error {
	ev, err := w.s.Catalog.lookup(ctx, eventKey)
	if err != nil {
		return apperr.NotFound("event")
	}
	// bila sedang ditawari, lepaskan hold-nya
	var holdID *string
	if rows, err := w.s.Q.WaitlistForUser(ctx, userID); err == nil {
		for _, r := range rows {
			if r.EventID == ev.ID && r.TierID == tierID && r.Status == "offered" {
				holdID = r.HoldID
			}
		}
	}
	if err := w.s.Q.WaitlistLeave(ctx, sqlcgen.WaitlistLeaveParams{EventID: ev.ID, TierID: tierID, UserID: userID}); err != nil {
		return err
	}
	if holdID != nil {
		_ = w.s.Hold.Release(ctx, userID, *holdID)
	}
	return nil
}

func (w *WaitlistService) List(ctx context.Context, userID string) ([]WaitlistView, error) {
	rows, err := w.s.Q.WaitlistForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]WaitlistView, 0, len(rows))
	for _, r := range rows {
		out = append(out, WaitlistView{ID: r.ID, EventID: r.EventID, EventTitle: r.EventTitle, EventSlug: r.EventSlug, TierID: r.TierID, TierName: r.TierName,
			Qty: r.Qty, Status: r.Status, OfferedUntil: r.OfferedUntil, HoldID: r.HoldID})
	}
	return out, nil
}

// OnInventoryFreed dipanggil setelah kursi/kuota kembali: tawarkan ke waitlist berurutan (FIFO ketat).
func (w *WaitlistService) OnInventoryFreed(ctx context.Context, eventID string) {
	if err := w.offerAll(ctx, eventID); err != nil {
		w.s.Log.Error("waitlist offer gagal", "event", eventID, "error", err)
	}
}

func (w *WaitlistService) offerAll(ctx context.Context, eventID string) error {
	ev, err := w.s.Q.GetEvent(ctx, eventID)
	if err != nil || ev.Status != stPublished || ev.SalesPaused {
		return nil
	}
	tiers, err := w.s.Q.WaitlistTiersWaiting(ctx, eventID)
	if err != nil || len(tiers) == 0 {
		return err
	}
	for _, tierID := range tiers {
		for guard := 0; guard < 200; guard++ {
			offered, err := w.offerOne(ctx, ev, tierID)
			if err != nil {
				return err
			}
			if !offered {
				break
			}
		}
	}
	return nil
}

// offerOne menawarkan ke antrean terdepan jika stok cukup. true = ada penawaran (lanjut ke berikutnya).
func (w *WaitlistService) offerOne(ctx context.Context, ev sqlcgen.Event, tierID string) (bool, error) {
	var offered bool
	var userID, holdID string
	var tierName string
	var qty int
	err := w.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		entry, err := q.WaitlistNext(ctx, sqlcgen.WaitlistNextParams{EventID: ev.ID, TierID: tierID})
		if err != nil {
			if platform.IsNoRows(err) {
				return nil
			}
			return err
		}
		avail, err := q.TierAvailability(ctx, ev.ID)
		if err != nil {
			return err
		}
		var free int64
		for _, a := range avail {
			if a.ID == tierID {
				free = a.Capacity - a.Sold - a.Held
				tierName = a.Name
			}
		}
		if free < int64(entry.Qty) {
			return nil // FIFO ketat: tidak melompati antrean
		}
		req := HoldRequest{}
		if ev.Mode == "seated" {
			ids, err := q.AvailableSeatsInTier(ctx, sqlcgen.AvailableSeatsInTierParams{EventID: ev.ID, TierID: tierID, Limit: int32(entry.Qty)})
			if err != nil {
				return err
			}
			if len(ids) < entry.Qty {
				return nil
			}
			req.SeatIDs = ids
		} else {
			req.TierID, req.Qty = tierID, entry.Qty
		}
		hv, err := w.s.Hold.createInTx(ctx, q, ev, entry.UserID, req, createOpts{SkipPhase: true, TTL: offerWindow})
		if err != nil {
			// penawaran gagal (mis. batas per akun): lewati pengguna ini
			if ae, ok := apperr.As(err); ok && (ae.Code == apperr.CodeLimitExceeded || ae.Code == apperr.CodeBlocked || ae.Code == apperr.CodeEmailNotVerified) {
				return q.WaitlistSetStatus(ctx, sqlcgen.WaitlistSetStatusParams{ID: entry.ID, Status: "cancelled"})
			}
			return err
		}
		until := w.s.now().Add(offerWindow)
		if err := q.WaitlistOffer(ctx, sqlcgen.WaitlistOfferParams{ID: entry.ID, OfferedUntil: &until, HoldID: &hv.ID}); err != nil {
			return err
		}
		offered, userID, holdID, qty = true, entry.UserID, hv.ID, entry.Qty
		return nil
	})
	if err != nil {
		return false, err
	}
	if offered {
		w.s.Catalog.InvalidateAvailability(ctx, ev.ID)
		w.s.Notifier.Send(ctx, NotifyReq{UserID: userID, Template: "waitlist_offer", Data: map[string]any{
			"name": "", "event_title": ev.Title, "minutes": int(offerWindow.Minutes()), "link": w.s.Cfg.PublicWebURL + "/checkout/" + holdID, "qty": qty, "tier": tierName}})
		w.s.publish(ctx, "user."+userID, "waitlist.offer", map[string]any{"hold_id": holdID, "event_id": ev.ID})
	}
	return offered, nil
}

// OnHoldEnded: tawaran kedaluwarsa/dilepas → entri 'expired'. OnHoldConverted: jadi order → 'accepted'.
func (w *WaitlistService) OnHoldEnded(ctx context.Context, q *sqlcgen.Queries, holdID, status string) {
	if e, err := q.WaitlistByHold(ctx, &holdID); err == nil && e.Status == "offered" {
		_ = q.WaitlistSetStatus(ctx, sqlcgen.WaitlistSetStatusParams{ID: e.ID, Status: status})
	}
}
