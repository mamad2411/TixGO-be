package service

import (
	"context"
	"net"
	"strings"
	"time"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/repository/sqlcgen"
)

// AdminUsers = pencarian/blokir/peran pengguna + blacklist & sinyal risiko (docs/12 §4, 20 M12). Semua aksi teraudit.
type AdminUsers struct{ s *Services }

func newAdminUsers(s *Services) *AdminUsers { return &AdminUsers{s: s} }

type AdminUserView struct {
	ID          string     `json:"id"`
	Email       string     `json:"email"`
	Name        string     `json:"name"`
	Role        string     `json:"role"`
	Status      string     `json:"status"`
	MFA         bool       `json:"mfa_enabled"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
	RiskScore   int        `json:"risk_score"`
	Orders      int        `json:"orders,omitempty"`
	Tickets     int        `json:"tickets,omitempty"`
}

func (a *AdminUsers) view(ctx context.Context, u sqlcgen.User) AdminUserView {
	return AdminUserView{ID: u.ID, Email: u.Email, Name: u.Name, Role: u.Role, Status: u.Status, MFA: u.MfaEnabled, CreatedAt: u.CreatedAt, LastLoginAt: u.LastLoginAt, RiskScore: a.s.Fraud.Score(ctx, u.ID)}
}

func (a *AdminUsers) Search(ctx context.Context, p *auth.Principal, q, role, status string, limit, offset int) ([]AdminUserView, error) {
	rows, err := a.s.Q.SearchUsers(ctx, sqlcgen.SearchUsersParams{Q: strPtr(q), Role: strPtr(role), Status: strPtr(status), Lim: int32(limit), Off: int32(offset)})
	if err != nil {
		return nil, err
	}
	out := make([]AdminUserView, 0, len(rows))
	for _, u := range rows {
		out = append(out, a.view(ctx, u))
	}
	_ = a.s.InTx(ctx, func(tx *sqlcgen.Queries) error { // akses data pribadi oleh staf teraudit (M29 AC6)
		return a.s.Audit.Log(ctx, tx, p, "admin.user_search", "user", "", nil, map[string]any{"q": q, "rows": len(out)})
	})
	return out, nil
}

func (a *AdminUsers) Get(ctx context.Context, p *auth.Principal, userID string) (*AdminUserView, error) {
	u, err := a.s.Q.GetUserByID(ctx, userID)
	if err != nil {
		return nil, apperr.NotFound("pengguna")
	}
	v := a.view(ctx, u)
	var o, t int
	_ = a.s.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM orders WHERE user_id=$1), (SELECT count(*) FROM tickets WHERE owner_user_id=$1)`, userID).Scan(&o, &t)
	v.Orders, v.Tickets = o, t
	_ = a.s.InTx(ctx, func(tx *sqlcgen.Queries) error {
		return a.s.Audit.Log(ctx, tx, p, "admin.user_view", "user", userID, nil, nil)
	})
	return &v, nil
}

func (a *AdminUsers) guardTarget(p *auth.Principal, u sqlcgen.User) error {
	if u.ID == p.UserID {
		return apperr.Forbidden("tidak dapat mengubah akun sendiri")
	}
	if u.Role == "admin" {
		return apperr.Forbidden("akun admin tidak dapat diubah dari sini")
	}
	return nil
}

func (a *AdminUsers) SetBlocked(ctx context.Context, p *auth.Principal, userID string, block bool, reason string) error {
	if block && len([]rune(strings.TrimSpace(reason))) < 5 {
		return apperr.Validation("alasan blokir wajib diisi").With("field", "reason")
	}
	err := a.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		u, err := q.GetUserByIDForUpdate(ctx, userID)
		if err != nil {
			return apperr.NotFound("pengguna")
		}
		if err := a.guardTarget(p, u); err != nil {
			return err
		}
		if u.Status == "deleted" {
			return apperr.Conflict(apperr.CodeInvalidState, "akun sudah dihapus")
		}
		st, action := "active", "user.unblock"
		if block {
			st, action = "blocked", "user.block"
		}
		if err := q.SetUserStatus(ctx, sqlcgen.SetUserStatusParams{ID: userID, Status: st}); err != nil {
			return err
		}
		return a.s.Audit.Log(ctx, q, p, action, "user", userID, map[string]any{"status": u.Status}, map[string]any{"status": st, "reason": reason})
	})
	if err == nil && block {
		a.s.Auth.RevokeUserAccess(ctx, userID)
	}
	return err
}

var staffRoles = map[string]bool{"customer": true, "support": true, "finance": true, "editor": true}

