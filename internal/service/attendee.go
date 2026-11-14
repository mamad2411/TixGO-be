package service

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"strings"
	"time"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/jobs"
	"tixgo/backend/internal/platform"
	"tixgo/backend/internal/repository/sqlcgen"
)

// AttendeeService = daftar peserta, comp/undangan, impor CSV, reissue/void/rename, siaran pesan (docs/20 M36).
type AttendeeService struct{ s *Services }

const (
	jobBroadcast        = "broadcast:send"
	maxBroadcastsPerDay = 3
	maxCompImportRows   = 500
)

func newAttendeeService(s *Services) *AttendeeService {
	a := &AttendeeService{s: s}
	s.Reg.Handle(jobBroadcast, a.handleBroadcast)
	return a
}

type AttendeeView struct {
	TicketID    string     `json:"ticket_id"`
	Status      string     `json:"status"`
	Origin      string     `json:"origin"`
	EntryNumber *string    `json:"entry_number,omitempty"`
	Seat        *string    `json:"seat_label,omitempty"`
	Name        *string    `json:"attendee_name,omitempty"`
	TierID      string     `json:"tier_id"`
	Tier        string     `json:"tier"`
	OwnerEmail  string     `json:"owner_email"`
	CheckedInAt *time.Time `json:"checked_in_at,omitempty"`
	IssuedAt    time.Time  `json:"issued_at"`
}

type AttendeeQuery struct {
	Status, TierID, Q string
	Limit, Offset     int
}

func (a *AttendeeService) List(ctx context.Context, sc OrgScope, eventID string, f AttendeeQuery) ([]AttendeeView, int, error) {
	if _, err := a.s.Event.ownEvent(ctx, a.s.Q, sc, eventID); err != nil {
		return nil, 0, err
	}
	rows, err := a.s.Q.ListAttendees(ctx, sqlcgen.ListAttendeesParams{EventID: eventID, Status: strPtr(f.Status), TierID: strPtr(f.TierID), Q: strPtr(strings.TrimSpace(f.Q)), Lim: int32(f.Limit), Off: int32(f.Offset)})
	if err != nil {
		return nil, 0, err
	}
	n, err := a.s.Q.CountAttendees(ctx, sqlcgen.CountAttendeesParams{EventID: eventID, Status: strPtr(f.Status), TierID: strPtr(f.TierID), Q: strPtr(strings.TrimSpace(f.Q))})
	if err != nil {
		return nil, 0, err
	}
	out := make([]AttendeeView, 0, len(rows))
	for _, r := range rows {
		out = append(out, AttendeeView{TicketID: r.ID, Status: r.Status, Origin: r.Origin, EntryNumber: r.EntryNumber, Seat: r.SeatLabel, Name: r.AttendeeName,
			TierID: r.TierID, Tier: r.TierName, OwnerEmail: r.OwnerEmail, CheckedInAt: r.UsedAt, IssuedAt: r.IssuedAt})
	}
	return out, int(n), nil
}

// csvSafe menetralkan formula injection di Excel/Sheets (=,+,-,@ di awal sel).
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

// ExportCSV: seluruh peserta (hasil filter), tercatat di audit (AC1).
func (a *AttendeeService) ExportCSV(ctx context.Context, p *auth.Principal, sc OrgScope, eventID string, f AttendeeQuery) ([]byte, error) {
	if !sc.IsOwner() {
		return nil, apperr.Forbidden("hanya pemilik organizer yang boleh mengekspor data peserta")
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"ticket_id", "status", "origin", "entry_number", "seat", "attendee_name", "tier", "owner_email", "checked_in_at"})
	off, total := 0, 0
	for {
		rows, _, err := a.List(ctx, sc, eventID, AttendeeQuery{Status: f.Status, TierID: f.TierID, Q: f.Q, Limit: 500, Offset: off})
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			ci := ""
			if r.CheckedInAt != nil {
				ci = r.CheckedInAt.UTC().Format(time.RFC3339)
			}
			_ = w.Write([]string{r.TicketID, r.Status, r.Origin, csvSafe(deref(r.EntryNumber)), csvSafe(deref(r.Seat)), csvSafe(deref(r.Name)), csvSafe(r.Tier), csvSafe(r.OwnerEmail), ci})
		}
		total += len(rows)
		if len(rows) < 500 {
			break
		}
		off += 500
	}
	w.Flush()
	_ = a.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		return a.s.Audit.Log(ctx, q, p, "attendees.export", "event", eventID, nil, map[string]any{"rows": total})
	})
	return buf.Bytes(), w.Error()
}

