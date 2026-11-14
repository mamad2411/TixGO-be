package service

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"time"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/jobs"
	"tixgo/backend/internal/repository/sqlcgen"
)

// PrivacyService = hak data pengguna: ekspor, hapus (anonimisasi), pengaduan, retensi (docs/20 M29; UU PDP — angka waktu = asumsi (T), konfirmasi hukum).
type PrivacyService struct{ s *Services }

const (
	jobPrivacyExport = "privacy:export"
	dsrSLADays       = 14
	exportTTL        = 7 * 24 * time.Hour
)

func newPrivacyService(s *Services) *PrivacyService {
	p := &PrivacyService{s: s}
	s.Reg.Handle(jobPrivacyExport, p.handleExport)
	return p
}

// ---------- ekspor ----------

type ExportView struct {
	ID        string     `json:"id"`
	Status    string     `json:"status"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	URL       string     `json:"url,omitempty"`
}

func (p *PrivacyService) RequestExport(ctx context.Context, userID string) (*ExportView, error) {
	if ok, ra, err := p.s.Limiter.Allow(ctx, "pexport:"+userID, 3, 24*time.Hour); err == nil && !ok {
		return nil, apperr.TooMany("permintaan ekspor terlalu sering").With("retry_after", int(ra.Seconds())+1)
	}
	job, err := p.s.Q.InsertExportJob(ctx, sqlcgen.InsertExportJobParams{ID: id.New(), UserID: userID, Kind: "privacy", Params: []byte("{}")})
	if err != nil {
		return nil, err
	}
	if err := p.s.Jobs.Enqueue(ctx, jobPrivacyExport, map[string]string{"id": job.ID}, jobs.MaxRetry(3)); err != nil {
		return nil, err
	}
	nj, _ := p.s.Q.GetExportJob(ctx, job.ID)
	return p.exportView(ctx, nj), nil
}

func (p *PrivacyService) exportView(ctx context.Context, j sqlcgen.ExportJob) *ExportView {
	v := &ExportView{ID: j.ID, Status: j.Status, ExpiresAt: j.ExpiresAt, CreatedAt: j.CreatedAt}
	if j.Status == "done" && j.ObjectKey != nil && j.ExpiresAt != nil && j.ExpiresAt.After(p.s.now()) {
		if u, err := p.s.Store.PresignGet(ctx, *j.ObjectKey, 10*time.Minute); err == nil {
			v.URL = u
		}
	}
	return v
}

func (p *PrivacyService) Exports(ctx context.Context, userID string) ([]ExportView, error) {
	rows, err := p.s.Q.ListUserExports(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]ExportView, 0, len(rows))
	for _, r := range rows {
		out = append(out, *p.exportView(ctx, r))
	}
	return out, nil
}

func (p *PrivacyService) one(ctx context.Context, q string, args ...any) json.RawMessage {
	var raw []byte
	if err := p.s.Pool.QueryRow(ctx, "SELECT coalesce(json_agg(t), '[]'::json) FROM ("+q+") t", args...).Scan(&raw); err != nil {
		p.s.Log.Error("ekspor privasi: query gagal", "error", err)
		return json.RawMessage("[]")
	}
	return raw
}

// collect mengumpulkan data milik pengguna SENDIRI (AC7: tidak memuat data pihak lain).
func (p *PrivacyService) collect(ctx context.Context, userID string) (map[string]any, error) {
	u, err := p.s.Q.GetUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"generated_at":       p.s.now(),
		"profile":            map[string]any{"id": u.ID, "email": u.Email, "name": u.Name, "phone": u.Phone, "locale": u.Locale, "created_at": u.CreatedAt},
		"orders":             p.one(ctx, `SELECT id, event_id, status, subtotal, discount, fee, total, created_at, paid_at FROM orders WHERE user_id = $1 ORDER BY created_at`, userID),
		"tickets":            p.one(ctx, `SELECT id, event_id, status, seat_label, entry_number, attendee_name, issued_at, used_at FROM tickets WHERE owner_user_id = $1 ORDER BY issued_at`, userID),
		"refunds":            p.one(ctx, `SELECT r.id, r.order_id, r.amount, r.status, r.method, r.created_at FROM refunds r JOIN orders o ON o.id = r.order_id WHERE o.user_id = $1`, userID),
		"consents":           p.one(ctx, `SELECT document_id, accepted_at FROM consents WHERE user_id = $1`, userID),
		"notification_prefs": p.one(ctx, `SELECT category, channel, enabled FROM notification_prefs WHERE user_id = $1`, userID),
		"favorites":          p.one(ctx, `SELECT event_id FROM favorites WHERE user_id = $1`, userID),
		"follows":            p.one(ctx, `SELECT kind, target FROM follows WHERE user_id = $1`, userID),
		"support_tickets":    p.one(ctx, `SELECT number, category, subject, status, created_at FROM support_tickets WHERE user_id = $1`, userID),
		"support_messages":   p.one(ctx, `SELECT m.ticket_id, m.body, m.created_at FROM support_messages m WHERE m.author_id = $1 AND NOT m.internal`, userID),
	}, nil
}

func (p *PrivacyService) handleExport(ctx context.Context, payload []byte) error {
	pl, err := jobs.Decode[map[string]string](payload)
	if err != nil {
		return nil
	}
	n, err := p.s.Q.SetExportRunning(ctx, pl["id"])
	if err != nil || n == 0 {
		return err
	}
	job, err := p.s.Q.GetExportJob(ctx, pl["id"])
	if err != nil {
		return err
	}
	data, err := p.collect(ctx, job.UserID)
	if err == nil {
		var b []byte
		if b, err = json.MarshalIndent(data, "", "  "); err == nil {
			key := "privacy-export/" + job.UserID + "/" + job.ID + ".json"
			if err = p.s.Store.Put(ctx, key, bytes.NewReader(b), int64(len(b)), "application/json"); err == nil {
				if err = p.s.Q.SetExportDone(ctx, sqlcgen.SetExportDoneParams{ID: job.ID, ObjectKey: &key, ExpiresAt: ptr(p.s.now().Add(exportTTL))}); err == nil {
					p.s.Notifier.Send(ctx, NotifyReq{UserID: job.UserID, Template: "export_ready", Data: map[string]any{
						"name": "", "expires": p.s.now().Add(exportTTL).Format("02 Jan 2006"), "link": p.s.Cfg.PublicWebURL + "/privasi"}})
					return nil
				}
			}
		}
	}
	_ = p.s.Q.SetExportFailed(ctx, sqlcgen.SetExportFailedParams{ID: job.ID, Error: strPtr(err.Error())})
	return err
}

// ---------- hapus akun ----------

type DSRView struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"`
	Status    string     `json:"status"`
	Reason    *string    `json:"reason,omitempty"`
	DueAt     *time.Time `json:"due_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	ClosedAt  *time.Time `json:"closed_at,omitempty"`
}

func dsrView(d sqlcgen.DsrRequest) DSRView {
	return DSRView{ID: d.ID, Kind: d.Kind, Status: d.Status, Reason: d.Reason, DueAt: d.DueAt, CreatedAt: d.CreatedAt, ClosedAt: d.ClosedAt}
}

// RequestDeletion: pengguna harus mengonfirmasi dengan kata sandi (verifikasi identitas awal); SLA 14 hari (T).
func (p *PrivacyService) RequestDeletion(ctx context.Context, userID, password, reason string, sf SecondFactor) (*DSRView, error) {
	u, err := p.s.Q.GetUserByID(ctx, userID)
	if err != nil {
		return nil, apperr.NotFound("akun")
	}
	if !p.s.Hasher.Verify(password, u.PasswordHash) {
		return nil, apperr.Unauthorized("kata sandi salah")
	}
	if err := p.s.Auth.requireStepUp(ctx, p.s.Q, u, sf.Method, sf.Code); err != nil {
		return nil, err
	}
	if open, _ := p.s.Q.OpenDSRForUser(ctx, sqlcgen.OpenDSRForUserParams{UserID: &userID, Kind: "delete"}); open {
		return nil, apperr.Conflict(apperr.CodeConflict, "sudah ada permintaan hapus akun yang berjalan")
	}
	b, err := p.s.Q.DeletionBlockers(ctx, userID)
	if err != nil {
		return nil, err
	}
	detail := map[string]any{"blockers": b}
	d, err := p.s.Q.InsertDSR(ctx, sqlcgen.InsertDSRParams{ID: id.New(), UserID: &userID, Kind: "delete", Reason: strPtr(reason), Detail: mustJSON(detail), DueAt: ptr(p.s.now().Add(dsrSLADays * 24 * time.Hour))})
	if err != nil {
		return nil, err
	}
	// identitas sudah diverifikasi lewat kata sandi → langsung masuk tinjauan
	_ = p.s.Q.SetDSRStatus(ctx, sqlcgen.SetDSRStatusParams{ID: d.ID, Status: "identity_verified"})
	d.Status = "identity_verified"
	v := dsrView(d)
	return &v, nil
}

func (p *PrivacyService) MyRequests(ctx context.Context, userID string) ([]DSRView, error) {
	rows, err := p.s.Q.ListUserDSR(ctx, &userID)
	if err != nil {
		return nil, err
	}
	out := make([]DSRView, 0, len(rows))
	for _, r := range rows {
		out = append(out, dsrView(r))
	}
	return out, nil
}

type DSRAdminView struct {
	DSRView
	UserEmail *string         `json:"user_email,omitempty"`
	Detail    json.RawMessage `json:"detail"`
	Overdue   bool            `json:"overdue"`
}

func (p *PrivacyService) AdminList(ctx context.Context, status string, limit, offset int) ([]DSRAdminView, int, error) {
	rows, err := p.s.Q.ListDSR(ctx, sqlcgen.ListDSRParams{Status: strPtr(status), Lim: int32(limit), Off: int32(offset)})
	if err != nil {
		return nil, 0, err
	}
	n, err := p.s.Q.CountDSR(ctx, strPtr(status))
	if err != nil {
		return nil, 0, err
	}
	out := make([]DSRAdminView, 0, len(rows))
	for _, r := range rows {
		d := sqlcgen.DsrRequest{ID: r.ID, UserID: r.UserID, Kind: r.Kind, Status: r.Status, Reason: r.Reason, DueAt: r.DueAt, CreatedAt: r.CreatedAt, ClosedAt: r.ClosedAt}
		v := DSRAdminView{DSRView: dsrView(d), UserEmail: r.UserEmail, Detail: r.Detail}
		v.Overdue = r.ClosedAt == nil && r.DueAt != nil && r.DueAt.Before(p.s.now())
		out = append(out, v)
	}
	return out, int(n), nil
}

// Reject menolak permintaan DENGAN alasan tertulis (wajib), pengguna diberi tahu.
func (p *PrivacyService) Reject(ctx context.Context, actor *auth.Principal, dsrID, reason string) error {
	if len([]rune(strings.TrimSpace(reason))) < 10 {
		return apperr.Validation("alasan penolakan (min. 10 karakter) wajib untuk pengguna").With("field", "reason")
	}
	var uid *string
	err := p.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		d, err := q.GetDSRForUpdate(ctx, dsrID)
		if err != nil {
			return apperr.NotFound("permintaan")
		}
		if d.ClosedAt != nil {
			return apperr.Conflict(apperr.CodeInvalidState, "permintaan sudah ditutup")
		}
		uid = d.UserID
		if err := q.SetDSRStatus(ctx, sqlcgen.SetDSRStatusParams{ID: dsrID, Status: "rejected_with_reason", Reason: &reason}); err != nil {
			return err
		}
		return p.s.Audit.Log(ctx, q, actor, "dsr.reject", "dsr", dsrID, nil, map[string]any{"reason": reason})
	})
	if err == nil && uid != nil {
		p.s.Notifier.Send(ctx, NotifyReq{UserID: *uid, Template: "dsr_update", Data: map[string]any{"name": "", "kind": "hapus akun", "status": "ditolak", "note": reason}})
	}
	return err
}

// Execute menganonimisasi akun. Data transaksi/pajak/audit tetap (PII diminimalkan). Ditolak bila ada kewajiban berjalan.
func (p *PrivacyService) Execute(ctx context.Context, actor *auth.Principal, dsrID string) error {
	var target string
	err := p.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		d, err := q.GetDSRForUpdate(ctx, dsrID)
		if err != nil {
			return apperr.NotFound("permintaan")
		}
		if d.Kind != "delete" || d.UserID == nil {
			return apperr.Conflict(apperr.CodeInvalidState, "bukan permintaan hapus akun")
		}
		if d.ClosedAt != nil {
			return apperr.Conflict(apperr.CodeInvalidState, "permintaan sudah ditutup")
		}
		uid := *d.UserID
		b, err := q.DeletionBlockers(ctx, uid)
		if err != nil {
			return err
		}
		if b.UpcomingTickets+b.PendingOrders+b.OpenRefunds+b.OwnedOrganizers+b.OpenPayouts > 0 {
			return apperr.Unprocessable(apperr.CodeValidation, "masih ada kewajiban berjalan; selesaikan atau tolak dengan alasan").With("blockers", b)
		}
		for _, f := range []func() error{
			func() error { return q.AnonymizeUser(ctx, uid) },
			func() error { return q.AnonymizeOrderItemsForUser(ctx, uid) },
			func() error { return q.ClearTicketNamesForUser(ctx, uid) },
			func() error { return q.AnonymizeSupportForUser(ctx, uid) },
			func() error { return q.AnonymizeSupportTicketsForUser(ctx, uid) },
			func() error { return q.DeletePassportData(ctx, uid) },
			func() error { return q.DeletePassportBadges(ctx, uid) },
			func() error { return q.WipeUserMfaRecovery(ctx, uid) },
			func() error { return q.WipeUserMfa(ctx, uid) },
			func() error { return q.WipeUserSocial(ctx, uid) },
			func() error { return q.WipeUserPushSubs(ctx, uid) },
			func() error { return q.WipeUserEmailTokens(ctx, uid) },
			func() error { return q.WipeUserNotifPrefs(ctx, uid) },
			func() error { return q.WipeUserFavorites(ctx, uid) },
			func() error { return q.WipeUserFollows(ctx, uid) },
			func() error { return q.WipeUserReminders(ctx, uid) },
			func() error {
				return q.SetDSRStatus(ctx, sqlcgen.SetDSRStatusParams{ID: dsrID, Status: "executed", Reason: strPtr("Akun dianonimisasi. Data transaksi, faktur, dan log audit dipertahankan sesuai kewajiban hukum dengan data pribadi diminimalkan.")})
			},
		} {
			if err := f(); err != nil {
				return err
			}
		}
		target = uid
		return p.s.Audit.Log(ctx, q, actor, "dsr.execute", "user", uid, nil, map[string]any{"dsr": dsrID})
	})
	if err == nil {
		p.s.Auth.RevokeUserAccess(ctx, target) // cabut semua sesi aktif (setelah commit)
	}
	return err
}

// ---------- pengaduan privasi ----------

type ComplaintInput struct {
	Name    string `json:"name" binding:"omitempty,max=100"`
	Email   string `json:"email" binding:"required,email,max=254"`
	Subject string `json:"subject" binding:"required,min=3,max=150"`
	Body    string `json:"body" binding:"required,min=10,max=5000"`
}

func (p *PrivacyService) Complain(ctx context.Context, in ComplaintInput) error {
	c, err := p.s.Q.InsertComplaint(ctx, sqlcgen.InsertComplaintParams{ID: id.New(), Name: strPtr(in.Name), Email: strings.ToLower(in.Email), Subject: in.Subject, Body: in.Body})
	if err != nil {
		return err
	}
	if p.s.Cfg.PrivacyContactEmail != "" { // dirutekan ke penanggung jawab
		p.s.Notifier.Send(ctx, NotifyReq{To: p.s.Cfg.PrivacyContactEmail, Template: "support_received", Data: map[string]any{"name": "Penanggung jawab privasi", "number": c.ID, "subject": in.Subject, "sla": "14 hari"}})
	}
	return nil
}

func (p *PrivacyService) Complaints(ctx context.Context, status string, limit, offset int) ([]sqlcgen.PrivacyComplaint, error) {
	return p.s.Q.ListComplaints(ctx, sqlcgen.ListComplaintsParams{Status: strPtr(status), Lim: int32(limit), Off: int32(offset)})
}

func (p *PrivacyService) CloseComplaint(ctx context.Context, actor *auth.Principal, cid string) error {
	n, err := p.s.Q.CloseComplaint(ctx, cid)
	if err != nil {
		return err
	}
	if n == 0 {
		return apperr.NotFound("pengaduan terbuka")
	}
	return p.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		return p.s.Audit.Log(ctx, q, actor, "complaint.close", "complaint", cid, nil, nil)
	})
}

// ---------- retensi ----------

// Purge (job periodik, idempoten): berkas ekspor kedaluwarsa, notifikasi lama, token kedaluwarsa, kunci idempotensi lama, outbox terbit >1 hari.
func (p *PrivacyService) Purge(ctx context.Context) (int, error) {
	total := 0
	exp, err := p.s.Q.ExpiredExports(ctx, ptr(p.s.now()))
	if err != nil {
		return 0, err
	}
	for _, e := range exp {
		if err := p.s.Store.Delete(ctx, *e.ObjectKey); err != nil {
			p.s.Log.Warn("hapus berkas ekspor gagal", "key", *e.ObjectKey, "error", err)
			continue
		}
		_ = p.s.Q.ClearExportObject(ctx, e.ID)
		total++
	}
	now := p.s.now()
	for _, f := range []func() (int64, error){
		func() (int64, error) {
			return p.s.Q.PurgeOldNotifications(ctx, now.Add(-time.Duration(p.s.Cfg.RetentionNotificationsDays)*24*time.Hour))
		},
		func() (int64, error) { return p.s.Q.PurgeOldTokens(ctx, now.Add(-7*24*time.Hour)) },
		func() (int64, error) { return p.s.Q.PurgeOldRefreshTokens(ctx, now.Add(-30*24*time.Hour)) },
		func() (int64, error) { return p.s.Q.PurgeIdempotency(ctx, now.Add(-48*time.Hour)) },
		func() (int64, error) { return p.s.Q.DeleteOldOutbox(ctx) }, // terbit >1 hari (interval di SQL)
	} {
		n, err := f()
		if err != nil {
			return total, err
		}
		total += int(n)
	}
	if total > 0 {
		_ = p.s.InTx(ctx, func(q *sqlcgen.Queries) error {
			return p.s.Audit.LogSystem(ctx, q, "privacy.purge", "system", "retention", map[string]any{"rows": total})
		})
	}
	return total, nil
}
