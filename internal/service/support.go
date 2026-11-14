package service

import (
	"context"
	"strconv"
	"strings"
	"time"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/repository/sqlcgen"
)

// SupportService = tiket bantuan bernomor, antrean agen, SLA & eskalasi (docs/20 M30).
type SupportService struct{ s *Services }

func newSupportService(s *Services) *SupportService { return &SupportService{s: s} }

const (
	supportSLA        = "1 hari kerja"
	escalatePayment   = 24 * time.Hour
	autoClosePending  = 7 * 24 * time.Hour
	autoCloseResolved = 14 * 24 * time.Hour
	reopenWindow      = 14 * 24 * time.Hour
	hZeroWindow       = 24 * time.Hour
)

// Balasan siap pakai agen (AC4). Variabel {{nama}} diisi di sisi agen.
var SupportTemplates = map[string]string{
	"refund_info":  "Halo {{nama}}, kebijakan refund mengikuti ketentuan event. Kami sedang memeriksa pesananmu dan akan mengabari dalam 1 hari kerja.",
	"payment_wait": "Halo {{nama}}, pembayaranmu masih kami verifikasi dengan penyedia pembayaran. Mohon tunggu hingga 1x24 jam; bila sudah lunas, tiket terbit otomatis.",
	"need_info":    "Halo {{nama}}, boleh kirim detail tambahan (nomor pesanan dan tangkapan layar) agar kami bisa menindaklanjuti?",
	"resolved":     "Halo {{nama}}, masalah ini sudah kami tangani. Kalau masih ada kendala, balas pesan ini dan kami buka kembali.",
}

func numOf(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

type SupportTicketView struct {
	ID              string     `json:"id"`
	Number          int64      `json:"number"`
	Category        string     `json:"category"`
	Subject         string     `json:"subject"`
	Status          string     `json:"status"`
	Priority        string     `json:"priority"`
	OrderID         *string    `json:"order_id,omitempty"`
	EventID         *string    `json:"event_id,omitempty"`
	TicketID        *string    `json:"ticket_id,omitempty"`
	AssigneeID      *string    `json:"assignee_id,omitempty"`
	FirstResponseAt *time.Time `json:"first_response_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func stView(t sqlcgen.SupportTicket) SupportTicketView {
	return SupportTicketView{ID: t.ID, Number: numOf(t.Number), Category: t.Category, Subject: t.Subject, Status: t.Status, Priority: t.Priority, OrderID: t.OrderID, EventID: t.EventID,
		TicketID: t.TicketID, AssigneeID: t.AssigneeID, FirstResponseAt: t.FirstResponseAt, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt}
}

type SupportMessageView struct {
	ID        string    `json:"id"`
	Author    string    `json:"author"`
	FromStaff bool      `json:"from_staff"`
	Internal  bool      `json:"internal,omitempty"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

type SupportThread struct {
	Ticket   SupportTicketView    `json:"ticket"`
	Messages []SupportMessageView `json:"messages"`
	Order    any                  `json:"order,omitempty"` // ringkasan terbatas, hanya untuk agen
}

type SupportInput struct {
	Category string `json:"category" binding:"required,oneof=payment ticket refund account event other"`
	Subject  string `json:"subject" binding:"required,min=5,max=150"`
	Body     string `json:"body" binding:"required,min=10,max=5000"`
	OrderID  string `json:"order_id" binding:"omitempty,len=26"`
	EventID  string `json:"event_id" binding:"omitempty,len=26"`
	TicketID string `json:"ticket_id" binding:"omitempty,len=26"`
}

func (ss *SupportService) Create(ctx context.Context, userID string, in SupportInput) (*SupportTicketView, error) {
	if ok, ra, err := ss.s.Limiter.Allow(ctx, "support:"+userID, 10, time.Hour); err == nil && !ok {
		return nil, apperr.TooMany("terlalu banyak permintaan bantuan").With("retry_after", int(ra.Seconds())+1)
	}
	priority := "normal"
	var eventForDay string
	var out sqlcgen.SupportTicket
	err := ss.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		// konteks wajib milik pengguna sendiri (tidak membocorkan data orang lain)
		var orderID, eventID, ticketID *string
		if in.OrderID != "" {
			o, err := q.GetOrder(ctx, in.OrderID)
			if err != nil || o.UserID != userID {
				return apperr.NotFound("pesanan")
			}
			orderID, eventForDay = &o.ID, o.EventID
		}
		if in.TicketID != "" {
			t, err := q.GetTicket(ctx, in.TicketID)
			if err != nil || t.OwnerUserID != userID {
				return apperr.NotFound("tiket")
			}
			ticketID, eventForDay = &t.ID, t.EventID
		}
		if in.EventID != "" {
			eventID, eventForDay = &in.EventID, in.EventID
		} else if eventForDay != "" {
			eventID = &eventForDay
		}
		if eventForDay != "" {
			if ev, err := q.GetEvent(ctx, eventForDay); err == nil {
				d := ev.StartsAt.Sub(ss.s.now())
				if d < hZeroWindow && d > -hZeroWindow { // hari-H: prioritas tinggi (AC3/AC6)
					priority = "high"
				}
			}
		}
		t, err := q.InsertSupportTicket(ctx, sqlcgen.InsertSupportTicketParams{ID: id.New(), UserID: userID, Category: in.Category, Subject: in.Subject, OrderID: orderID, EventID: eventID, TicketID: ticketID, Priority: priority})
		if err != nil {
			return err
		}
		out = t
		_, err = q.InsertSupportMessage(ctx, sqlcgen.InsertSupportMessageParams{ID: id.New(), TicketID: t.ID, AuthorID: userID, Internal: false, Body: in.Body})
		return err
	})
	if err != nil {
		return nil, err
	}
	sla := supportSLA
	if priority == "high" {
		sla = "secepatnya (prioritas tinggi)"
	}
	ss.s.Notifier.Send(ctx, NotifyReq{UserID: userID, Template: "support_received", Data: map[string]any{"name": "", "number": strconv.FormatInt(numOf(out.Number), 10), "subject": out.Subject, "sla": sla}})
	v := stView(out)
	return &v, nil
}