// ---------- comp / undangan ----------

type CompInput struct {
	TierID string `json:"tier_id" binding:"required,len=26"`
	Name   string `json:"name" binding:"required,min=2,max=100"`
	Email  string `json:"email" binding:"required,email,max=254"`
	Reason string `json:"reason" binding:"required,min=3,max=200"`
}

type CompView struct {
	TicketID string  `json:"ticket_id"`
	Email    string  `json:"email"`
	Claim    *string `json:"claim_link,omitempty"` // bila penerima belum punya akun
}

func (a *AttendeeService) Comp(ctx context.Context, p *auth.Principal, sc OrgScope, eventID string, in CompInput) (*CompView, error) {
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	var tk sqlcgen.Ticket
	var ev sqlcgen.Event
	var token string
	direct := false
	err := a.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		if ev, err = a.s.Event.ownEvent(ctx, q, sc, eventID); err != nil {
			return err
		}
		if ev.Status != stPublished && ev.Status != stPaused && ev.Status != stApproved {
			return apperr.Conflict(apperr.CodeInvalidState, "event belum/tidak lagi dapat menerbitkan tiket undangan")
		}
		tier, err := q.GetTier(ctx, in.TierID)
		if err != nil || tier.EventID != eventID {
			return apperr.NotFound("tier")
		}
		if n, err := q.ReserveComp(ctx, sqlcgen.ReserveCompParams{ID: tier.ID, EventID: eventID}); err != nil {
			return err
		} else if n == 0 {
			return apperr.Conflict(apperr.CodeLimitExceeded, "kuota undangan tier ini habis")
		}
		owner := p.UserID
		if u, err := q.GetUserByEmail(ctx, in.Email); err == nil {
			owner, direct = u.ID, true
		} else if !platform.IsNoRows(err) {
			return err
		}
		enc, err := a.s.Box.Encrypt([]byte(auth.NewTOTPSecret()))
		if err != nil {
			return err
		}
		n, err := q.NextEntryNumber(ctx)
		if err != nil {
			return err
		}
		name := in.Name
		tk, err = q.InsertTicket(ctx, sqlcgen.InsertTicketParams{ID: id.New(), EventID: eventID, TierID: tier.ID, OwnerUserID: owner, EntryNumber: strPtr(fmt.Sprintf("%06d", n)),
			AttendeeName: &name, SecretEnc: enc, Origin: "comp"})
		if err != nil {
			return err
		}
		if _, err := q.InsertCompInvitation(ctx, sqlcgen.InsertCompInvitationParams{ID: id.New(), EventID: eventID, TierID: tier.ID, Name: in.Name, Email: in.Email, Reason: &in.Reason, IssuedBy: p.UserID, TicketID: &tk.ID}); err != nil {
			return err
		}
		if !direct {
			token = auth.RandomToken(32)
			e := in.Email
			if _, err := q.InsertClaimLink(ctx, sqlcgen.InsertClaimLinkParams{ID: id.New(), TicketID: tk.ID, TokenHash: auth.HashToken(token), TargetEmail: &e, CreatedBy: p.UserID, ExpiresAt: a.s.now().Add(30 * 24 * time.Hour)}); err != nil {
				return err
			}
		}
		return a.s.Audit.Log(ctx, q, p, "ticket.comp", "ticket", tk.ID, nil, map[string]any{"email": in.Email, "tier": tier.ID, "reason": in.Reason})
	})
	if err != nil {
		return nil, err
	}
	v := &CompView{TicketID: tk.ID, Email: in.Email}
	if direct {
		a.s.Notifier.Send(ctx, NotifyReq{To: in.Email, Template: "comp_ticket", Data: map[string]any{"name": in.Name, "event_title": ev.Title, "tickets_url": a.s.Cfg.PublicWebURL + "/tiket"}})
	} else {
		link := a.s.Cfg.PublicWebURL + "/claim/" + token
		v.Claim = &link
		a.s.Notifier.Send(ctx, NotifyReq{To: in.Email, Template: "ticket_claim", Data: map[string]any{"from_name": "Penyelenggara " + ev.Title, "event_title": ev.Title, "link": link,
			"expires": a.s.now().Add(30 * 24 * time.Hour).Format("02 Jan 2006")}})
	}
	return v, nil
}

