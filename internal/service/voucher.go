package service

import (
	"context"
	"strings"
	"time"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/repository/sqlcgen"
)

// VoucherService = dompet voucher akun: tukar kode promo ke akun, daftar + status, hapus.
// Pemotongan harga tetap dihitung server saat quote/order (PromoService.evaluate).
type VoucherService struct{ s *Services }

func newVoucherService(s *Services) *VoucherService { return &VoucherService{s: s} }

type VoucherView struct {
	PromoID    string     `json:"promo_id"`
	Code       string     `json:"code"`
	Kind       string     `json:"kind"`
	Value      int64      `json:"value"`
	MinTotal   int64      `json:"min_total"`
	TierIDs    []string   `json:"tier_ids"`
	StartsAt   *time.Time `json:"starts_at"`
	EndsAt     *time.Time `json:"ends_at"`
	Status     string     `json:"status"` // available | upcoming | used | expired | exhausted
	EventID    string     `json:"event_id"`
	EventSlug  string     `json:"event_slug"`
	EventTitle string     `json:"event_title"`
	SavedAt    time.Time  `json:"saved_at"`
}

func voucherStatus(r sqlcgen.ListUserVouchersRow, now time.Time) string {
	switch {
	case !r.Active || (r.EndsAt != nil && now.After(*r.EndsAt)):
		return "expired"
	case r.MyUses >= r.MaxPerAccount:
		return "used"
	case r.MaxUses != nil && r.Used >= *r.MaxUses:
		return "exhausted"
	case r.StartsAt != nil && now.Before(*r.StartsAt):
		return "upcoming"
	}
	return "available"
}

func (v *VoucherService) List(ctx context.Context, userID string) ([]VoucherView, error) {
	rows, err := v.s.Q.ListUserVouchers(ctx, userID)
	if err != nil {
		return nil, err
	}
	now := v.s.now()
	out := make([]VoucherView, 0, len(rows))
	for _, r := range rows {
		tiers := r.TierIds
		if tiers == nil {
			tiers = []string{}
		}
		out = append(out, VoucherView{PromoID: r.PromoID, Code: r.Code, Kind: r.Kind, Value: r.Value, MinTotal: r.MinTotal,
			TierIDs: tiers, StartsAt: r.StartsAt, EndsAt: r.EndsAt, Status: voucherStatus(r, now),
			EventID: r.EventID, EventSlug: r.EventSlug, EventTitle: r.EventTitle, SavedAt: r.SavedAt})
	}
	return out, nil
}

// Redeem menyimpan semua promo publik berkode itu (kode unik per event, bisa sama antar event) ke akun.
func (v *VoucherService) Redeem(ctx context.Context, userID, code string) ([]VoucherView, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if !promoCodeRe.MatchString(code) {
		return nil, apperr.BadRequest(apperr.CodePromoInvalid, "kode voucher tidak valid")
	}
	promos, err := v.s.Q.FindPublicPromosByCode(ctx, code)
	if err != nil {
		return nil, err
	}
	if len(promos) == 0 {
		return nil, apperr.BadRequest(apperr.CodePromoInvalid, "kode voucher tidak ditemukan")
	}
	now := v.s.now()
	saved := 0
	for _, p := range promos {
		if !p.Active || (p.EndsAt != nil && now.After(*p.EndsAt)) {
			continue
		}
		if err := v.s.Q.SaveUserVoucher(ctx, sqlcgen.SaveUserVoucherParams{UserID: userID, PromoID: p.ID}); err != nil {
			return nil, err
		}
		saved++
	}
	if saved == 0 {
		return nil, apperr.BadRequest(apperr.CodePromoInvalid, "kode voucher sudah tidak berlaku")
	}
	all, err := v.List(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]VoucherView, 0, saved)
	for _, w := range all {
		if w.Code == code {
			out = append(out, w)
		}
	}
	return out, nil
}

func (v *VoucherService) Remove(ctx context.Context, userID, promoID string) error {
	n, err := v.s.Q.DeleteUserVoucher(ctx, sqlcgen.DeleteUserVoucherParams{UserID: userID, PromoID: promoID})
	if err != nil {
		return err
	}
	if n == 0 {
		return apperr.NotFound("voucher")
	}
	return nil
}
