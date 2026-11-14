package service

import (
	"context"
	"strings"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/repository/sqlcgen"
)

// GateAdminService = pengaturan pintu & penugasan staf gate per event (docs/20 M10).
type GateAdminService struct{ s *Services }

func newGateAdminService(s *Services) *GateAdminService { return &GateAdminService{s: s} }

type GateView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type StaffView struct {
	UserID string  `json:"user_id"`
	Name   string  `json:"name"`
	Email  string  `json:"email"`
	GateID *string `json:"gate_id"`
}

func (g *GateAdminService) CreateGate(ctx context.Context, p *auth.Principal, sc OrgScope, eventID, name string) (*GateView, error) {
	name = strings.TrimSpace(name)
	if l := len([]rune(name)); l < 1 || l > 40 {
		return nil, apperr.Validation("nama pintu 1–40 karakter").With("field", "name")
	}
	var out sqlcgen.Gate
	err := g.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := g.s.Event.ownEvent(ctx, q, sc, eventID); err != nil {
			return err
		}
		var err error
		out, err = q.InsertGate(ctx, sqlcgen.InsertGateParams{ID: id.New(), EventID: eventID, Name: name})
		if err != nil {
			return apperr.Conflict(apperr.CodeConflict, "nama pintu sudah ada")
		}
		return g.s.Audit.Log(ctx, q, p, "gate.create", "event", eventID, nil, map[string]any{"name": name})
	})
	if err != nil {
		return nil, err
	}
	return &GateView{ID: out.ID, Name: out.Name}, nil
}

func (g *GateAdminService) Gates(ctx context.Context, sc OrgScope, eventID string) ([]GateView, error) {
	if _, err := g.s.Event.ownEvent(ctx, g.s.Q, sc, eventID); err != nil {
		return nil, err
	}
	rows, err := g.s.Q.ListGates(ctx, eventID)
	if err != nil {
		return nil, err
	}
	out := make([]GateView, 0, len(rows))
	for _, r := range rows {
		out = append(out, GateView{ID: r.ID, Name: r.Name})
	}
	return out, nil
}

func (g *GateAdminService) DeleteGate(ctx context.Context, p *auth.Principal, sc OrgScope, eventID, gateID string) error {
	if _, err := g.s.Event.ownEvent(ctx, g.s.Q, sc, eventID); err != nil {
		return err
	}
	return g.s.Q.DeleteGate(ctx, sqlcgen.DeleteGateParams{ID: gateID, EventID: eventID})
}

// AssignStaff menugaskan anggota tim ber-role gate_staff (organizer yang sama) ke event/pintu.
func (g *GateAdminService) AssignStaff(ctx context.Context, p *auth.Principal, sc OrgScope, eventID, email, gateID string) (*StaffView, error) {
	em, ok := NormalizeEmail(email)
	if !ok {
		return nil, apperr.Validation("email tidak valid").With("field", "email")
	}
	var out StaffView
	err := g.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := g.s.Event.ownEvent(ctx, q, sc, eventID); err != nil {
			return err
		}
		u, err := q.GetUserByEmail(ctx, em)
		if err != nil {
			return apperr.NotFound("pengguna")
		}
		m, err := q.GetMembership(ctx, u.ID)
		if err != nil || m.OrganizerID != sc.OrganizerID {
			return apperr.Conflict(apperr.CodeConflict, "pengguna bukan anggota tim organizer ini")
		}
		if m.Role != "gate_staff" && m.Role != "member" && m.Role != "owner" {
			return apperr.Conflict(apperr.CodeConflict, "peran anggota tidak valid untuk gate")
		}
		var gid *string
		if gateID != "" {
			gate, err := q.ListGates(ctx, eventID)
			if err != nil {
				return err
			}
			found := false
			for _, x := range gate {
				if x.ID == gateID {
					found = true
				}
			}
			if !found {
				return apperr.Validation("pintu tidak ditemukan").With("field", "gate_id")
			}
			gid = &gateID
		}
		if err := q.AssignGateStaff(ctx, sqlcgen.AssignGateStaffParams{EventID: eventID, UserID: u.ID, GateID: gid}); err != nil {
			return err
		}
		out = StaffView{UserID: u.ID, Name: u.Name, Email: u.Email, GateID: gid}
		return g.s.Audit.Log(ctx, q, p, "gate.staff.assign", "event", eventID, nil, map[string]any{"user": u.ID})
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (g *GateAdminService) Staff(ctx context.Context, sc OrgScope, eventID string) ([]StaffView, error) {
	if _, err := g.s.Event.ownEvent(ctx, g.s.Q, sc, eventID); err != nil {
		return nil, err
	}
	rows, err := g.s.Q.ListGateStaff(ctx, eventID)
	if err != nil {
		return nil, err
	}
	out := make([]StaffView, 0, len(rows))
	for _, r := range rows {
		out = append(out, StaffView{UserID: r.UserID, Name: r.Name, Email: r.Email, GateID: r.GateID})
	}
	return out, nil
}

func (g *GateAdminService) RemoveStaff(ctx context.Context, p *auth.Principal, sc OrgScope, eventID, userID string) error {
	if _, err := g.s.Event.ownEvent(ctx, g.s.Q, sc, eventID); err != nil {
		return err
	}
	return g.s.Q.RemoveGateStaff(ctx, sqlcgen.RemoveGateStaffParams{EventID: eventID, UserID: userID})
}
