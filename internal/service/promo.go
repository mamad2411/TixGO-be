package service

import (
	"context"
	"regexp"
	"strings"
	"time"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/platform"
	"tixgo/backend/internal/repository/sqlcgen"
)

// PromoService = kode promo per event: validasi server, konsumsi atomik, laporan (docs/20 M35).
type PromoService struct{ s *Services }

func newPromoService(s *Services) *PromoService { return &PromoService{s: s} }

var promoCodeRe = regexp.MustCompile(`^[A-Z0-9_-]{3,32}$`)

type PromoInput struct {
	Code          string     `json:"code" binding:"required,max=32"`
	Kind          string     `json:"kind" binding:"required,oneof=percent fixed"`
	Value         int64      `json:"value" binding:"required,gt=0"`
	MaxUses       *int       `json:"max_uses" binding:"omitempty,min=1"`
	MaxPerAccount int        `json:"max_per_account" binding:"omitempty,min=1,max=50"`
	StartsAt      *time.Time `json:"starts_at"`
	EndsAt        *time.Time `json:"ends_at"`
	MinTotal      int64      `json:"min_total" binding:"omitempty,gte=0"`
	TierIDs       []string   `json:"tier_ids" binding:"omitempty,max=50,dive,len=26"`
}

type PromoView struct {
	ID            string     `json:"id"`
	Code          string     `json:"code"`
	Kind          string     `json:"kind"`
	Value         int64      `json:"value"`
	MaxUses       *int       `json:"max_uses"`
	Used          int        `json:"used"`
	MaxPerAccount int        `json:"max_per_account"`
	StartsAt      *time.Time `json:"starts_at"`
	EndsAt        *time.Time `json:"ends_at"`
	MinTotal      int64      `json:"min_total"`
	TierIDs       []string   `json:"tier_ids"`
	Active        bool       `json:"active"`
}

func promoView(p sqlcgen.PromoCode) PromoView {
	t := p.TierIds
	if t == nil {
		t = []string{}
	}
	return PromoView{ID: p.ID, Code: p.Code, Kind: p.Kind, Value: p.Value, MaxUses: p.MaxUses, Used: p.Used, MaxPerAccount: p.MaxPerAccount,
		StartsAt: p.StartsAt, EndsAt: p.EndsAt, MinTotal: p.MinTotal, TierIDs: t, Active: p.Active}
}

func (ps *PromoService) Create(ctx context.Context, p *auth.Principal, sc OrgScope, eventID string, in PromoInput) (*PromoView, error) {
	code := strings.ToUpper(strings.TrimSpace(in.Code))
	if !promoCodeRe.MatchString(code) {
		return nil, apperr.Validation("kode promo 3–32 karakter A–Z, 0–9, _ atau -").With("field", "code")
	}
	if in.Kind == "percent" && in.Value > 100 {
		return nil, apperr.Validation("persen maksimal 100").With("field", "value")
	}
	if in.Kind == "fixed" && in.Value > 100_000_000 {
		return nil, apperr.Validation("nominal maksimal 100.000.000").With("field", "value")
	}
	if in.StartsAt != nil && in.EndsAt != nil && !in.EndsAt.After(*in.StartsAt) {
		return nil, apperr.Validation("ends_at harus setelah starts_at").With("field", "ends_at")
	}
	mpa := in.MaxPerAccount
	if mpa == 0 {
		mpa = 1
	}
	var out sqlcgen.PromoCode
	err := ps.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := ps.s.Event.ownEvent(ctx, q, sc, eventID); err != nil {
			return err
		}
		if !sc.IsOwner() {
			return apperr.Forbidden("hanya pemilik organizer yang dapat mengatur promo")
		}
		for _, t := range in.TierIDs {
			tier, err := q.GetTier(ctx, t)
			if err != nil || tier.EventID != eventID {
				return apperr.Validation("tier_ids memuat tier yang bukan milik event ini").With("field", "tier_ids")
			}
		}
		var err error
		out, err = q.InsertPromo(ctx, sqlcgen.InsertPromoParams{ID: id.New(), EventID: eventID, Code: code, Kind: in.Kind, Value: in.Value,
			MaxUses: in.MaxUses, MaxPerAccount: mpa, StartsAt: in.StartsAt, EndsAt: in.EndsAt, MinTotal: in.MinTotal, TierIds: in.TierIDs})
		if err != nil {
			if platform.IsUniqueViolation(err) {
				return apperr.Conflict(apperr.CodeConflict, "kode promo sudah ada")
			}
			return err
		}
		return ps.s.Audit.Log(ctx, q, p, "promo.create", "promo", out.ID, nil, map[string]any{"event": eventID, "code": code})
	})
	if err != nil {
		return nil, err
	}
	v := promoView(out)
	return &v, nil
}

func (ps *PromoService) List(ctx context.Context, sc OrgScope, eventID string) ([]PromoView, error) {
	if _, err := ps.s.Event.ownEvent(ctx, ps.s.Q, sc, eventID); err != nil {
		return nil, err
	}
	rows, err := ps.s.Q.ListPromos(ctx, eventID)
	if err != nil {
		return nil, err
	}
	out := make([]PromoView, 0, len(rows))
	for _, r := range rows {
		out = append(out, promoView(r))
	}
	return out, nil
}

