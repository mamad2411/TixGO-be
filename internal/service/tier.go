package service

import (
	"context"
	"strings"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/platform"
	"tixgo/backend/internal/repository/sqlcgen"
)

// TierService = kategori harga & kuota (GA) + add-on (docs/06 M11, 20 M43).
type TierService struct{ s *Services }

func newTierService(s *Services) *TierService { return &TierService{s: s} }

type TierView struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Price      int64   `json:"price"`
	Quota      int     `json:"quota"`
	Sold       int     `json:"sold"`
	Held       int     `json:"held"`
	CompQuota  int     `json:"comp_quota"`
	CompIssued int     `json:"comp_issued"`
	Color      *string `json:"color"`
	Sort       int     `json:"sort"`
}

func tierView(t sqlcgen.Tier) TierView {
	return TierView{ID: t.ID, Name: t.Name, Price: t.Price, Quota: t.Quota, Sold: t.Sold, Held: t.Held, CompQuota: t.CompQuota, CompIssued: t.CompIssued, Color: t.Color, Sort: t.Sort}
}

func (t *TierService) list(ctx context.Context, q *sqlcgen.Queries, eventID string) ([]TierView, error) {
	rows, err := q.ListTiers(ctx, eventID)
	if err != nil {
		return nil, err
	}
	out := make([]TierView, 0, len(rows))
	for _, r := range rows {
		out = append(out, tierView(r))
	}
	return out, nil
}

type TierInput struct {
	Name      *string `json:"name" binding:"omitempty,max=80"`
	Price     *int64  `json:"price"`
	Quota     *int    `json:"quota"`
	CompQuota *int    `json:"comp_quota"`
	Color     *string `json:"color" binding:"omitempty,max=20"`
	Sort      *int    `json:"sort"`
}

const maxTierPrice = 100_000_000

func (t *TierService) validate(in TierInput, create bool) error {
	if create && (in.Name == nil || in.Price == nil) {
		return apperr.Validation("name dan price wajib")
	}
	if in.Name != nil {
		if l := len([]rune(strings.TrimSpace(*in.Name))); l < 1 || l > 80 {
			return apperr.Validation("nama tier 1–80 karakter").With("field", "name")
		}
	}
	if in.Price != nil && (*in.Price < 0 || *in.Price > maxTierPrice) {
		return apperr.Validation("harga 0–100.000.000").With("field", "price")
	}
	if in.Quota != nil && (*in.Quota < 0 || *in.Quota > 500000) {
		return apperr.Validation("kuota 0–500.000").With("field", "quota")
	}
	if in.CompQuota != nil && (*in.CompQuota < 0 || *in.CompQuota > 100000) {
		return apperr.Validation("comp_quota 0–100.000").With("field", "comp_quota")
	}
	return nil
}

func (t *TierService) ownerEvent(ctx context.Context, q *sqlcgen.Queries, sc OrgScope, eventID string) (sqlcgen.Event, error) {
	if !sc.IsOwner() {
		return sqlcgen.Event{}, apperr.Forbidden("hanya pemilik organizer yang dapat mengatur tier/harga")
	}
	ev, err := t.s.Event.ownEvent(ctx, q, sc, eventID)
	if err != nil {
		return ev, err
	}
	if ev.Status == stCancelled || ev.Status == stFinished {
		return ev, apperr.Conflict(apperr.CodeInvalidState, "event "+ev.Status+" tidak dapat diubah")
	}
	if ev.Status == stInReview {
		return ev, apperr.Conflict(apperr.CodeInvalidState, "event sedang ditinjau; tarik pengajuan dulu")
	}
	return ev, nil
}