type ImportRowError struct {
	Row   int    `json:"row"`
	Error string `json:"error"`
}

type ImportResult struct {
	Issued int              `json:"issued"`
	Errors []ImportRowError `json:"errors"`
}

// ImportComps: CSV header "name,email" + tier_id + reason untuk seluruh berkas; galat per baris dilaporkan, baris valid tetap diproses.
func (a *AttendeeService) ImportComps(ctx context.Context, p *auth.Principal, sc OrgScope, eventID, tierID, reason string, data []byte) (*ImportResult, error) {
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		return nil, apperr.Validation("CSV tidak dapat dibaca: " + err.Error())
	}
	if len(rows) < 2 {
		return nil, apperr.Validation("CSV kosong; baris pertama harus header name,email")
	}
	hdr := map[string]int{}
	for i, h := range rows[0] {
		hdr[strings.ToLower(strings.TrimSpace(h))] = i
	}
	ni, ok1 := hdr["name"]
	ei, ok2 := hdr["email"]
	if !ok1 || !ok2 {
		return nil, apperr.Validation("header wajib: name,email")
	}
	if len(rows)-1 > maxCompImportRows {
		return nil, apperr.Validation(fmt.Sprintf("maksimal %d baris per impor", maxCompImportRows))
	}
	res := &ImportResult{Errors: []ImportRowError{}}
	for i, row := range rows[1:] {
		line := i + 2
		if len(row) <= ni || len(row) <= ei {
			res.Errors = append(res.Errors, ImportRowError{line, "kolom kurang"})
			continue
		}
		in := CompInput{TierID: tierID, Name: strings.TrimSpace(row[ni]), Email: strings.TrimSpace(row[ei]), Reason: reason}
		if len([]rune(in.Name)) < 2 || !strings.Contains(in.Email, "@") {
			res.Errors = append(res.Errors, ImportRowError{line, "nama/email tidak valid"})
			continue
		}
		if _, err := a.Comp(ctx, p, sc, eventID, in); err != nil {
			msg := err.Error()
			if ae, ok := err.(*apperr.Error); ok {
				msg = ae.Message
			}
			res.Errors = append(res.Errors, ImportRowError{line, msg})
			continue
		}
		res.Issued++
	}
	return res, nil
}

func (a *AttendeeService) ListComps(ctx context.Context, sc OrgScope, eventID string, limit, offset int) ([]sqlcgen.CompInvitation, error) {
	if _, err := a.s.Event.ownEvent(ctx, a.s.Q, sc, eventID); err != nil {
		return nil, err
	}
	return a.s.Q.ListComps(ctx, sqlcgen.ListCompsParams{EventID: eventID, Limit: int32(limit), Offset: int32(offset)})
}

// ---------- reissue / void / rename ----------

