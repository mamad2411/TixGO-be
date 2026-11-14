package service

import (
	"context"
	"crypto/rand"
	"strings"
	"time"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/repository/sqlcgen"
)

// SquadService = Squad Checkout (docs/10 §2, M14): ketua menahan N kursi, tiap anggota membayar bagiannya sendiri sebelum timer habis.
// Mekanisme: satu hold induk milik ketua; saat anggota "bayar bagianku", slot-nya DIPINDAH ke hold anak milik anggota → order biasa (tanpa jalur pembayaran baru).
type SquadService struct{ s *Services }

func newSquadService(s *Services) *SquadService { return &SquadService{s: s} }

const squadCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func newSquadCode() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = squadCodeAlphabet[int(b[i])%len(squadCodeAlphabet)]
	}
	return string(b)
}

type SquadCreate struct {
	Size    int      `json:"size" binding:"required,min=2,max=10"`
	Policy  string   `json:"policy" binding:"required,oneof=release_unpaid cancel_all"`
	SeatIDs []string `json:"seat_ids" binding:"omitempty,max=10,dive,len=26"`
	TierID  string   `json:"tier_id" binding:"omitempty,len=26"`
}

type SquadMemberView struct {
	UserID string  `json:"user_id"`
	Name   string  `json:"name"`
	Seat   *string `json:"seat_id,omitempty"`
	Label  *string `json:"seat_label,omitempty"`
	Status string  `json:"status"` // joined | paid | released
	Leader bool    `json:"leader"`
}

type SquadView struct {
	ID         string            `json:"id"`
	Code       string            `json:"code"`
	EventID    string            `json:"event_id"`
	EventTitle string            `json:"event_title,omitempty"`
	EventSlug  string            `json:"event_slug,omitempty"`
	Size       int               `json:"size"`
	Policy     string            `json:"policy"`
	Status     string            `json:"status"`
	ExpiresAt  *time.Time        `json:"expires_at,omitempty"`
	Members    []SquadMemberView `json:"members"`
	Open       int               `json:"open_slots"`
	Paid       int               `json:"paid"`
	HoldID     string            `json:"hold_id,omitempty"` // hanya ketua
	MyHoldID   string            `json:"my_hold_id,omitempty"`
}

// Create: ketua menahan Size kursi/kuota (hold induk) dan menjadi anggota pertama.
func (sq *SquadService) Create(ctx context.Context, userID, eventKey string, in SquadCreate, admissionToken string) (*SquadView, error) {
	ev, err := sq.s.Catalog.lookup(ctx, eventKey)
	if err != nil {
		return nil, apperr.NotFound("event")
	}
	if in.Size > ev.MaxPerOrder {
		return nil, apperr.Unprocessable(apperr.CodeLimitExceeded, "ukuran squad melebihi batas per pesanan event").With("limit", ev.MaxPerOrder)
	}
	req := HoldRequest{}
	if ev.Mode == "seated" {
		if len(in.SeatIDs) != in.Size {
			return nil, apperr.Validation("jumlah seat_ids harus sama dengan ukuran squad").With("field", "seat_ids")
		}
		req.SeatIDs = in.SeatIDs
	} else {
		if in.TierID == "" {
			return nil, apperr.Validation("tier_id wajib untuk event general").With("field", "tier_id")
		}
		req.TierID, req.Qty = in.TierID, in.Size
	}
	squadID := id.New()
	hv, err := sq.s.Hold.CreateWith(ctx, userID, eventKey, req, admissionToken, createOpts{AccountQty: 1, SquadID: squadID})
	if err != nil {
		return nil, err
	}
	err = sq.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := q.InsertSquad(ctx, sqlcgen.InsertSquadParams{ID: squadID, EventID: ev.ID, LeaderID: userID, Code: newSquadCode(), HoldID: &hv.ID, Size: in.Size, Policy: in.Policy}); err != nil {
			return err
		}
		seat, err := sq.nextSlot(ctx, q, hv.ID, squadID)
		if err != nil {
			return err
		}
		if err := q.InsertSquadMember(ctx, sqlcgen.InsertSquadMemberParams{SquadID: squadID, UserID: userID, SeatID: seat}); err != nil {
			return err
		}
		return sq.s.Audit.Log(ctx, q, &auth.Principal{UserID: userID, Role: "customer"}, "squad.create", "squad", squadID, nil, map[string]any{"size": in.Size, "policy": in.Policy})
	})
	if err != nil {
		_ = sq.s.Hold.Release(ctx, userID, hv.ID) // jangan tinggalkan hold yatim
		return nil, err
	}
	return sq.Get(ctx, userID, squadID)
}

