package service

import (
	"context"
	"sort"
	"time"
	"tixgo/backend/internal/metrics"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/platform"
	"tixgo/backend/internal/repository/sqlcgen"
)

// HoldService = penahanan kursi/kuota anti-oversell (docs/11 §1, 20 M4). DB adalah sumber kebenaran:
// kursi dikunci berurutan (SELECT ... ORDER BY id FOR UPDATE) lalu diubah available→held dalam SATU transaksi.
type HoldService struct{ s *Services }

func newHoldService(s *Services) *HoldService { return &HoldService{s: s} }

type HoldRequest struct {
	SeatIDs []string `json:"seat_ids" binding:"omitempty,max=50,dive,len=26"`
	TierID  string   `json:"tier_id" binding:"omitempty,len=26"`
	Qty     int      `json:"qty" binding:"omitempty,min=1,max=50"`
}

type HoldItemView struct {
	SeatID     *string `json:"seat_id,omitempty"`
	Label      *string `json:"label,omitempty"`
	TierID     string  `json:"tier_id"`
	TierName   string  `json:"tier_name"`
	Qty        int     `json:"qty"`
	Price      int64   `json:"price"`
	FinalPrice int64   `json:"final_price"`
}

type HoldView struct {
	ID         string         `json:"id"`
	EventID    string         `json:"event_id"`
	Status     string         `json:"status"`
	ExpiresAt  time.Time      `json:"expires_at"`
	ExpiresIn  int            `json:"expires_in"`
	ExtendedAt *time.Time     `json:"extended_at,omitempty"`
	CanExtend  bool           `json:"can_extend"`
	Items      []HoldItemView `json:"items"`
	Subtotal   int64          `json:"subtotal"`
}

func (h *HoldService) view(ctx context.Context, q *sqlcgen.Queries, hold sqlcgen.Hold, ev sqlcgen.Event) (*HoldView, error) {
	items, err := q.ListHoldItems(ctx, hold.ID)
	if err != nil {
		return nil, err
	}
	cfg := feeConfigOf(ev)
	v := &HoldView{ID: hold.ID, EventID: hold.EventID, Status: hold.Status, ExpiresAt: hold.ExpiresAt, ExtendedAt: hold.ExtendedAt,
		Items: make([]HoldItemView, 0, len(items))}
	if d := hold.ExpiresAt.Sub(h.s.now()); d > 0 {
		v.ExpiresIn = int(d.Seconds())
	}
	v.CanExtend = hold.Status == "active" && hold.ExtendedAt == nil && v.ExpiresIn > 0
	for _, it := range items {
		qty := it.Qty
		v.Items = append(v.Items, HoldItemView{SeatID: it.SeatID, Label: it.Label, TierID: it.TierID, TierName: it.TierName, Qty: qty, Price: it.Price, FinalPrice: cfg.FinalPrice(it.Price)})
		v.Subtotal += it.Price * int64(qty)
	}
	return v, nil
}

