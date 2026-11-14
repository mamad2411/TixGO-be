package service

import (
	"context"
	"encoding/json"
	"net/netip"

	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/repository/sqlcgen"
	"tixgo/backend/internal/reqmeta"
)

// Auditor menulis audit_logs (docs/12 §5: aksi sensitif harus tercatat; tidak bisa diubah via API).
type Auditor struct{ s *Services }

func newAuditor(s *Services) *Auditor { return &Auditor{s: s} }

// Log mencatat dalam transaksi pemanggil (q terikat tx) agar atomik dengan perubahan datanya.
func (a *Auditor) Log(ctx context.Context, q *sqlcgen.Queries, actor *auth.Principal, action, entity, entityID string, before, after any) error {
	m := reqmeta.From(ctx)
	p := sqlcgen.InsertAuditParams{ID: id.New(), Action: action, Entity: entity, EntityID: strPtr(entityID), RequestID: strPtr(m.RequestID)}
	if actor != nil {
		p.ActorID, p.ActorRole = strPtr(actor.UserID), strPtr(actor.Role)
	}
	if m.IP != "" {
		if ip, err := netip.ParseAddr(m.IP); err == nil {
			p.Ip = &ip
		}
	}
	if before != nil {
		p.Before, _ = json.Marshal(before)
	}
	if after != nil {
		p.After, _ = json.Marshal(after)
	}
	return q.InsertAudit(ctx, p)
}

// LogSystem = aksi tanpa aktor manusia (worker/webhook).
func (a *Auditor) LogSystem(ctx context.Context, q *sqlcgen.Queries, action, entity, entityID string, after any) error {
	return a.Log(ctx, q, &auth.Principal{UserID: "", Role: "system"}, action, entity, entityID, nil, after)
}