// nextSlot memilih kursi hold induk yang belum diberikan ke anggota (nil untuk event general).
func (sq *SquadService) nextSlot(ctx context.Context, q *sqlcgen.Queries, holdID, squadID string) (*string, error) {
	seats, err := q.HoldSeatIDs(ctx, holdID)
	if err != nil {
		return nil, err
	}
	if len(seats) == 0 {
		return nil, nil
	}
	members, err := q.ListSquadMembers(ctx, squadID)
	if err != nil {
		return nil, err
	}
	taken := map[string]bool{}
	for _, m := range members {
		if m.SeatID != nil {
			taken[*m.SeatID] = true
		}
	}
	for _, s := range seats {
		if s != nil && !taken[*s] {
			return s, nil
		}
	}
	return nil, apperr.Conflict(apperr.CodeLimitExceeded, "squad sudah penuh")
}

// Join dengan kode undangan.
func (sq *SquadService) Join(ctx context.Context, userID, code string) (*SquadView, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	var squadID string
	err := sq.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		s0, err := q.GetSquadByCode(ctx, code)
		if err != nil {
			return apperr.NotFound("squad")
		}
		s, err := q.GetSquadForUpdate(ctx, s0.ID)
		if err != nil {
			return err
		}
		squadID = s.ID
		if s.Status != "holding" || s.HoldID == nil {
			return apperr.Conflict(apperr.CodeInvalidState, "squad ini sudah tidak menerima anggota")
		}
		hold, err := q.GetHold(ctx, *s.HoldID)
		if err != nil || hold.Status != "active" || !hold.ExpiresAt.After(sq.s.now()) {
			return apperr.Conflict(apperr.CodeHoldExpired, "waktu squad sudah habis")
		}
		if _, err := sq.s.Auth.RequireVerified(ctx, q, userID); err != nil {
			return err
		}
		if sq.s.Fraud.Decision(ctx, userID) == "block" {
			return apperr.ForbiddenCode(apperr.CodeBlocked, "akses ditolak")
		}
		members, err := q.ListSquadMembers(ctx, s.ID)
		if err != nil {
			return err
		}
		for _, m := range members {
			if m.UserID == userID {
				return nil // sudah anggota → idempoten
			}
		}
		if len(members) >= s.Size {
			return apperr.Conflict(apperr.CodeLimitExceeded, "squad sudah penuh")
		}
		ev, err := q.GetEvent(ctx, s.EventID)
		if err != nil {
			return err
		}
		purchased, err := q.UserEventPurchased(ctx, sqlcgen.UserEventPurchasedParams{UserID: userID, EventID: s.EventID})
		if err != nil {
			return err
		}
		if int(purchased)+1 > ev.MaxPerAccount {
			return apperr.Unprocessable(apperr.CodeLimitExceeded, "melebihi batas pembelian per akun untuk event ini").With("limit", ev.MaxPerAccount)
		}
		seat, err := sq.nextSlot(ctx, q, hold.ID, s.ID)
		if err != nil {
			return err
		}
		if err := q.InsertSquadMember(ctx, sqlcgen.InsertSquadMemberParams{SquadID: s.ID, UserID: userID, SeatID: seat}); err != nil {
			return err
		}
		return emit(ctx, q, "user."+s.LeaderID, "squad.joined", map[string]any{"squad_id": s.ID})
	})
	if err != nil {
		return nil, err
	}
	return sq.Get(ctx, userID, squadID)
}