func (ss *SupportService) MyTickets(ctx context.Context, userID string, limit, offset int) ([]SupportTicketView, error) {
	rows, err := ss.s.Q.ListUserSupportTickets(ctx, sqlcgen.ListUserSupportTicketsParams{UserID: userID, Limit: int32(limit), Offset: int32(offset)})
	if err != nil {
		return nil, err
	}
	out := make([]SupportTicketView, 0, len(rows))
	for _, r := range rows {
		out = append(out, stView(r))
	}
	return out, nil
}

func (ss *SupportService) thread(ctx context.Context, t sqlcgen.SupportTicket, staff bool) (*SupportThread, error) {
	msgs, err := ss.s.Q.ListSupportMessages(ctx, sqlcgen.ListSupportMessagesParams{TicketID: t.ID, IncludeInternal: staff})
	if err != nil {
		return nil, err
	}
	th := &SupportThread{Ticket: stView(t), Messages: make([]SupportMessageView, 0, len(msgs))}
	for _, m := range msgs {
		fromStaff := m.AuthorRole == "support" || m.AuthorRole == "admin"
		name := m.AuthorName
		if !staff && fromStaff {
			name = "Tim TixGo"
		}
		th.Messages = append(th.Messages, SupportMessageView{ID: m.ID, Author: name, FromStaff: fromStaff, Internal: m.Internal, Body: m.Body, CreatedAt: m.CreatedAt})
	}
	if staff && t.OrderID != nil {
		if o, err := ss.s.Q.SupportOrderSummary(ctx, *t.OrderID); err == nil {
			th.Order = o // tanpa PII pembeli: hanya status, nominal, event, jumlah tiket, status pembayaran
		}
	}
	return th, nil
}

func (ss *SupportService) MyThread(ctx context.Context, userID, ticketID string) (*SupportThread, error) {
	t, err := ss.s.Q.GetSupportTicket(ctx, ticketID)
	if err != nil || t.UserID != userID {
		return nil, apperr.NotFound("tiket bantuan")
	}
	return ss.thread(ctx, t, false)
}