func (a *AttendeeService) mutate(ctx context.Context, p *auth.Principal, sc OrgScope, eventID, ticketID, action string, fn func(q *sqlcgen.Queries, tk sqlcgen.Ticket, ev sqlcgen.Event) error) error {
	return a.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		ev, err := a.s.Event.ownEvent(ctx, q, sc, eventID)
		if err != nil {
			return err
		}
		tk, err := q.GetTicketForUpdate(ctx, ticketID)
		if err != nil || tk.EventID != eventID {
			return apperr.NotFound("tiket")
		}
		if tk.Status != "valid" {
			return apperr.Conflict(apperr.CodeTicketInvalid, "hanya tiket aktif yang dapat diubah")
		}
		if err := fn(q, tk, ev); err != nil {
			return err
		}
		return a.s.Audit.Log(ctx, q, p, "ticket."+action, "ticket", ticketID, nil, nil)
	})
}

// Reissue merotasi secret (QR lama mati) dan mengirim ulang tiket ke pemilik.
func (a *AttendeeService) Reissue(ctx context.Context, p *auth.Principal, sc OrgScope, eventID, ticketID string) error {
	var owner string
	var ev sqlcgen.Event
	err := a.mutate(ctx, p, sc, eventID, ticketID, "reissue", func(q *sqlcgen.Queries, tk sqlcgen.Ticket, e sqlcgen.Event) error {
		enc, err := a.s.Box.Encrypt([]byte(auth.NewTOTPSecret()))
		if err != nil {
			return err
		}
		owner, ev = tk.OwnerUserID, e
		return q.RotateTicketSecret(ctx, sqlcgen.RotateTicketSecretParams{ID: tk.ID, SecretEnc: enc})
	})
	if err == nil {
		a.s.Notifier.Send(ctx, NotifyReq{UserID: owner, Template: "ticket_received", Data: map[string]any{"name": "", "event_title": ev.Title, "tickets_url": a.s.Cfg.PublicWebURL + "/tiket"}})
	}
	return err
}

// Void membatalkan tiket tanpa refund otomatis (keputusan organizer; refund diurus terpisah). Hanya owner organizer.
func (a *AttendeeService) Void(ctx context.Context, p *auth.Principal, sc OrgScope, eventID, ticketID string) error {
	if !sc.IsOwner() {
		return apperr.Forbidden("hanya pemilik organizer")
	}
	return a.mutate(ctx, p, sc, eventID, ticketID, "void", func(q *sqlcgen.Queries, tk sqlcgen.Ticket, _ sqlcgen.Event) error {
		_, err := q.SetTicketStatusForEvent(ctx, sqlcgen.SetTicketStatusForEventParams{ID: tk.ID, EventID: eventID, Status: "void"})
		return err
	})
}

// Rename mengganti nama peserta (sebelum acara dimulai) dan merotasi secret.
func (a *AttendeeService) Rename(ctx context.Context, p *auth.Principal, sc OrgScope, eventID, ticketID, name string) error {
	name = strings.TrimSpace(name)
	if len([]rune(name)) < 2 || len([]rune(name)) > 100 {
		return apperr.Validation("nama 2–100 karakter").With("field", "name")
	}
	return a.mutate(ctx, p, sc, eventID, ticketID, "rename", func(q *sqlcgen.Queries, tk sqlcgen.Ticket, ev sqlcgen.Event) error {
		if !a.s.now().Before(ev.StartsAt) {
			return apperr.Conflict(apperr.CodeInvalidState, "nama tidak dapat diganti setelah acara dimulai")
		}
		enc, err := a.s.Box.Encrypt([]byte(auth.NewTOTPSecret()))
		if err != nil {
			return err
		}
		if err := q.RotateTicketSecret(ctx, sqlcgen.RotateTicketSecretParams{ID: tk.ID, SecretEnc: enc}); err != nil {
			return err
		}
		return q.RenameTicketAttendee(ctx, sqlcgen.RenameTicketAttendeeParams{ID: tk.ID, AttendeeName: &name})
	})
}

// ---------- siaran pesan ----------

