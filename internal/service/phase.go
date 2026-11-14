package service

import (
	"context"
	"strings"
	"time"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/platform"
	"tixgo/backend/internal/repository/sqlcgen"
)

// PhaseService = fase penjualan (publik/kode/fan), kode presale, registrasi fans (docs/20 M6).
type PhaseService struct{ s *Services }

func newPhaseService(s *Services) *PhaseService { return &PhaseService{s: s} }

type PhaseView struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Audience    string     `json:"audience"`
	StartsAt    time.Time  `json:"starts_at"`
	EndsAt      time.Time  `json:"ends_at"`
	QueueOpenAt *time.Time `json:"queue_open_at"`
}

func phaseView(p sqlcgen.SalePhase) PhaseView {
	return PhaseView{ID: p.ID, Name: p.Name, Audience: p.Audience, StartsAt: p.StartsAt, EndsAt: p.EndsAt, QueueOpenAt: p.QueueOpenAt}
}

func (ph *PhaseService) list(ctx context.Context, q *sqlcgen.Queries, eventID string) ([]PhaseView, error) {
	rows, err := q.ListPhases(ctx, eventID)
	if err != nil {
		return nil, err
	}
	out := make([]PhaseView, 0, len(rows))
	for _, r := range rows {
		out = append(out, phaseView(r))
	}
	return out, nil
}

type PhaseInput struct {
	Name        *string    `json:"name" binding:"omitempty,max=80"`
	Audience    *string    `json:"audience" binding:"omitempty,oneof=public code fan"`
	StartsAt    *time.Time `json:"starts_at"`
	EndsAt      *time.Time `json:"ends_at"`
	QueueOpenAt *time.Time `json:"queue_open_at"`
}

// validatePhase memeriksa jendela & tumpang tindih fase dengan audiens yang sama.
func (ph *PhaseService) validatePhase(ctx context.Context, q *sqlcgen.Queries, ev sqlcgen.Event, selfID, audience string, starts, ends time.Time) error {
	if !ends.After(starts) {
		return apperr.Validation("ends_at harus setelah starts_at").With("field", "ends_at")
	}
	limit := ev.StartsAt
	if ev.EndsAt != nil {
		limit = *ev.EndsAt
	}
	if ends.After(limit) {
		return apperr.Validation("fase penjualan tidak boleh melewati akhir acara").With("field", "ends_at")
	}
	phases, err := q.ListPhases(ctx, ev.ID)
	if err != nil {
		return err
	}
	for _, p := range phases {
		if p.ID == selfID || p.Audience != audience {
			continue
		}
		if starts.Before(p.EndsAt) && ends.After(p.StartsAt) {
			return apperr.Conflict(apperr.CodeConflict, "fase dengan audiens yang sama tidak boleh tumpang tindih").With("phase_id", p.ID)
		}
	}
	return nil
}

func (ph *PhaseService) Create(ctx context.Context, p *auth.Principal, sc OrgScope, eventID string, in PhaseInput) (*PhaseView, error) {
	if in.Name == nil || in.Audience == nil || in.StartsAt == nil || in.EndsAt == nil {
		return nil, apperr.Validation("name, audience, starts_at, ends_at wajib")
	}
	if a := *in.Audience; a != "public" && a != "code" && a != "fan" {
		return nil, apperr.Validation("audience harus public, code, atau fan").With("field", "audience")
	}
	if l := len([]rune(strings.TrimSpace(*in.Name))); l < 1 || l > 80 {
		return nil, apperr.Validation("nama fase 1–80 karakter").With("field", "name")
	}
	var out sqlcgen.SalePhase
	err := ph.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		ev, err := ph.s.Event.ownEvent(ctx, q, sc, eventID)
		if err != nil {
			return err
		}
		if ev.Status == stCancelled || ev.Status == stFinished || ev.Status == stInReview {
			return apperr.Conflict(apperr.CodeInvalidState, "event "+ev.Status+" tidak dapat diubah")
		}
		if err := ph.validatePhase(ctx, q, ev, "", *in.Audience, *in.StartsAt, *in.EndsAt); err != nil {
			return err
		}
		qo := in.QueueOpenAt
		if qo == nil && ev.QueueRequired {
			t := in.StartsAt.Add(-time.Duration(ev.QueuePreOpenMin) * time.Minute)
			qo = &t
		}
		if qo != nil && qo.After(*in.StartsAt) {
			return apperr.Validation("queue_open_at tidak boleh setelah starts_at").With("field", "queue_open_at")
		}
		out, err = q.InsertPhase(ctx, sqlcgen.InsertPhaseParams{ID: id.New(), EventID: eventID, Name: strings.TrimSpace(*in.Name), Audience: *in.Audience, StartsAt: *in.StartsAt, EndsAt: *in.EndsAt, QueueOpenAt: qo})
		if err != nil {
			return err
		}
		return ph.s.Audit.Log(ctx, q, p, "phase.create", "sale_phase", out.ID, nil, map[string]any{"event": eventID, "audience": out.Audience})
	})
	if err != nil {
		return nil, err
	}
	v := phaseView(out)
	return &v, nil
}