func (t *TierService) Create(ctx context.Context, p *auth.Principal, sc OrgScope, eventID string, in TierInput) (*TierView, error) {
	if err := t.validate(in, true); err != nil {
		return nil, err
	}
	var out sqlcgen.Tier
	err := t.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		ev, err := t.ownerEvent(ctx, q, sc, eventID)
		if err != nil {
			return err
		}
		quota := derefOr(in.Quota, 0)
		if ev.Mode == "seated" {
			quota = 0 // kapasitas kursi ditentukan denah
		} else if quota < 1 {
			return apperr.Validation("kuota tier wajib ≥ 1 untuk event general admission").With("field", "quota")
		}
		out, err = q.InsertTier(ctx, sqlcgen.InsertTierParams{ID: id.New(), EventID: eventID, Name: strings.TrimSpace(*in.Name), Price: *in.Price,
			Quota: quota, CompQuota: derefOr(in.CompQuota, 0), Color: in.Color, Sort: derefOr(in.Sort, 0)})
		if err != nil {
			return err
		}
		// event approved yang ditambah tier berharga perlu tinjau ulang
		if ev.Status == stApproved {
			if _, err := q.SetEventStatus(ctx, sqlcgen.SetEventStatusParams{ID: eventID, Status: stDraft, Note: strPtr("tier ditambah; ajukan ulang")}); err != nil {
				return err
			}
		}
		return t.s.Audit.Log(ctx, q, p, "tier.create", "tier", out.ID, nil, map[string]any{"event": eventID, "price": out.Price})
	})
	if err != nil {
		return nil, err
	}
	v := tierView(out)
	return &v, nil
}

func (t *TierService) Update(ctx context.Context, p *auth.Principal, sc OrgScope, eventID, tierID string, in TierInput) (*TierView, error) {
	if err := t.validate(in, false); err != nil {
		return nil, err
	}
	var out sqlcgen.Tier
	err := t.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		ev, err := t.ownerEvent(ctx, q, sc, eventID)
		if err != nil {
			return err
		}
		cur, err := q.GetTierForUpdate(ctx, tierID)
		if err != nil || cur.EventID != eventID {
			return apperr.NotFound("tier")
		}
		arg := sqlcgen.UpdateTierParams{ID: tierID, Name: in.Name, Price: in.Price, Quota: in.Quota, CompQuota: in.CompQuota, Color: in.Color, Sort: in.Sort}
		if ev.Mode == "seated" {
			arg.Quota = nil
		}
		if arg.Quota != nil && *arg.Quota < cur.Sold+cur.Held {
			return apperr.Conflict(apperr.CodeLimitExceeded, "kuota tidak boleh di bawah tiket terjual + ditahan").With("min_quota", cur.Sold+cur.Held)
		}
		if in.CompQuota != nil && *in.CompQuota < cur.CompIssued {
			return apperr.Conflict(apperr.CodeLimitExceeded, "comp_quota tidak boleh di bawah comp yang sudah diterbitkan").With("min", cur.CompIssued)
		}
		out, err = q.UpdateTier(ctx, arg)
		if err != nil {
			return err
		}
		if in.Price != nil && *in.Price != cur.Price {
			if ev.Status == stApproved {
				if _, err := q.SetEventStatus(ctx, sqlcgen.SetEventStatusParams{ID: eventID, Status: stDraft, Note: strPtr("harga berubah; ajukan ulang")}); err != nil {
					return err
				}
			}
			// perubahan harga hanya berlaku untuk penjualan baru; order lama memakai snapshot (docs/20 M37 AC7)
			return t.s.Audit.Log(ctx, q, p, "tier.price_change", "tier", tierID, map[string]any{"price": cur.Price}, map[string]any{"price": out.Price, "sold": cur.Sold})
		}
		return t.s.Audit.Log(ctx, q, p, "tier.update", "tier", tierID, nil, nil)
	})
	if err != nil {
		return nil, err
	}
	v := tierView(out)
	return &v, nil
}

func (t *TierService) Delete(ctx context.Context, p *auth.Principal, sc OrgScope, eventID, tierID string) error {
	return t.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		ev, err := t.ownerEvent(ctx, q, sc, eventID)
		if err != nil {
			return err
		}
		cur, err := q.GetTier(ctx, tierID)
		if err != nil || cur.EventID != eventID {
			return apperr.NotFound("tier")
		}
		n, err := q.DeleteTier(ctx, tierID)
		if err != nil {
			if platform.IsForeignKeyViolation(err) {
				return apperr.Conflict(apperr.CodeInvalidState, "tier memiliki kursi/penjualan; tidak dapat dihapus")
			}
			return err
		}
		if n == 0 {
			return apperr.Conflict(apperr.CodeInvalidState, "tier sudah memiliki penjualan/penahanan; tidak dapat dihapus")
		}
		_ = ev
		return t.s.Audit.Log(ctx, q, p, "tier.delete", "tier", tierID, map[string]any{"name": cur.Name}, nil)
	})
}