func uniqSorted(ids []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(ids))
	for _, s := range ids {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// Create menahan kursi/kuota. admissionToken wajib bila event memakai antrean.
func (h *HoldService) Create(ctx context.Context, userID, eventKey string, req HoldRequest, admissionToken string) (*HoldView, error) {
	return h.CreateWith(ctx, userID, eventKey, req, admissionToken, createOpts{})
}

// CreateWith = Create dengan opsi internal (squad: batas per akun hanya menghitung bagian ketua).
func (h *HoldService) CreateWith(ctx context.Context, userID, eventKey string, req HoldRequest, admissionToken string, opt createOpts) (*HoldView, error) {
	ev, err := h.s.Catalog.lookup(ctx, eventKey)
	if err != nil {
		return nil, apperr.NotFound("event")
	}
	switch ev.Status {
	case stPublished, stPaused:
	case stCancelled:
		return nil, apperr.Conflict(apperr.CodeSaleNotOpen, "event dibatalkan")
	case stFinished:
		return nil, apperr.Conflict(apperr.CodeSaleNotOpen, "event sudah selesai")
	default:
		return nil, apperr.NotFound("event")
	}
	if ev.Status == stPaused || ev.SalesPaused {
		return nil, apperr.Conflict(apperr.CodeSalePaused, "penjualan sedang dijeda")
	}
	if ev.ScheduleState == "cancelled" {
		return nil, apperr.Conflict(apperr.CodeSaleNotOpen, "event dibatalkan")
	}
	if err := h.s.Queue.CheckAdmission(ctx, ev, userID, admissionToken); err != nil {
		return nil, err
	}
	if h.s.Fraud.Decision(ctx, userID) == "block" {
		return nil, apperr.ForbiddenCode(apperr.CodeBlocked, "akses ditolak")
	}
	if ok, ra, err := h.s.Limiter.Allow(ctx, "hold:"+userID, 30, time.Minute); err == nil && !ok {
		return nil, apperr.TooMany("terlalu sering menahan kursi").With("retry_after", int(ra.Seconds())+1)
	}

	var out *HoldView
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		out, lastErr = h.create(ctx, ev, userID, req, opt)
		if lastErr == nil || !platform.IsRetryable(lastErr) {
			break
		}
		time.Sleep(time.Duration(10*(attempt+1)) * time.Millisecond) // deadlock/serialization: coba lagi
	}
	if lastErr != nil {
		metrics.Holds.WithLabelValues("rejected").Inc()
		return nil, lastErr
	}
	metrics.Holds.WithLabelValues("ok").Inc()
	h.s.Catalog.InvalidateAvailability(ctx, ev.ID)
	h.s.Analytics.Track(ctx, ev.ID, "hold")
	return out, nil
}

// createOpts mengatur jalur internal (penawaran waitlist): lewati cek fase/antrean, TTL khusus.
type createOpts struct {
	SkipPhase  bool
	TTL        time.Duration
	AccountQty int    // >0: jumlah yang dihitung ke batas per akun (squad); 0 = seluruh permintaan
	SquadID    string // hold milik squad: tidak otomatis dilepas saat pengguna membuat hold biasa
}

func (h *HoldService) create(ctx context.Context, ev sqlcgen.Event, userID string, req HoldRequest, opt createOpts) (*HoldView, error) {
	var out *HoldView
	err := h.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		out, err = h.createInTx(ctx, q, ev, userID, req, opt)
		return err
	})
	return out, err
}