func (ph *PhaseService) Update(ctx context.Context, p *auth.Principal, sc OrgScope, eventID, phaseID string, in PhaseInput) (*PhaseView, error) {
	var out sqlcgen.SalePhase
	err := ph.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		ev, err := ph.s.Event.ownEvent(ctx, q, sc, eventID)
		if err != nil {
			return err
		}
		cur, err := q.GetPhase(ctx, phaseID)
		if err != nil || cur.EventID != eventID {
			return apperr.NotFound("fase")
		}
		starts, ends := cur.StartsAt, cur.EndsAt
		if in.StartsAt != nil {
			starts = *in.StartsAt
		}
		if in.EndsAt != nil {
			ends = *in.EndsAt
		}
		if cur.StartsAt.Before(ph.s.now()) && in.StartsAt != nil && !in.StartsAt.Equal(cur.StartsAt) {
			return apperr.Conflict(apperr.CodeInvalidState, "fase yang sudah dimulai tidak dapat dimajukan/dimundurkan awalnya")
		}
		if err := ph.validatePhase(ctx, q, ev, phaseID, cur.Audience, starts, ends); err != nil {
			return err
		}
		out, err = q.UpdatePhase(ctx, sqlcgen.UpdatePhaseParams{ID: phaseID, Name: in.Name, StartsAt: in.StartsAt, EndsAt: in.EndsAt, QueueOpenAt: in.QueueOpenAt})
		if err != nil {
			return err
		}
		return ph.s.Audit.Log(ctx, q, p, "phase.update", "sale_phase", phaseID, nil, nil)
	})
	if err != nil {
		return nil, err
	}
	v := phaseView(out)
	return &v, nil
}

func (ph *PhaseService) Delete(ctx context.Context, p *auth.Principal, sc OrgScope, eventID, phaseID string) error {
	return ph.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := ph.s.Event.ownEvent(ctx, q, sc, eventID); err != nil {
			return err
		}
		cur, err := q.GetPhase(ctx, phaseID)
		if err != nil || cur.EventID != eventID {
			return apperr.NotFound("fase")
		}
		if !cur.StartsAt.After(ph.s.now()) {
			return apperr.Conflict(apperr.CodeInvalidState, "fase yang sudah dimulai tidak dapat dihapus")
		}
		if err := q.DeletePhase(ctx, sqlcgen.DeletePhaseParams{ID: phaseID, EventID: eventID}); err != nil {
			return err
		}
		return ph.s.Audit.Log(ctx, q, p, "phase.delete", "sale_phase", phaseID, nil, nil)
	})
}

func (ph *PhaseService) List(ctx context.Context, sc OrgScope, eventID string) ([]PhaseView, error) {
	if _, err := ph.s.Event.ownEvent(ctx, ph.s.Q, sc, eventID); err != nil {
		return nil, err
	}
	return ph.list(ctx, ph.s.Q, eventID)
}

// ---------- Kode presale ----------

// CreateCodes membuat `count` kode acak (8 karakter) ATAU satu kode kustom; mengembalikan plaintext SEKALI.
func (ph *PhaseService) CreateCodes(ctx context.Context, p *auth.Principal, sc OrgScope, eventID, phaseID string, count int, custom string, maxUses *int) ([]string, error) {
	if count < 0 || count > 1000 {
		return nil, apperr.Validation("count 0–1000").With("field", "count")
	}
	if maxUses != nil && *maxUses < 1 {
		return nil, apperr.Validation("max_uses minimal 1").With("field", "max_uses")
	}
	var codes []string
	if custom != "" {
		c := strings.ToUpper(strings.TrimSpace(custom))
		if len(c) < 4 || len(c) > 32 {
			return nil, apperr.Validation("kode kustom 4–32 karakter").With("field", "code")
		}
		codes = []string{c}
	} else {
		if count == 0 {
			count = 1
		}
		for i := 0; i < count; i++ {
			codes = append(codes, auth.RandomCode(8))
		}
	}
	err := ph.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := ph.s.Event.ownEvent(ctx, q, sc, eventID); err != nil {
			return err
		}
		cur, err := q.GetPhase(ctx, phaseID)
		if err != nil || cur.EventID != eventID {
			return apperr.NotFound("fase")
		}
		if cur.Audience != "code" {
			return apperr.Conflict(apperr.CodeInvalidState, "kode presale hanya untuk fase audiens 'code'")
		}
		for _, c := range codes {
			if err := q.InsertPresaleCode(ctx, sqlcgen.InsertPresaleCodeParams{ID: id.New(), PhaseID: phaseID, CodeHash: auth.HashToken(c), MaxUses: maxUses}); err != nil {
				if platform.IsUniqueViolation(err) {
					return apperr.Conflict(apperr.CodeConflict, "kode sudah ada")
				}
				return err
			}
		}
		return ph.s.Audit.Log(ctx, q, p, "presale.codes.create", "sale_phase", phaseID, nil, map[string]any{"count": len(codes)})
	})
	if err != nil {
		return nil, err
	}
	return codes, nil
}