type BroadcastInput struct {
	TierID  string `json:"tier_id" binding:"omitempty,len=26"`
	Subject string `json:"subject" binding:"required,min=3,max=150"`
	Body    string `json:"body" binding:"required,min=10,max=5000"`
}

func (a *AttendeeService) Broadcast(ctx context.Context, p *auth.Principal, sc OrgScope, eventID string, in BroadcastInput) (*sqlcgen.Broadcast, error) {
	var out sqlcgen.Broadcast
	err := a.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := a.s.Event.ownEvent(ctx, q, sc, eventID); err != nil {
			return err
		}
		if n, err := q.CountBroadcastsToday(ctx, sqlcgen.CountBroadcastsTodayParams{EventID: eventID, Since: a.s.now().Add(-24 * time.Hour)}); err != nil {
			return err
		} else if int(n) >= maxBroadcastsPerDay {
			return apperr.TooMany("batas siaran harian tercapai").With("limit", maxBroadcastsPerDay)
		}
		ids, err := q.BroadcastRecipients(ctx, sqlcgen.BroadcastRecipientsParams{EventID: eventID, TierID: strPtr(in.TierID)})
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return apperr.Unprocessable(apperr.CodeValidation, "tidak ada penerima")
		}
		out, err = q.InsertBroadcast(ctx, sqlcgen.InsertBroadcastParams{ID: id.New(), EventID: eventID, TierID: strPtr(in.TierID), Subject: in.Subject, Body: in.Body, Recipients: len(ids), CreatedBy: p.UserID})
		if err != nil {
			return err
		}
		return a.s.Audit.Log(ctx, q, p, "broadcast.create", "event", eventID, nil, map[string]any{"recipients": len(ids), "subject": in.Subject})
	})
	if err != nil {
		return nil, err
	}
	if err := a.s.Jobs.Enqueue(ctx, jobBroadcast, map[string]string{"id": out.ID}, jobs.MaxRetry(5), jobs.Unique("bc:"+out.ID, 24*time.Hour)); err != nil {
		a.s.Log.Error("enqueue siaran gagal", "id", out.ID, "error", err)
	}
	return &out, nil
}

func (a *AttendeeService) handleBroadcast(ctx context.Context, payload []byte) error {
	p, err := jobs.Decode[map[string]string](payload)
	if err != nil {
		return nil
	}
	b, err := a.s.Q.GetBroadcast(ctx, p["id"])
	if err != nil || b.Status == "sent" {
		return nil
	}
	ev, err := a.s.Q.GetEvent(ctx, b.EventID)
	if err != nil {
		return nil
	}
	ids, err := a.s.Q.BroadcastRecipients(ctx, sqlcgen.BroadcastRecipientsParams{EventID: b.EventID, TierID: b.TierID})
	if err != nil {
		return err
	}
	_ = a.s.Q.SetBroadcastProgress(ctx, sqlcgen.SetBroadcastProgressParams{ID: b.ID, Status: "sending", SentCount: b.SentCount})
	for _, uid := range ids {
		// kategori 'broadcasts' = dapat dimatikan pengguna (preferensi dihormati oleh Notifier)
		a.s.Notifier.Send(ctx, NotifyReq{UserID: uid, Template: "broadcast", Category: "broadcasts", Data: map[string]any{"subject": b.Subject, "body": b.Body, "event_title": ev.Title}})
	}
	return a.s.Q.SetBroadcastProgress(ctx, sqlcgen.SetBroadcastProgressParams{ID: b.ID, Status: "sent", SentCount: len(ids)})
}

func (a *AttendeeService) ListBroadcasts(ctx context.Context, sc OrgScope, eventID string) ([]sqlcgen.Broadcast, error) {
	if _, err := a.s.Event.ownEvent(ctx, a.s.Q, sc, eventID); err != nil {
		return nil, err
	}
	return a.s.Q.ListBroadcasts(ctx, eventID)
}
