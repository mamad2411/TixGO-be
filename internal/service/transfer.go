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

// TransferService = kirim tiket lewat tautan klaim sekali pakai (docs/06 M9, 20 M26).
// Rotasi secret saat klaim → QR lama mati. Rantai kepemilikan tercatat di ticket_transfers.
type TransferService struct{ s *Services }

func newTransferService(s *Services) *TransferService { return &TransferService{s: s} }

const claimTTL = 72 * time.Hour

type ClaimLinkView struct {
	ID          string    `json:"id"`
	TicketID    string    `json:"ticket_id"`
	TargetEmail *string   `json:"target_email,omitempty"`
	ExpiresAt   time.Time `json:"expires_at"`
	Status      string    `json:"status"`
	Link        string    `json:"link,omitempty"` // hanya saat dibuat
}

func (t *TransferService) transferable(ctx context.Context, q *sqlcgen.Queries, tk sqlcgen.Ticket) (sqlcgen.Event, error) {
	ev, err := q.GetEvent(ctx, tk.EventID)
	if err != nil {
		return ev, err
	}
	if tk.Status != "valid" {
		return ev, apperr.Conflict(apperr.CodeTicketInvalid, "hanya tiket aktif yang bisa dikirim")
	}
	if !ev.TransferEnabled && tk.Origin != "comp" {
		return ev, apperr.Unprocessable(apperr.CodeNotAllowed, "penyelenggara tidak mengizinkan pengiriman tiket untuk event ini")
	}
	end := ev.StartsAt.Add(12 * time.Hour)
	if ev.EndsAt != nil {
		end = *ev.EndsAt
	}
	if !t.s.now().Before(end) || ev.Status == stCancelled {
		return ev, apperr.Conflict(apperr.CodeInvalidState, "event sudah selesai atau dibatalkan")
	}
	return ev, nil
}

// CreateLink membuat tautan klaim. email opsional: bila diisi, hanya akun terverifikasi dengan email itu yang bisa klaim (+ email dikirim).
func (t *TransferService) CreateLink(ctx context.Context, userID, ticketID, email string) (*ClaimLinkView, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email != "" && !strings.Contains(email, "@") {
		return nil, apperr.Validation("email tujuan tidak valid")
	}
	var link sqlcgen.ClaimLink
	var ev sqlcgen.Event
	var token string
	err := t.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		tk, err := q.GetTicketForUpdate(ctx, ticketID)
		if err != nil || tk.OwnerUserID != userID {
			return apperr.NotFound("tiket")
		}
		if ev, err = t.transferable(ctx, q, tk); err != nil {
			return err
		}
		if n, err := q.CountTicketTransfers(ctx, tk.ID); err == nil && int(n) >= t.s.Cfg.TicketMaxTransfers {
			return apperr.Unprocessable(apperr.CodeNotAllowed, "tiket ini sudah mencapai batas jumlah pengiriman")
		}
		if lst, err := q.ActiveListingForTicket(ctx, tk.ID); err == nil && lst.ID != "" {
			return apperr.Conflict(apperr.CodeConflict, "tiket sedang dijual ulang; batalkan listing dulu")
		}
		// satu tautan aktif per tiket: yang lama dicabut
		if _, err := q.RevokeClaimLinksForTicket(ctx, tk.ID); err != nil {
			return err
		}
		token = auth.RandomToken(32)
		var te *string
		if email != "" {
			te = &email
		}
		link, err = q.InsertClaimLink(ctx, sqlcgen.InsertClaimLinkParams{ID: id.New(), TicketID: tk.ID, TokenHash: auth.HashToken(token), TargetEmail: te,
			CreatedBy: userID, ExpiresAt: t.s.now().Add(claimTTL)})
		if err != nil {
			return err
		}
		return t.s.Audit.Log(ctx, q, &auth.Principal{UserID: userID, Role: "customer"}, "ticket.claim_link", "ticket", tk.ID, nil, map[string]any{"targeted": email != ""})
	})
	if err != nil {
		return nil, err
	}
	url := t.s.Cfg.PublicWebURL + "/claim/" + token
	if email != "" {
		from, _ := t.s.Q.GetUserByID(ctx, userID)
		t.s.Notifier.Send(ctx, NotifyReq{To: email, Template: "ticket_claim", Data: map[string]any{
			"from_name": from.Name, "event_title": ev.Title, "link": url, "expires": link.ExpiresAt.Format("02 Jan 2006 15:04")}})
	}
	return &ClaimLinkView{ID: link.ID, TicketID: link.TicketID, TargetEmail: link.TargetEmail, ExpiresAt: link.ExpiresAt, Status: link.Status, Link: url}, nil
}