// Invite mengirim email undangan berisi kode (hanya ketua).
func (sq *SquadService) Invite(ctx context.Context, userID, squadID, email string) error {
	s, err := sq.s.Q.GetSquad(ctx, squadID)
	if err != nil || s.LeaderID != userID {
		return apperr.NotFound("squad")
	}
	if s.Status != "holding" {
		return apperr.Conflict(apperr.CodeInvalidState, "squad tidak aktif")
	}
	e, ok := NormalizeEmail(email)
	if !ok {
		return apperr.Validation("email tidak valid").With("field", "email")
	}
	if ok, _, err := sq.s.Limiter.Allow(ctx, "squadinv:"+userID, 20, time.Hour); err == nil && !ok {
		return apperr.TooMany("terlalu banyak undangan")
	}
	leader, _ := sq.s.Q.GetUserByID(ctx, userID)
	ev, _ := sq.s.Q.GetEvent(ctx, s.EventID)
	sq.s.Notifier.Send(ctx, NotifyReq{To: e, Template: "squad_invite", Data: map[string]any{"leader": firstName(leader.Name), "event_title": ev.Title, "code": s.Code, "link": sq.s.Cfg.PublicWebURL + "/squad/" + s.Code}})
	return nil
}

type SquadPayResult struct {
	HoldID string     `json:"hold_id"`
	Order  *OrderView `json:"order"`
}

// Pay = "Bayar bagianku": slot anggota dipindah ke hold anak lalu dibuat order biasa (pembayaran via /orders/:id/pay).
func (sq *SquadService) Pay(ctx context.Context, userID, squadID string, req CreateOrderReq) (*SquadPayResult, error) {
	var childID string
	err := sq.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		s, err := q.GetSquadForUpdate(ctx, squadID)
		if err != nil {
			return apperr.NotFound("squad")
		}
		m, err := q.GetSquadMemberForUpdate(ctx, sqlcgen.GetSquadMemberForUpdateParams{SquadID: squadID, UserID: userID})
		if err != nil {
			return apperr.NotFound("squad")
		}
		if s.Status != "holding" || s.HoldID == nil {
			return apperr.Conflict(apperr.CodeInvalidState, "squad tidak aktif")
		}
		if m.Status != "joined" {
			return apperr.Conflict(apperr.CodeInvalidState, "bagianmu sudah dibayar atau dilepas")
		}
		if m.HoldID != nil { // coba lagi setelah gagal membuat order sebelumnya
			childID = *m.HoldID
			return nil
		}
		parent, err := q.GetHoldForUpdate(ctx, *s.HoldID)
		if err != nil || parent.Status != "active" || !parent.ExpiresAt.After(sq.s.now()) {
			return apperr.Conflict(apperr.CodeHoldExpired, "waktu squad sudah habis")
		}
		child, err := q.InsertHold(ctx, sqlcgen.InsertHoldParams{ID: id.New(), EventID: parent.EventID, UserID: userID, ExpiresAt: parent.ExpiresAt, SquadID: &s.ID})
		if err != nil {
			return err
		}
		if m.SeatID != nil {
			n, err := q.MoveSeatHoldItem(ctx, sqlcgen.MoveSeatHoldItemParams{ToHold: child.ID, FromHold: parent.ID, SeatID: m.SeatID})
			if err != nil {
				return err
			}
			if n != 1 {
				return apperr.Conflict(apperr.CodeSeatTaken, "kursi bagianmu sudah tidak ditahan")
			}
		} else {
			row, err := q.TakeOneFromTierHoldItem(ctx, parent.ID)
			if err != nil {
				return apperr.Conflict(apperr.CodeSoldOut, "kuota bagianmu sudah tidak ditahan")
			}
			if err := q.InsertHoldItem(ctx, sqlcgen.InsertHoldItemParams{HoldID: child.ID, TierID: row.TierID, Qty: 1}); err != nil {
				return err
			}
			if err := q.DeleteEmptyHoldItems(ctx, parent.ID); err != nil {
				return err
			}
		}
		if err := q.SetSquadMemberHold(ctx, sqlcgen.SetSquadMemberHoldParams{SquadID: squadID, UserID: userID, HoldID: &child.ID}); err != nil {
			return err
		}
		childID = child.ID
		return nil
	})
	if err != nil {
		return nil, err
	}
	req.HoldID = childID
	ord, err := sq.s.Order.Create(ctx, userID, req)
	if err != nil {
		return &SquadPayResult{HoldID: childID}, err // hold anak tetap ada; panggil lagi setelah masalah (mis. persetujuan) diselesaikan
	}
	if err := sq.s.Q.SetSquadMemberOrder(ctx, sqlcgen.SetSquadMemberOrderParams{SquadID: squadID, UserID: userID, OrderID: &ord.ID}); err != nil {
		return nil, err
	}
	// order gratis langsung lunas → tandai
	if ord.Status == "paid" {
		_ = sq.s.InTx(ctx, func(q *sqlcgen.Queries) error { return sq.markPaidTx(ctx, q, ord.ID) })
	}
	return &SquadPayResult{HoldID: childID, Order: ord}, nil
}