// Redeem menukar kode presale dengan akses ke fasenya. Salah 5× → kunci 15 menit (docs/06 M6 AC2).
func (ph *PhaseService) Redeem(ctx context.Context, userID, eventID, code string) (*PhaseView, error) {
	lockKey := "presale:lock:" + userID + ":" + eventID
	failKey := "presale:fail:" + userID + ":" + eventID
	if n, _ := ph.s.Redis.Exists(ctx, lockKey).Result(); n > 0 {
		return nil, apperr.New(429, apperr.CodeRateLimited, "terlalu banyak kode salah; coba lagi nanti").With("retry_after", 900)
	}
	code = strings.ToUpper(strings.TrimSpace(code))
	bad := func() error {
		n, _ := ph.s.Redis.Incr(ctx, failKey).Result()
		ph.s.Redis.Expire(ctx, failKey, 15*time.Minute)
		if n >= 5 {
			ph.s.Redis.Set(ctx, lockKey, "1", 15*time.Minute)
			ph.s.Redis.Del(ctx, failKey)
			return apperr.New(429, apperr.CodeRateLimited, "terlalu banyak kode salah; dikunci 15 menit").With("retry_after", 900)
		}
		return apperr.BadRequest(apperr.CodePresaleCodeInvalid, "kode presale tidak valid")
	}
	if code == "" || len(code) > 64 {
		return nil, bad()
	}
	var out sqlcgen.SalePhase
	err := ph.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		pc, err := q.GetPresaleCodeByHash(ctx, auth.HashToken(code))
		if err != nil || pc.EventID != eventID {
			return bad()
		}
		phase, err := q.GetPhase(ctx, pc.PhaseID)
		if err != nil {
			return err
		}
		now := ph.s.now()
		if now.After(phase.EndsAt) {
			return apperr.BadRequest(apperr.CodePresaleCodeInvalid, "fase presale sudah berakhir")
		}
		has, err := q.HasPhaseAccess(ctx, sqlcgen.HasPhaseAccessParams{PhaseID: phase.ID, UserID: userID})
		if err != nil {
			return err
		}
		if !has {
			n, err := q.ConsumePresaleCode(ctx, pc.ID)
			if err != nil {
				return err
			}
			if n == 0 {
				return apperr.BadRequest(apperr.CodePresaleCodeInvalid, "kode sudah habis dipakai")
			}
			if _, err := q.GrantPhaseAccess(ctx, sqlcgen.GrantPhaseAccessParams{PhaseID: phase.ID, UserID: userID, Via: "code"}); err != nil {
				return err
			}
		}
		out = phase
		return nil
	})
	if err != nil {
		return nil, err
	}
	ph.s.Redis.Del(ctx, failKey)
	v := phaseView(out)
	return &v, nil
}

// ---------- Registrasi fans ----------

func (ph *PhaseService) RegisterFan(ctx context.Context, userID, eventID string) error {
	ev, err := ph.s.Q.GetEvent(ctx, eventID)
	if err != nil || ev.Status != stPublished && ev.Status != stPaused {
		return apperr.NotFound("event")
	}
	phases, err := ph.s.Q.ListPhases(ctx, eventID)
	if err != nil {
		return err
	}
	var fan *sqlcgen.SalePhase
	for i := range phases {
		if phases[i].Audience == "fan" {
			fan = &phases[i]
			break
		}
	}
	if fan == nil {
		return apperr.Conflict(apperr.CodeInvalidState, "event ini tidak memiliki fase fan")
	}
	if !ph.s.now().Before(fan.StartsAt) {
		return apperr.Conflict(apperr.CodeSaleNotOpen, "pendaftaran fan sudah ditutup (fase sudah dimulai)")
	}
	if _, err := ph.s.Auth.RequireVerified(ctx, ph.s.Q, userID); err != nil {
		return err
	}
	return ph.s.Q.RegisterFan(ctx, sqlcgen.RegisterFanParams{EventID: eventID, UserID: userID})
}