// Reply oleh pelanggan: membuka kembali (≤14 hari sejak resolved) atau melanjutkan.
func (ss *SupportService) Reply(ctx context.Context, userID, ticketID, body string) error {
	if len([]rune(strings.TrimSpace(body))) < 2 {
		return apperr.Validation("pesan kosong").With("field", "body")
	}
	return ss.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		t, err := q.GetSupportTicketForUpdate(ctx, ticketID)
		if err != nil || t.UserID != userID {
			return apperr.NotFound("tiket bantuan")
		}
		switch t.Status {
		case "closed":
			return apperr.Conflict(apperr.CodeInvalidState, "tiket sudah ditutup; buat permintaan baru")
		case "resolved":
			if ss.s.now().Sub(t.UpdatedAt) > reopenWindow {
				return apperr.Conflict(apperr.CodeInvalidState, "batas membuka kembali terlewati; buat permintaan baru")
			}
		}
		if _, err := q.InsertSupportMessage(ctx, sqlcgen.InsertSupportMessageParams{ID: id.New(), TicketID: ticketID, AuthorID: userID, Body: body}); err != nil {
			return err
		}
		return q.SetSupportStatus(ctx, sqlcgen.SetSupportStatusParams{ID: ticketID, Status: "open"})
	})
}

func (ss *SupportService) CloseMine(ctx context.Context, userID, ticketID string) error {
	t, err := ss.s.Q.GetSupportTicket(ctx, ticketID)
	if err != nil || t.UserID != userID {
		return apperr.NotFound("tiket bantuan")
	}
	return ss.s.Q.SetSupportStatus(ctx, sqlcgen.SetSupportStatusParams{ID: ticketID, Status: "closed"})
}

// ---------- agen ----------

type AgentFilter struct {
	Status, Category, Priority, Assignee string
	Limit, Offset                        int
}

type AgentRow struct {
	SupportTicketView
	UserEmail string `json:"user_email"`
	Overdue   bool   `json:"sla_overdue"`
}

func (ss *SupportService) Queue(ctx context.Context, f AgentFilter) ([]AgentRow, int, error) {
	rows, err := ss.s.Q.AgentSupportQueue(ctx, sqlcgen.AgentSupportQueueParams{Status: strPtr(f.Status), Category: strPtr(f.Category), Priority: strPtr(f.Priority), Assignee: strPtr(f.Assignee), Lim: int32(f.Limit), Off: int32(f.Offset)})
	if err != nil {
		return nil, 0, err
	}
	n, err := ss.s.Q.CountAgentSupportQueue(ctx, sqlcgen.CountAgentSupportQueueParams{Status: strPtr(f.Status), Category: strPtr(f.Category), Priority: strPtr(f.Priority), Assignee: strPtr(f.Assignee)})
	if err != nil {
		return nil, 0, err
	}
	out := make([]AgentRow, 0, len(rows))
	for _, r := range rows {
		t := sqlcgen.SupportTicket{ID: r.ID, Number: r.Number, UserID: r.UserID, Category: r.Category, Subject: r.Subject, OrderID: r.OrderID, EventID: r.EventID, TicketID: r.TicketID,
			Status: r.Status, Priority: r.Priority, AssigneeID: r.AssigneeID, FirstResponseAt: r.FirstResponseAt, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
		od := t.FirstResponseAt == nil && t.Status == "open" && ss.s.now().Sub(t.CreatedAt) > 24*time.Hour
		out = append(out, AgentRow{SupportTicketView: stView(t), UserEmail: r.UserEmail, Overdue: od})
	}
	return out, int(n), nil
}

func (ss *SupportService) AgentThread(ctx context.Context, p *auth.Principal, ticketID string) (*SupportThread, error) {
	t, err := ss.s.Q.GetSupportTicket(ctx, ticketID)
	if err != nil {
		return nil, apperr.NotFound("tiket bantuan")
	}
	_ = ss.s.InTx(ctx, func(q *sqlcgen.Queries) error { // akses data pribadi oleh staf teraudit (M29 AC6)
		return ss.s.Audit.Log(ctx, q, p, "support.view", "support_ticket", ticketID, nil, nil)
	})
	return ss.thread(ctx, t, true)
}

// AgentReply: internal=true → catatan internal (tidak dikirim). Balasan publik mengubah status → pending_customer + email.
func (ss *SupportService) AgentReply(ctx context.Context, p *auth.Principal, ticketID, body string, internal bool) error {
	if len([]rune(strings.TrimSpace(body))) < 2 {
		return apperr.Validation("pesan kosong").With("field", "body")
	}
	var t sqlcgen.SupportTicket
	err := ss.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		if t, err = q.GetSupportTicketForUpdate(ctx, ticketID); err != nil {
			return apperr.NotFound("tiket bantuan")
		}
		if t.Status == "closed" {
			return apperr.Conflict(apperr.CodeInvalidState, "tiket sudah ditutup")
		}
		if _, err := q.InsertSupportMessage(ctx, sqlcgen.InsertSupportMessageParams{ID: id.New(), TicketID: ticketID, AuthorID: p.UserID, Internal: internal, Body: body}); err != nil {
			return err
		}
		if internal {
			return nil
		}
		if t.AssigneeID == nil {
			if err := q.SetSupportAssignee(ctx, sqlcgen.SetSupportAssigneeParams{ID: ticketID, AssigneeID: &p.UserID}); err != nil {
				return err
			}
		}
		return q.SetSupportStatus(ctx, sqlcgen.SetSupportStatusParams{ID: ticketID, Status: "pending_customer", MarkResponse: true})
	})
	if err == nil && !internal {
		ss.s.Notifier.Send(ctx, NotifyReq{UserID: t.UserID, Template: "support_reply", Data: map[string]any{"name": "", "number": strconv.FormatInt(numOf(t.Number), 10), "subject": t.Subject, "body": body,
			"link": ss.s.Cfg.PublicWebURL + "/bantuan/" + t.ID}})
	}
	return err
}