func (t *TierService) List(ctx context.Context, sc OrgScope, eventID string) ([]TierView, error) {
	if _, err := t.s.Event.ownEvent(ctx, t.s.Q, sc, eventID); err != nil {
		return nil, err
	}
	return t.list(ctx, t.s.Q, eventID)
}

// ---------- Add-on (M43) ----------

type AddonView struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Price  int64  `json:"price"`
	Quota  int    `json:"quota"`
	Sold   int    `json:"sold"`
	Active bool   `json:"active"`
}

func addonView(a sqlcgen.Addon) AddonView {
	return AddonView{ID: a.ID, Name: a.Name, Price: a.Price, Quota: a.Quota, Sold: a.Sold, Active: a.Active}
}

type AddonInput struct {
	Name   *string `json:"name" binding:"omitempty,max=80"`
	Price  *int64  `json:"price"`
	Quota  *int    `json:"quota"`
	Active *bool   `json:"active"`
}

func (t *TierService) CreateAddon(ctx context.Context, p *auth.Principal, sc OrgScope, eventID string, in AddonInput) (*AddonView, error) {
	if in.Name == nil || in.Price == nil || in.Quota == nil {
		return nil, apperr.Validation("name, price, dan quota wajib")
	}
	if l := len([]rune(strings.TrimSpace(*in.Name))); l < 1 || l > 80 {
		return nil, apperr.Validation("nama add-on 1–80 karakter").With("field", "name")
	}
	if *in.Price < 0 || *in.Price > 10_000_000 || *in.Quota < 0 || *in.Quota > 100000 {
		return nil, apperr.Validation("harga 0–10.000.000 dan kuota 0–100.000")
	}
	var out sqlcgen.Addon
	err := t.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := t.s.Event.ownEvent(ctx, q, sc, eventID); err != nil {
			return err
		}
		if !sc.IsOwner() {
			return apperr.Forbidden("hanya pemilik organizer")
		}
		var err error
		out, err = q.InsertAddon(ctx, sqlcgen.InsertAddonParams{ID: id.New(), EventID: eventID, Name: strings.TrimSpace(*in.Name), Price: *in.Price, Quota: *in.Quota})
		if err != nil {
			return err
		}
		return t.s.Audit.Log(ctx, q, p, "addon.create", "addon", out.ID, nil, map[string]any{"event": eventID})
	})
	if err != nil {
		return nil, err
	}
	v := addonView(out)
	return &v, nil
}

func (t *TierService) UpdateAddon(ctx context.Context, p *auth.Principal, sc OrgScope, eventID, addonID string, in AddonInput) (*AddonView, error) {
	if !sc.IsOwner() {
		return nil, apperr.Forbidden("hanya pemilik organizer")
	}
	var out sqlcgen.Addon
	err := t.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := t.s.Event.ownEvent(ctx, q, sc, eventID); err != nil {
			return err
		}
		cur, err := q.GetAddon(ctx, addonID)
		if err != nil || cur.EventID != eventID {
			return apperr.NotFound("add-on")
		}
		if in.Quota != nil && *in.Quota < cur.Sold {
			return apperr.Conflict(apperr.CodeLimitExceeded, "kuota tidak boleh di bawah add-on terjual").With("min_quota", cur.Sold)
		}
		out, err = q.UpdateAddon(ctx, sqlcgen.UpdateAddonParams{ID: addonID, Name: in.Name, Price: in.Price, Quota: in.Quota, Active: in.Active})
		return err
	})
	if err != nil {
		return nil, err
	}
	v := addonView(out)
	return &v, nil
}

func (t *TierService) Addons(ctx context.Context, sc OrgScope, eventID string) ([]AddonView, error) {
	if _, err := t.s.Event.ownEvent(ctx, t.s.Q, sc, eventID); err != nil {
		return nil, err
	}
	rows, err := t.s.Q.ListAddons(ctx, sqlcgen.ListAddonsParams{EventID: eventID, OnlyActive: false})
	if err != nil {
		return nil, err
	}
	out := make([]AddonView, 0, len(rows))
	for _, r := range rows {
		out = append(out, addonView(r))
	}
	return out, nil
}