type PromoPatch struct {
	Active   *bool      `json:"active"`
	MaxUses  *int       `json:"max_uses" binding:"omitempty,min=1"`
	StartsAt *time.Time `json:"starts_at"`
	EndsAt   *time.Time `json:"ends_at"`
}

func (ps *PromoService) Update(ctx context.Context, p *auth.Principal, sc OrgScope, eventID, promoID string, in PromoPatch) (*PromoView, error) {
	var out sqlcgen.PromoCode
	err := ps.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := ps.s.Event.ownEvent(ctx, q, sc, eventID); err != nil {
			return err
		}
		if !sc.IsOwner() {
			return apperr.Forbidden("hanya pemilik organizer")
		}
		cur, err := q.GetPromo(ctx, promoID)
		if err != nil || cur.EventID != eventID {
			return apperr.NotFound("promo")
		}
		if in.MaxUses != nil && *in.MaxUses < cur.Used {
			return apperr.Conflict(apperr.CodeLimitExceeded, "max_uses tidak boleh di bawah pemakaian").With("used", cur.Used)
		}
		out, err = q.UpdatePromo(ctx, sqlcgen.UpdatePromoParams{ID: promoID, Active: in.Active, MaxUses: in.MaxUses, StartsAt: in.StartsAt, EndsAt: in.EndsAt})
		if err != nil {
			return err
		}
		return ps.s.Audit.Log(ctx, q, p, "promo.update", "promo", promoID, nil, nil)
	})
	if err != nil {
		return nil, err
	}
	v := promoView(out)
	return &v, nil
}

type PromoReport struct {
	Redemptions   int64 `json:"redemptions"`
	DiscountTotal int64 `json:"discount_total"`
	Revenue       int64 `json:"revenue"`
}

func (ps *PromoService) Report(ctx context.Context, sc OrgScope, eventID, promoID string) (*PromoReport, error) {
	if _, err := ps.s.Event.ownEvent(ctx, ps.s.Q, sc, eventID); err != nil {
		return nil, err
	}
	cur, err := ps.s.Q.GetPromo(ctx, promoID)
	if err != nil || cur.EventID != eventID {
		return nil, apperr.NotFound("promo")
	}
	r, err := ps.s.Q.PromoReport(ctx, promoID)
	if err != nil {
		return nil, err
	}
	return &PromoReport{Redemptions: r.Redemptions, DiscountTotal: r.DiscountTotal, Revenue: r.Revenue}, nil
}

// evaluate memvalidasi kode untuk pengguna & baris harga (TANPA mengonsumsi).
func (ps *PromoService) evaluate(ctx context.Context, q *sqlcgen.Queries, ev sqlcgen.Event, userID, code string, lines []PriceLine) (*sqlcgen.PromoCode, int64, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	bad := func(msg string) error { return apperr.BadRequest(apperr.CodePromoInvalid, msg) }
	promo, err := q.GetPromoByCode(ctx, sqlcgen.GetPromoByCodeParams{EventID: ev.ID, Code: code})
	if err != nil {
		if platform.IsNoRows(err) {
			return nil, 0, bad("kode promo tidak valid")
		}
		return nil, 0, err
	}
	now := ps.s.now()
	if !promo.Active || (promo.StartsAt != nil && now.Before(*promo.StartsAt)) || (promo.EndsAt != nil && now.After(*promo.EndsAt)) {
		return nil, 0, bad("kode promo tidak berlaku saat ini")
	}
	if promo.MaxUses != nil && promo.Used >= *promo.MaxUses {
		return nil, 0, apperr.Conflict(apperr.CodePromoExhausted, "kuota kode promo sudah habis")
	}
	uses, err := q.PromoUsesByUser(ctx, sqlcgen.PromoUsesByUserParams{PromoID: promo.ID, UserID: userID})
	if err != nil {
		return nil, 0, err
	}
	if int(uses) >= promo.MaxPerAccount {
		return nil, 0, apperr.Conflict(apperr.CodePromoExhausted, "batas pemakaian kode untuk akunmu sudah tercapai")
	}
	d := PromoDiscount(promo, lines)
	if d <= 0 {
		return nil, 0, bad("kode promo tidak berlaku untuk pesanan ini (periksa tier/minimal belanja)")
	}
	return &promo, d, nil
}

// consume mengonsumsi kuota secara atomik dalam transaksi order (T31: tidak pernah melebihi kuota).
func (ps *PromoService) consume(ctx context.Context, q *sqlcgen.Queries, promo sqlcgen.PromoCode, orderID, userID string, discount int64) error {
	n, err := q.ConsumePromo(ctx, promo.ID)
	if err != nil {
		return err
	}
	if n == 0 {
		return apperr.Conflict(apperr.CodePromoExhausted, "kuota kode promo sudah habis")
	}
	return q.InsertPromoRedemption(ctx, sqlcgen.InsertPromoRedemptionParams{ID: id.New(), PromoID: promo.ID, OrderID: orderID, UserID: userID, Discount: discount})
}

// release mengembalikan kuota saat order kedaluwarsa/dibatalkan.
func (ps *PromoService) release(ctx context.Context, q *sqlcgen.Queries, orderID string) error {
	pid, err := q.DeletePromoRedemptionByOrder(ctx, orderID)
	if err != nil {
		if platform.IsNoRows(err) {
			return nil
		}
		return err
	}
	return q.ReleasePromo(ctx, pid)
}