// createInTx membuat hold di dalam transaksi pemanggil (dipakai juga penawaran waitlist).
func (h *HoldService) createInTx(ctx context.Context, q *sqlcgen.Queries, ev sqlcgen.Event, userID string, req HoldRequest, opt createOpts) (*HoldView, error) {
	var out *HoldView
	err := func() error {
		if err := q.LockUser(ctx, userID); err != nil { // serialisasi hold per pengguna (batas per akun)
			return err
		}
		if _, err := h.s.Auth.RequireVerified(ctx, q, userID); err != nil {
			return err
		}
		now := h.s.now()
		if !opt.SkipPhase {
			phase, err := h.s.Phase.EligiblePhase(ctx, q, ev.ID, userID, now)
			if err != nil {
				return err
			}
			if phase == nil {
				return h.notOpenErr(ctx, q, ev, now)
			}
		}
		// validasi bentuk permintaan
		var seatIDs []string
		requested := 0
		if ev.Mode == "seated" {
			seatIDs = uniqSorted(req.SeatIDs)
			if len(seatIDs) == 0 || req.TierID != "" || req.Qty != 0 {
				return apperr.Validation("event seated memerlukan seat_ids (tanpa tier_id/qty)").With("field", "seat_ids")
			}
			requested = len(seatIDs)
		} else {
			if req.TierID == "" || req.Qty < 1 || len(req.SeatIDs) > 0 {
				return apperr.Validation("event general memerlukan tier_id dan qty (tanpa seat_ids)").With("field", "tier_id")
			}
			requested = req.Qty
		}
		if requested > ev.MaxPerOrder {
			return apperr.Unprocessable(apperr.CodeLimitExceeded, "melebihi batas per pesanan").With("limit", ev.MaxPerOrder).With("scope", "order")
		}
		// lepas hold aktif sebelumnya milik pengguna di event ini (ganti pilihan)
		old, err := q.ActiveHoldsForUserEvent(ctx, sqlcgen.ActiveHoldsForUserEventParams{UserID: userID, EventID: ev.ID})
		if err != nil {
			return err
		}
		for _, o := range old {
			if err := h.releaseTx(ctx, q, o, "released"); err != nil {
				return err
			}
		}
		purchased, err := q.UserEventPurchased(ctx, sqlcgen.UserEventPurchasedParams{UserID: userID, EventID: ev.ID})
		if err != nil {
			return err
		}
		acct := requested
		if opt.AccountQty > 0 {
			acct = opt.AccountQty
		}
		if int(purchased)+acct > ev.MaxPerAccount {
			return apperr.Unprocessable(apperr.CodeLimitExceeded, "melebihi batas pembelian per akun untuk event ini").
				With("limit", ev.MaxPerAccount).With("scope", "account").With("already", int(purchased))
		}

		holdID := id.New()
		ttl := time.Duration(ev.HoldTtlSec) * time.Second
		if opt.TTL > 0 {
			ttl = opt.TTL
		}
		hold, err := q.InsertHold(ctx, sqlcgen.InsertHoldParams{ID: holdID, EventID: ev.ID, UserID: userID, ExpiresAt: now.Add(ttl), SquadID: strPtr(opt.SquadID)})
		if err != nil {
			return err
		}
		if ev.Mode == "seated" {
			locked, err := q.LockSeatsOrdered(ctx, sqlcgen.LockSeatsOrderedParams{EventID: ev.ID, Ids: seatIDs})
			if err != nil {
				return err
			}
			found := map[string]sqlcgen.LockSeatsOrderedRow{}
			for _, r := range locked {
				found[r.ID] = r
			}
			var taken []string
			for _, sid := range seatIDs {
				r, ok := found[sid]
				if !ok {
					return apperr.NotFound("kursi").With("seat_id", sid)
				}
				if r.Status != "available" {
					taken = append(taken, sid)
				}
			}
			if len(taken) > 0 {
				// tidak ada hold parsial: transaksi dibatalkan, tidak ada kursi yang berubah (T01)
				return apperr.Conflict(apperr.CodeSeatTaken, "sebagian kursi sudah diambil").With("seat_ids", taken)
			}
			acquired, err := q.AcquireSeats(ctx, sqlcgen.AcquireSeatsParams{EventID: ev.ID, Ids: seatIDs})
			if err != nil {
				return err
			}
			if len(acquired) != len(seatIDs) {
				return apperr.Conflict(apperr.CodeSeatTaken, "sebagian kursi sudah diambil").With("seat_ids", seatIDs)
			}
			for _, a := range acquired {
				sid := a.ID
				if err := q.InsertHoldItem(ctx, sqlcgen.InsertHoldItemParams{HoldID: holdID, SeatID: &sid, TierID: a.TierID, Qty: 1}); err != nil {
					return err
				}
			}
			if err := emit(ctx, q, "event."+ev.ID, "seat.updated", map[string]any{"seat_ids": seatIDs, "status": "held"}); err != nil {
				return err
			}
		} else {
			tier, err := q.GetTier(ctx, req.TierID)
			if err != nil || tier.EventID != ev.ID {
				return apperr.NotFound("tier")
			}
			n, err := q.HoldTierQty(ctx, sqlcgen.HoldTierQtyParams{ID: req.TierID, EventID: ev.ID, Qty: req.Qty})
			if err != nil {
				return err
			}
			if n == 0 {
				return apperr.Conflict(apperr.CodeSoldOut, "kuota tidak mencukupi").With("tier_id", req.TierID)
			}
			if err := q.InsertHoldItem(ctx, sqlcgen.InsertHoldItemParams{HoldID: holdID, TierID: req.TierID, Qty: req.Qty}); err != nil {
				return err
			}
			if err := emit(ctx, q, "event."+ev.ID, "inventory.updated", map[string]any{"tier_id": req.TierID}); err != nil {
				return err
			}
		}
		out, err = h.view(ctx, q, hold, ev)
		return err
	}()
	return out, err
}