type FanView struct {
	UserID    string    `json:"user_id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

func (ph *PhaseService) FanRegistrations(ctx context.Context, sc OrgScope, eventID, status string, limit, offset int) ([]FanView, int, error) {
	if _, err := ph.s.Event.ownEvent(ctx, ph.s.Q, sc, eventID); err != nil {
		return nil, 0, err
	}
	rows, err := ph.s.Q.ListFanRegistrations(ctx, sqlcgen.ListFanRegistrationsParams{EventID: eventID, Status: strPtr(status), Lim: int32(limit), Off: int32(offset)})
	if err != nil {
		return nil, 0, err
	}
	n, err := ph.s.Q.CountFanRegistrations(ctx, sqlcgen.CountFanRegistrationsParams{EventID: eventID, Status: strPtr(status)})
	if err != nil {
		return nil, 0, err
	}
	out := make([]FanView, 0, len(rows))
	for _, r := range rows {
		out = append(out, FanView{UserID: r.UserID, Name: r.Name, Email: r.Email, Status: r.Status, CreatedAt: r.CreatedAt})
	}
	return out, int(n), nil
}

// SelectFans memilih fans (eksplisit atau acak N) dan memberi akses ke fase 'fan'.
func (ph *PhaseService) SelectFans(ctx context.Context, p *auth.Principal, sc OrgScope, eventID string, userIDs []string, randomN int) (int, error) {
	if !sc.IsOwner() {
		return 0, apperr.Forbidden("hanya pemilik organizer")
	}
	granted := 0
	err := ph.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := ph.s.Event.ownEvent(ctx, q, sc, eventID); err != nil {
			return err
		}
		phases, err := q.ListPhases(ctx, eventID)
		if err != nil {
			return err
		}
		var fan *sqlcgen.SalePhase
		for i := range phases {
			if phases[i].Audience == "fan" {
				fan = &phases[i]
			}
		}
		if fan == nil {
			return apperr.Conflict(apperr.CodeInvalidState, "event tidak memiliki fase fan")
		}
		seen := map[string]bool{}
		ids := make([]string, 0, len(userIDs))
		for _, u := range userIDs {
			if !seen[u] {
				seen[u] = true
				ids = append(ids, u)
			}
		}
		if randomN > 0 {
			picked, err := q.RandomFansToSelect(ctx, sqlcgen.RandomFansToSelectParams{EventID: eventID, Exclude: ids, Lim: int32(randomN)})
			if err != nil {
				return err
			}
			ids = append(ids, picked...)
		}
		if len(ids) == 0 {
			return apperr.Validation("tidak ada fan yang dipilih")
		}
		if _, err := q.SetFanStatus(ctx, sqlcgen.SetFanStatusParams{EventID: eventID, NewStatus: "selected", UserIds: ids}); err != nil {
			return err
		}
		for _, uid := range ids {
			reg, err := q.GetFanRegistration(ctx, sqlcgen.GetFanRegistrationParams{EventID: eventID, UserID: uid})
			if err != nil || reg.Status != "selected" {
				continue
			}
			n, err := q.GrantPhaseAccess(ctx, sqlcgen.GrantPhaseAccessParams{PhaseID: fan.ID, UserID: uid, Via: "fan"})
			if err != nil {
				return err
			}
			granted += int(n)
		}
		return ph.s.Audit.Log(ctx, q, p, "fan.select", "event", eventID, nil, map[string]any{"granted": granted})
	})
	return granted, err
}

// ---------- Status penjualan (dipakai antrean/hold/order) ----------

// ActivePhases mengembalikan fase yang sedang berjalan pada waktu t.
func (ph *PhaseService) ActivePhases(ctx context.Context, q *sqlcgen.Queries, eventID string, t time.Time) ([]sqlcgen.SalePhase, error) {
	rows, err := q.ListPhases(ctx, eventID)
	if err != nil {
		return nil, err
	}
	var out []sqlcgen.SalePhase
	for _, r := range rows {
		if !t.Before(r.StartsAt) && t.Before(r.EndsAt) {
			out = append(out, r)
		}
	}
	return out, nil
}

// EligiblePhase: fase aktif yang boleh dimasuki pengguna (publik, atau punya akses code/fan). nil = tidak ada.
func (ph *PhaseService) EligiblePhase(ctx context.Context, q *sqlcgen.Queries, eventID, userID string, t time.Time) (*sqlcgen.SalePhase, error) {
	active, err := ph.ActivePhases(ctx, q, eventID, t)
	if err != nil {
		return nil, err
	}
	for i := range active {
		if active[i].Audience == "public" {
			return &active[i], nil
		}
	}
	for i := range active {
		has, err := q.HasPhaseAccess(ctx, sqlcgen.HasPhaseAccessParams{PhaseID: active[i].ID, UserID: userID})
		if err != nil {
			return nil, err
		}
		if has {
			return &active[i], nil
		}
	}
	return nil, nil
}