// Revoke mencabut tautan aktif milik tiket (hanya pemilik).
func (t *TransferService) Revoke(ctx context.Context, userID, ticketID string) error {
	tk, err := t.s.Ticket.ownTicket(ctx, userID, ticketID)
	if err != nil {
		return err
	}
	_, err = t.s.Q.RevokeClaimLinksForTicket(ctx, tk.ID)
	return err
}

type ClaimPreview struct {
	EventTitle string    `json:"event_title"`
	StartsAt   time.Time `json:"starts_at"`
	SeatLabel  *string   `json:"seat_label,omitempty"`
	FromName   string    `json:"from_name"`
	ExpiresAt  time.Time `json:"expires_at"`
	Targeted   bool      `json:"targeted"`
}

func (t *TransferService) linkState(l sqlcgen.ClaimLink) error {
	switch {
	case l.Status == "claimed":
		return apperr.Conflict(apperr.CodeInvalidState, "tautan sudah dipakai")
	case l.Status == "revoked":
		return apperr.Conflict(apperr.CodeInvalidState, "tautan sudah dicabut pengirim")
	case l.Status != "active" || !l.ExpiresAt.After(t.s.now()):
		return apperr.Conflict(apperr.CodeInvalidState, "tautan sudah kedaluwarsa")
	}
	return nil
}

// Preview menampilkan isi tautan sebelum klaim (login diperlukan agar tidak jadi alat enumerasi).
func (t *TransferService) Preview(ctx context.Context, token string) (*ClaimPreview, error) {
	l, err := t.s.Q.GetClaimLinkByHash(ctx, auth.HashToken(token))
	if err != nil {
		return nil, apperr.NotFound("tautan klaim")
	}
	if err := t.linkState(l); err != nil {
		return nil, err
	}
	tk, err := t.s.Q.GetTicket(ctx, l.TicketID)
	if err != nil {
		return nil, apperr.NotFound("tautan klaim")
	}
	ev, err := t.s.Q.GetEvent(ctx, tk.EventID)
	if err != nil {
		return nil, err
	}
	from, _ := t.s.Q.GetUserByID(ctx, l.CreatedBy)
	return &ClaimPreview{EventTitle: ev.Title, StartsAt: ev.StartsAt, SeatLabel: tk.SeatLabel, FromName: from.Name, ExpiresAt: l.ExpiresAt, Targeted: l.TargetEmail != nil}, nil
}