// SetRole: hanya antar peran internal non-admin; sesi dicabut agar klaim peran baru berlaku.
func (a *AdminUsers) SetRole(ctx context.Context, p *auth.Principal, userID, role string) error {
	if !staffRoles[role] {
		return apperr.Validation("role harus customer, support, finance, atau editor").With("field", "role")
	}
	err := a.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		u, err := q.GetUserByIDForUpdate(ctx, userID)
		if err != nil {
			return apperr.NotFound("pengguna")
		}
		if err := a.guardTarget(p, u); err != nil {
			return err
		}
		if u.Role == "organizer" || u.Role == "organizer_member" || u.Role == "gate_staff" {
			return apperr.Conflict(apperr.CodeInvalidState, "peran organizer dikelola lewat organizer")
		}
		if u.EmailVerifiedAt == nil {
			return apperr.Conflict(apperr.CodeEmailNotVerified, "email pengguna belum terverifikasi")
		}
		if err := q.SetUserRole(ctx, sqlcgen.SetUserRoleParams{ID: userID, Role: role}); err != nil {
			return err
		}
		return a.s.Audit.Log(ctx, q, p, "user.role", "user", userID, map[string]any{"role": u.Role}, map[string]any{"role": role})
	})
	if err == nil {
		a.s.Auth.RevokeUserAccess(ctx, userID)
	}
	return err
}

type BlacklistInput struct {
	Kind      string     `json:"kind" binding:"required,oneof=user phone device ip"`
	Value     string     `json:"value" binding:"required,max=200"`
	Reason    string     `json:"reason" binding:"required,min=5,max=300"`
	ExpiresAt *time.Time `json:"expires_at"`
}

func (a *AdminUsers) AddBlacklist(ctx context.Context, p *auth.Principal, in BlacklistInput) (*sqlcgen.Blacklist, error) {
	val := strings.TrimSpace(in.Value)
	switch in.Kind {
	case "phone":
		n, ok := NormalizePhone(val)
		if !ok {
			return nil, apperr.Validation("nomor telepon tidak valid").With("field", "value")
		}
		val = n
	case "ip":
		if net.ParseIP(val) == nil {
			return nil, apperr.Validation("alamat IP tidak valid").With("field", "value")
		}
	case "user":
		if !id.Valid(val) {
			return nil, apperr.Validation("value harus ID pengguna").With("field", "value")
		}
		if u, err := a.s.Q.GetUserByID(ctx, val); err != nil || u.Role == "admin" || u.ID == p.UserID {
			return nil, apperr.Forbidden("pengguna ini tidak dapat di-blacklist")
		}
	}
	if in.ExpiresAt != nil && !in.ExpiresAt.After(a.s.now()) {
		return nil, apperr.Validation("expires_at harus di masa depan").With("field", "expires_at")
	}
	var out sqlcgen.Blacklist
	err := a.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		out, err = q.AddBlacklist(ctx, sqlcgen.AddBlacklistParams{ID: id.New(), Kind: in.Kind, ValueHash: hashValue(val), Reason: &in.Reason, ExpiresAt: in.ExpiresAt, CreatedBy: &p.UserID})
		if err != nil {
			return err
		}
		return a.s.Audit.Log(ctx, q, p, "blacklist.add", "blacklist", out.ID, nil, map[string]any{"kind": in.Kind, "reason": in.Reason}) // nilai mentah tidak dicatat
	})
	if err == nil && in.Kind == "user" {
		a.s.Auth.RevokeUserAccess(ctx, val)
	}
	return &out, err
}

func (a *AdminUsers) ListBlacklist(ctx context.Context, limit, offset int) ([]sqlcgen.Blacklist, error) {
	return a.s.Q.ListBlacklist(ctx, sqlcgen.ListBlacklistParams{Limit: int32(limit), Offset: int32(offset)})
}

func (a *AdminUsers) RemoveBlacklist(ctx context.Context, p *auth.Principal, bid string) error {
	return a.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		if err := q.DeleteBlacklist(ctx, bid); err != nil {
			return err
		}
		return a.s.Audit.Log(ctx, q, p, "blacklist.remove", "blacklist", bid, nil, nil)
	})
}

func (a *AdminUsers) Signals(ctx context.Context, userID string, limit, offset int) ([]sqlcgen.RiskSignal, error) {
	return a.s.Q.ListRiskSignals(ctx, sqlcgen.ListRiskSignalsParams{UserID: strPtr(userID), Lim: int32(limit), Off: int32(offset)})
}