func (ss *SupportService) Assign(ctx context.Context, p *auth.Principal, ticketID, assignee string) error {
	if assignee == "" {
		assignee = p.UserID
	}
	return ss.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := q.GetSupportTicket(ctx, ticketID); err != nil {
			return apperr.NotFound("tiket bantuan")
		}
		if err := q.SetSupportAssignee(ctx, sqlcgen.SetSupportAssigneeParams{ID: ticketID, AssigneeID: &assignee}); err != nil {
			return err
		}
		return ss.s.Audit.Log(ctx, q, p, "support.assign", "support_ticket", ticketID, nil, map[string]any{"assignee": assignee})
	})
}

func (ss *SupportService) Resolve(ctx context.Context, p *auth.Principal, ticketID string) error {
	return ss.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		t, err := q.GetSupportTicketForUpdate(ctx, ticketID)
		if err != nil {
			return apperr.NotFound("tiket bantuan")
		}
		if t.Status == "closed" {
			return apperr.Conflict(apperr.CodeInvalidState, "tiket sudah ditutup")
		}
		if err := q.SetSupportStatus(ctx, sqlcgen.SetSupportStatusParams{ID: ticketID, Status: "resolved", MarkResponse: true}); err != nil {
			return err
		}
		return ss.s.Audit.Log(ctx, q, p, "support.resolve", "support_ticket", ticketID, nil, nil)
	})
}

// Sweep (scheduler): eskalasi pembayaran >24 jam, tutup otomatis pending_customer 7 hari & resolved 14 hari.
func (ss *SupportService) Sweep(ctx context.Context) (int, error) {
	now := ss.s.now()
	n1, err := ss.s.Q.EscalateStalePaymentTickets(ctx, now.Add(-escalatePayment))
	if err != nil {
		return 0, err
	}
	closed, err := ss.s.Q.CloseStalePendingCustomer(ctx, now.Add(-autoClosePending))
	if err != nil {
		return 0, err
	}
	n3, err := ss.s.Q.CloseOldResolved(ctx, now.Add(-autoCloseResolved))
	if err != nil {
		return 0, err
	}
	return int(n1) + len(closed) + int(n3), nil
}