// Claim memindahkan kepemilikan secara atomik dan merotasi secret.
func (t *TransferService) Claim(ctx context.Context, userID, token string) (*TicketView, error) {
	user, err := t.s.Q.GetUserByID(ctx, userID)
	if err != nil {
		return nil, apperr.NotFound("akun")
	}
	if user.EmailVerifiedAt == nil {
		return nil, apperr.ForbiddenCode(apperr.CodeEmailNotVerified, "verifikasi email dulu sebelum menerima tiket")
	}
	if t.s.Fraud.IsBlacklisted(ctx, "user", userID) {
		return nil, apperr.Forbidden("akun tidak dapat menerima tiket")
	}
	var tk sqlcgen.Ticket
	var ev sqlcgen.Event
	var fromID string
	err = t.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		l, err := q.GetClaimLinkByHashForUpdate(ctx, auth.HashToken(token))
		if err != nil {
			if platform.IsNoRows(err) {
				return apperr.NotFound("tautan klaim")
			}
			return err
		}
		if err := t.linkState(l); err != nil {
			return err
		}
		if l.CreatedBy == userID {
			return apperr.Conflict(apperr.CodeConflict, "ini tiketmu sendiri")
		}
		if l.TargetEmail != nil && !strings.EqualFold(*l.TargetEmail, user.Email) {
			return apperr.Forbidden("tautan ini ditujukan untuk email lain")
		}
		tk, err = q.GetTicketForUpdate(ctx, l.TicketID)
		if err != nil {
			return err
		}
		if ev, err = t.transferable(ctx, q, tk); err != nil {
			return err
		}
		if tk.OwnerUserID != l.CreatedBy {
			return apperr.Conflict(apperr.CodeInvalidState, "tiket sudah berpindah tangan")
		}
		fromID = tk.OwnerUserID
		enc, err := t.s.Box.Encrypt([]byte(auth.NewTOTPSecret()))
		if err != nil {
			return err
		}
		name := user.Name
		n, err := q.TransferTicketOwner(ctx, sqlcgen.TransferTicketOwnerParams{ID: tk.ID, OldOwner: fromID, NewOwner: userID, SecretEnc: enc, AttendeeName: &name})
		if err != nil {
			return err
		}
		if n == 0 {
			return apperr.Conflict(apperr.CodeInvalidState, "tiket tidak lagi bisa dikirim")
		}
		if err := q.SetClaimLinkClaimed(ctx, sqlcgen.SetClaimLinkClaimedParams{ID: l.ID, ClaimedBy: &userID, ClaimedAt: ptr(t.s.now())}); err != nil {
			return err
		}
		if err := q.InsertTicketTransfer(ctx, sqlcgen.InsertTicketTransferParams{ID: id.New(), TicketID: tk.ID, FromUserID: fromID, ToUserID: userID, Via: "claim", RefID: &l.ID}); err != nil {
			return err
		}
		if err := t.s.Audit.Log(ctx, q, &auth.Principal{UserID: userID, Role: "customer"}, "ticket.claim", "ticket", tk.ID, map[string]any{"from": fromID}, map[string]any{"to": userID}); err != nil {
			return err
		}
		return emit(ctx, q, "user."+fromID, "ticket.transferred", map[string]any{"ticket_id": tk.ID})
	})
	if err != nil {
		return nil, err
	}
	t.s.Notifier.Send(ctx, NotifyReq{UserID: userID, Template: "ticket_received", Data: map[string]any{
		"name": user.Name, "event_title": ev.Title, "tickets_url": t.s.Cfg.PublicWebURL + "/tiket"}})
	return t.s.Ticket.Get(ctx, userID, tk.ID)
}

type TransferHop struct {
	From string    `json:"from_user_id"`
	To   string    `json:"to_user_id"`
	Via  string    `json:"via"`
	At   time.Time `json:"at"`
}

// Chain = rantai kepemilikan (organizer/admin/support; tidak diekspos ke pembeli).
func (t *TransferService) Chain(ctx context.Context, ticketID string) ([]TransferHop, error) {
	rows, err := t.s.Q.ListTicketTransfers(ctx, ticketID)
	if err != nil {
		return nil, err
	}
	out := make([]TransferHop, 0, len(rows))
	for _, r := range rows {
		out = append(out, TransferHop{From: r.FromUserID, To: r.ToUserID, Via: r.Via, At: r.CreatedAt})
	}
	return out, nil
}

type TicketPolicy struct {
	TransferEnabled bool `json:"transfer_enabled"`
	ResaleCapPct    int  `json:"resale_cap_pct" binding:"min=0,max=200"` // 0 = resale dimatikan
}

// SetPolicy: hanya owner organizer; berlaku untuk pengiriman/resale berikutnya.
func (t *TransferService) SetPolicy(ctx context.Context, p *auth.Principal, sc OrgScope, eventID string, in TicketPolicy) (*TicketPolicy, error) {
	if !sc.IsOwner() {
		return nil, apperr.Forbidden("hanya owner yang boleh mengubah kebijakan tiket")
	}
	err := t.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		ev, err := t.s.Event.ownEvent(ctx, q, sc, eventID)
		if err != nil {
			return err
		}
		if err := q.SetEventTicketPolicy(ctx, sqlcgen.SetEventTicketPolicyParams{ID: ev.ID, TransferEnabled: in.TransferEnabled, ResaleCapPct: in.ResaleCapPct}); err != nil {
			return err
		}
		return t.s.Audit.Log(ctx, q, p, "event.ticket_policy", "event", ev.ID,
			map[string]any{"transfer_enabled": ev.TransferEnabled, "resale_cap_pct": ev.ResaleCapPct}, in)
	})
	if err != nil {
		return nil, err
	}
	return &in, nil
}