// markPaidTx dipanggil saat order lunas (settle): anggota → paid; semua lunas → squad completed.
func (sq *SquadService) markPaidTx(ctx context.Context, q *sqlcgen.Queries, orderID string) error {
	sid, err := q.MarkSquadMemberPaidByOrder(ctx, &orderID)
	if err != nil {
		return nil // bukan order squad
	}
	ms, err := q.ListSquadMembers(ctx, sid)
	if err != nil {
		return err
	}
	s, err := q.GetSquad(ctx, sid)
	if err != nil {
		return err
	}
	paid := 0
	for _, m := range ms {
		if m.Status == "paid" {
			paid++
		}
	}
	if paid == s.Size && s.Status == "holding" {
		if err := q.SetSquadStatus(ctx, sqlcgen.SetSquadStatusParams{ID: sid, Status: "completed"}); err != nil {
			return err
		}
		return emit(ctx, q, "user."+s.LeaderID, "squad.completed", map[string]any{"squad_id": sid})
	}
	return emit(ctx, q, "user."+s.LeaderID, "squad.member_paid", map[string]any{"squad_id": sid, "paid": paid})
}

// Extend: ketua memperpanjang SEKALI; hold anak dan order pending anggota ikut diperpanjang.
func (sq *SquadService) Extend(ctx context.Context, userID, squadID string) (*SquadView, error) {
	s, err := sq.s.Q.GetSquad(ctx, squadID)
	if err != nil || s.LeaderID != userID || s.HoldID == nil {
		return nil, apperr.NotFound("squad")
	}
	if s.Status != "holding" {
		return nil, apperr.Conflict(apperr.CodeInvalidState, "squad tidak aktif")
	}
	hv, err := sq.s.Hold.Extend(ctx, userID, *s.HoldID)
	if err != nil {
		return nil, err
	}
	err = sq.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		ms, err := q.ListSquadMembers(ctx, squadID)
		if err != nil {
			return err
		}
		for _, m := range ms {
			if m.HoldID != nil {
				if err := q.ExtendHold(ctx, sqlcgen.ExtendHoldParams{ID: *m.HoldID, ExpiresAt: hv.ExpiresAt, ExtendedAt: ptr(sq.s.now())}); err != nil {
					return err
				}
			}
			if m.OrderID != nil {
				if err := q.ExtendOrderExpiry(ctx, sqlcgen.ExtendOrderExpiryParams{ID: *m.OrderID, ExpiresAt: hv.ExpiresAt}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return sq.Get(ctx, userID, squadID)
}

// Cancel oleh ketua: hanya bila belum ada yang membayar.
func (sq *SquadService) Cancel(ctx context.Context, userID, squadID string) error {
	var affected []string
	err := sq.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		s, err := q.GetSquadForUpdate(ctx, squadID)
		if err != nil || s.LeaderID != userID {
			return apperr.NotFound("squad")
		}
		if s.Status != "holding" {
			return apperr.Conflict(apperr.CodeInvalidState, "squad tidak aktif")
		}
		ms, err := q.ListSquadMembers(ctx, squadID)
		if err != nil {
			return err
		}
		for _, m := range ms {
			if m.Status == "paid" {
				return apperr.Conflict(apperr.CodeInvalidState, "sudah ada anggota yang membayar; squad tidak dapat dibatalkan")
			}
		}
		if err := sq.releaseAll(ctx, q, s, ms); err != nil {
			return err
		}
		affected = append(affected, s.EventID)
		return q.SetSquadStatus(ctx, sqlcgen.SetSquadStatusParams{ID: squadID, Status: "cancelled"})
	})
	if err == nil {
		for _, e := range affected {
			sq.s.Catalog.InvalidateAvailability(ctx, e)
			sq.s.Waitlist.OnInventoryFreed(ctx, e)
		}
	}
	return err
}

// releaseAll melepas hold induk + hold anak aktif + order pending anggota.
func (sq *SquadService) releaseAll(ctx context.Context, q *sqlcgen.Queries, s sqlcgen.Squad, ms []sqlcgen.ListSquadMembersRow) error {
	for _, m := range ms {
		if m.OrderID != nil {
			if ord, err := q.GetOrderForUpdate(ctx, *m.OrderID); err == nil && ord.Status == "pending" {
				if err := sq.s.Order.releaseOrderTx(ctx, q, ord, "cancelled", "released"); err != nil {
					return err
				}
			}
		} else if m.HoldID != nil {
			if h, err := q.GetHoldForUpdate(ctx, *m.HoldID); err == nil && h.Status == "active" {
				if err := sq.s.Hold.releaseTx(ctx, q, h, "released"); err != nil {
					return err
				}
			}
		}
		if m.Status == "joined" {
			if err := q.SetSquadMemberStatus(ctx, sqlcgen.SetSquadMemberStatusParams{SquadID: s.ID, UserID: m.UserID, Status: "released"}); err != nil {
				return err
			}
		}
	}
	if s.HoldID != nil {
		if h, err := q.GetHoldForUpdate(ctx, *s.HoldID); err == nil && h.Status == "active" {
			if err := sq.s.Hold.releaseTx(ctx, q, h, "released"); err != nil {
				return err
			}
		}
	}
	return nil
}

// Sweep (scheduler): squad yang timer-nya habis. release_unpaid → bagian belum bayar dilepas, anggota lain tetap.
// cancel_all → semua yang sudah bayar di-refund penuh (otomatis) dan squad dibatalkan.
func (sq *SquadService) Sweep(ctx context.Context) (int, error) {
	due, err := sq.s.Q.SquadsDue(ctx, sq.s.now().Add(-time.Minute)) // jeda 1 menit agar expire hold/order selesai dulu
	if err != nil {
		return 0, err
	}
	n := 0
	for _, d := range due {
		var kick []string
		err := sq.s.InTx(ctx, func(q *sqlcgen.Queries) error {
			s, err := q.GetSquadForUpdate(ctx, d.ID)
			if err != nil || s.Status != "holding" {
				return nil
			}
			ms, err := q.ListSquadMembers(ctx, s.ID)
			if err != nil {
				return err
			}
			paid := 0
			for _, m := range ms {
				if m.Status == "paid" {
					paid++
				}
			}
			if err := sq.releaseAll(ctx, q, s, ms); err != nil {
				return err
			}
			if s.Policy == "cancel_all" && paid > 0 && paid < s.Size {
				for _, m := range ms {
					if m.Status != "paid" || m.OrderID == nil {
						continue
					}
					ord, err := q.GetOrderForUpdate(ctx, *m.OrderID)
					if err != nil || ord.Status != "paid" || ord.Total <= 0 {
						continue
					}
					pay, err := q.SettledPaymentForOrder(ctx, ord.ID)
					if err != nil {
						continue
					}
					if _, err := sq.s.Refund.insertTx(ctx, q, ord, pay, ord.Total, "squad dibatalkan: ada anggota yang tidak membayar", "admin", "order"); err != nil {
						return err
					}
					kick = append(kick, ord.ID)
				}
				if err := q.SetSquadStatus(ctx, sqlcgen.SetSquadStatusParams{ID: s.ID, Status: "cancelled"}); err != nil {
					return err
				}
			} else if err := q.SetSquadStatus(ctx, sqlcgen.SetSquadStatusParams{ID: s.ID, Status: "expired"}); err != nil {
				return err
			}
			return sq.s.Audit.LogSystem(ctx, q, "squad.sweep", "squad", s.ID, map[string]any{"paid": paid, "policy": s.Policy})
		})
		if err != nil {
			return n, err
		}
		for _, oid := range kick {
			sq.s.Refund.kick(ctx, oid)
		}
		n++
	}
	return n, nil
}

// Get: hanya anggota squad.
func (sq *SquadService) Get(ctx context.Context, userID, squadID string) (*SquadView, error) {
	s, err := sq.s.Q.GetSquad(ctx, squadID)
	if err != nil {
		return nil, apperr.NotFound("squad")
	}
	ms, err := sq.s.Q.ListSquadMembers(ctx, s.ID)
	if err != nil {
		return nil, err
	}
	isMember := false
	for _, m := range ms {
		if m.UserID == userID {
			isMember = true
		}
	}
	if !isMember {
		return nil, apperr.NotFound("squad")
	}
	v := &SquadView{ID: s.ID, Code: s.Code, EventID: s.EventID, Size: s.Size, Policy: s.Policy, Status: s.Status, Members: make([]SquadMemberView, 0, len(ms)), Open: s.Size - len(ms)}
	if ev, err := sq.s.Q.GetEvent(ctx, s.EventID); err == nil {
		v.EventTitle, v.EventSlug = ev.Title, ev.Slug
	}
	if s.HoldID != nil {
		if h, err := sq.s.Q.GetHold(ctx, *s.HoldID); err == nil {
			v.ExpiresAt = &h.ExpiresAt
		}
		if s.LeaderID == userID {
			v.HoldID = *s.HoldID
		}
	}
	for _, m := range ms {
		mv := SquadMemberView{UserID: m.UserID, Name: firstName(m.UserName), Seat: m.SeatID, Status: m.Status, Leader: m.UserID == s.LeaderID}
		if m.SeatID != nil {
			if st, err := sq.s.Q.GetSeatLabel(ctx, *m.SeatID); err == nil {
				mv.Label = &st
			}
		}
		if m.Status == "paid" {
			v.Paid++
		}
		if m.UserID == userID && m.HoldID != nil {
			v.MyHoldID = *m.HoldID
		}
		v.Members = append(v.Members, mv)
	}
	return v, nil
}

func (sq *SquadService) Mine(ctx context.Context, userID string) ([]SquadView, error) {
	rows, err := sq.s.Q.SquadsOfUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]SquadView, 0, len(rows))
	for _, r := range rows {
		if v, err := sq.Get(ctx, userID, r.ID); err == nil {
			out = append(out, *v)
		}
	}
	return out, nil
}