func (h *HoldService) notOpenErr(ctx context.Context, q *sqlcgen.Queries, ev sqlcgen.Event, now time.Time) error {
	phases, _ := q.ListPhases(ctx, ev.ID)
	var next *time.Time
	allEnded := len(phases) > 0
	for _, p := range phases {
		if p.EndsAt.After(now) {
			allEnded = false
		}
		if p.StartsAt.After(now) && (next == nil || p.StartsAt.Before(*next)) {
			t := p.StartsAt
			next = &t
		}
	}
	e := apperr.Conflict(apperr.CodeSaleNotOpen, "penjualan belum dibuka untuk akunmu")
	if next != nil {
		e = e.With("opens_at", next.UTC().Format(time.RFC3339))
	}
	if allEnded {
		e = apperr.Conflict(apperr.CodeSaleNotOpen, "penjualan sudah ditutup")
	}
	return e
}

// releaseTx mengembalikan kursi/kuota dari hold dan menandainya (released|expired). Dipakai juga oleh order.
func (h *HoldService) releaseTx(ctx context.Context, q *sqlcgen.Queries, hold sqlcgen.Hold, status string) error {
	items, err := q.ListHoldItems(ctx, hold.ID)
	if err != nil {
		return err
	}
	var seatIDs []string
	tierQty := map[string]int{}
	for _, it := range items {
		if it.SeatID != nil {
			seatIDs = append(seatIDs, *it.SeatID)
		} else {
			tierQty[it.TierID] += it.Qty
		}
	}
	if len(seatIDs) > 0 {
		sort.Strings(seatIDs)
		if _, err := q.ReleaseSeats(ctx, seatIDs); err != nil {
			return err
		}
		if err := emit(ctx, q, "event."+hold.EventID, "seat.updated", map[string]any{"seat_ids": seatIDs, "status": "available"}); err != nil {
			return err
		}
	}
	tids := make([]string, 0, len(tierQty))
	for t := range tierQty {
		tids = append(tids, t)
	}
	sort.Strings(tids)
	for _, t := range tids {
		if err := q.ReleaseTierHeld(ctx, sqlcgen.ReleaseTierHeldParams{ID: t, Qty: tierQty[t]}); err != nil {
			return err
		}
		if err := emit(ctx, q, "event."+hold.EventID, "inventory.updated", map[string]any{"tier_id": t}); err != nil {
			return err
		}
	}
	h.s.Waitlist.OnHoldEnded(ctx, q, hold.ID, "expired") // tawaran waitlist yang hold-nya berakhir tanpa jadi order
	return q.SetHoldStatus(ctx, sqlcgen.SetHoldStatusParams{ID: hold.ID, Status: status})
}

func (h *HoldService) own(ctx context.Context, q *sqlcgen.Queries, userID, holdID string, forUpdate bool) (sqlcgen.Hold, error) {
	var hold sqlcgen.Hold
	var err error
	if forUpdate {
		hold, err = q.GetHoldForUpdate(ctx, holdID)
	} else {
		hold, err = q.GetHold(ctx, holdID)
	}
	if err != nil || hold.UserID != userID {
		return hold, apperr.NotFound("hold")
	}
	return hold, nil
}

func (h *HoldService) Get(ctx context.Context, userID, holdID string) (*HoldView, error) {
	hold, err := h.own(ctx, h.s.Q, userID, holdID, false)
	if err != nil {
		return nil, err
	}
	ev, err := h.s.Q.GetEvent(ctx, hold.EventID)
	if err != nil {
		return nil, err
	}
	return h.view(ctx, h.s.Q, hold, ev)
}

func (h *HoldService) Release(ctx context.Context, userID, holdID string) error {
	var ev string
	err := h.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		hold, err := h.own(ctx, q, userID, holdID, true)
		if err != nil {
			return err
		}
		if hold.Status != "active" {
			return apperr.Conflict(apperr.CodeInvalidState, "hold tidak aktif")
		}
		ev = hold.EventID
		return h.releaseTx(ctx, q, hold, "released")
	})
	if err != nil {
		return err
	}
	h.s.Queue.FreeSlot(ctx, ev, userID, false)
	h.s.Catalog.InvalidateAvailability(ctx, ev)
	h.s.Waitlist.OnInventoryFreed(ctx, ev)
	return nil
}

// Extend = "Timer Tenang" (docs/17 F6): perpanjang SEKALI selama pembayaran belum dibuka.
func (h *HoldService) Extend(ctx context.Context, userID, holdID string) (*HoldView, error) {
	var out *HoldView
	err := h.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		hold, err := h.own(ctx, q, userID, holdID, true)
		if err != nil {
			return err
		}
		now := h.s.now()
		converted := hold.Status == "converted" && hold.OrderID != nil
		if (hold.Status != "active" && !converted) || !hold.ExpiresAt.After(now) {
			return apperr.Conflict(apperr.CodeHoldExpired, "hold sudah tidak aktif")
		}
		if hold.ExtendedAt != nil {
			return apperr.Conflict(apperr.CodeInvalidState, "perpanjangan hanya boleh sekali")
		}
		if converted { // hold sudah menjadi order: boleh selama order pending dan pembayaran BELUM dibuka
			ord, err := q.GetOrderForUpdate(ctx, *hold.OrderID)
			if err != nil || ord.Status != "pending" {
				return apperr.Conflict(apperr.CodeInvalidState, "pesanan tidak lagi menunggu pembayaran")
			}
			if _, err := q.LatestPaymentForOrder(ctx, ord.ID); err == nil {
				return apperr.Conflict(apperr.CodeInvalidState, "pembayaran sudah dibuka; perpanjangan tidak tersedia")
			}
		}
		ev, err := q.GetEvent(ctx, hold.EventID)
		if err != nil {
			return err
		}
		extra := time.Duration(h.s.Cfg.HoldExtendSec) * time.Second
		exp := hold.ExpiresAt.Add(extra)
		if err := q.ExtendHold(ctx, sqlcgen.ExtendHoldParams{ID: hold.ID, ExpiresAt: exp, ExtendedAt: ptr(now)}); err != nil {
			return err
		}
		hold.ExpiresAt, hold.ExtendedAt = exp, ptr(now)
		if converted {
			if err := q.ExtendOrderExpiry(ctx, sqlcgen.ExtendOrderExpiryParams{ID: *hold.OrderID, ExpiresAt: exp}); err != nil {
				return err
			}
		}
		out, err = h.view(ctx, q, hold, ev)
		return err
	})
	return out, err
}

// ExpireDue melepas hold aktif yang lewat waktu (worker, ±30 dtk). Mengembalikan jumlah.
func (h *HoldService) ExpireDue(ctx context.Context) (int, error) {
	total := 0
	for {
		var events []string
		var users []string
		n := 0
		err := h.s.InTx(ctx, func(q *sqlcgen.Queries) error {
			due, err := q.DueActiveHolds(ctx, sqlcgen.DueActiveHoldsParams{ExpiresAt: h.s.now(), Limit: 200})
			if err != nil {
				return err
			}
			for _, hold := range due {
				if err := h.releaseTx(ctx, q, hold, "expired"); err != nil {
					return err
				}
				events = append(events, hold.EventID)
				users = append(users, hold.UserID)
				n++
			}
			return nil
		})
		if err != nil {
			return total, err
		}
		for i := range events {
			h.s.Queue.FreeSlot(ctx, events[i], users[i], false)
			h.s.Catalog.InvalidateAvailability(ctx, events[i])
		}
		seen := map[string]bool{}
		for _, e := range events {
			if !seen[e] {
				seen[e] = true
				h.s.Waitlist.OnInventoryFreed(ctx, e)
			}
		}
		total += n
		if n < 200 {
			return total, nil
		}
	}
}
