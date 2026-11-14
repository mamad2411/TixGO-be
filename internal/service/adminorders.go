package service

import (
	"context"
	"strings"
	"time"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/repository/sqlcgen"
)

// AdminOrders = alat operasional support/finance: cari & lihat pesanan, sinkron ulang status bayar, refund oleh admin (docs/20 M47 AC1 "tindakan terpandu").
type AdminOrders struct{ s *Services }

func newAdminOrders(s *Services) *AdminOrders { return &AdminOrders{s: s} }

type AdminOrderRow struct {
	ID         string     `json:"id"`
	Status     string     `json:"status"`
	Total      int64      `json:"total"`
	UserEmail  string     `json:"user_email"`
	EventTitle string     `json:"event_title"`
	CreatedAt  time.Time  `json:"created_at"`
	PaidAt     *time.Time `json:"paid_at,omitempty"`
}

func (a *AdminOrders) Search(ctx context.Context, p *auth.Principal, q, status string, limit, offset int) ([]AdminOrderRow, error) {
	q = strings.TrimSpace(q)
	rows, err := a.s.Q.AdminSearchOrders(ctx, sqlcgen.AdminSearchOrdersParams{Q: strPtr(q), Status: strPtr(status), Lim: int32(limit), Off: int32(offset)})
	if err != nil {
		return nil, err
	}
	out := make([]AdminOrderRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, AdminOrderRow{ID: r.ID, Status: r.Status, Total: r.Total, UserEmail: r.UserEmail, EventTitle: r.EventTitle, CreatedAt: r.CreatedAt, PaidAt: r.PaidAt})
	}
	_ = a.s.InTx(ctx, func(tx *sqlcgen.Queries) error { // akses data pribadi oleh staf teraudit
		return a.s.Audit.Log(ctx, tx, p, "admin.order_search", "order", "", nil, map[string]any{"rows": len(out)})
	})
	return out, nil
}

type AdminOrderDetail struct {
	Order    AdminOrderRow    `json:"order"`
	Fee      int64            `json:"fee"`
	Discount int64            `json:"discount"`
	Items    int              `json:"items"`
	Payments []map[string]any `json:"payments"`
	Refunds  []RefundView     `json:"refunds"`
	Tickets  []map[string]any `json:"tickets"`
}

func (a *AdminOrders) Get(ctx context.Context, p *auth.Principal, orderID string) (*AdminOrderDetail, error) {
	o, err := a.s.Q.GetOrder(ctx, orderID)
	if err != nil {
		return nil, apperr.NotFound("pesanan")
	}
	u, _ := a.s.Q.GetUserByID(ctx, o.UserID)
	ev, _ := a.s.Q.GetEvent(ctx, o.EventID)
	d := &AdminOrderDetail{Order: AdminOrderRow{ID: o.ID, Status: o.Status, Total: o.Total, UserEmail: u.Email, EventTitle: ev.Title, CreatedAt: o.CreatedAt, PaidAt: o.PaidAt}, Fee: o.Fee, Discount: o.Discount,
		Payments: []map[string]any{}, Refunds: []RefundView{}, Tickets: []map[string]any{}}
	items, _ := a.s.Q.ListOrderItems(ctx, o.ID)
	d.Items = len(items)
	pays, _ := a.s.Q.ListPaymentsForOrder(ctx, o.ID)
	for _, pm := range pays {
		d.Payments = append(d.Payments, map[string]any{"id": pm.ID, "provider": pm.Provider, "provider_ref": pm.ProviderRef, "method": pm.Method, "amount": pm.Amount, "status": pm.Status, "created_at": pm.CreatedAt, "settled_at": pm.SettledAt})
	}
	rfs, _ := a.s.Q.ListRefundsForOrder(ctx, o.ID)
	for _, r := range rfs {
		d.Refunds = append(d.Refunds, refundView(r))
	}
	tks, _ := a.s.Q.ListOrderTickets(ctx, o.ID)
	for _, t := range tks {
		d.Tickets = append(d.Tickets, map[string]any{"id": t.ID, "status": t.Status, "entry_number": t.EntryNumber, "seat": t.SeatLabel})
	}
	_ = a.s.InTx(ctx, func(tx *sqlcgen.Queries) error {
		return a.s.Audit.Log(ctx, tx, p, "admin.order_view", "order", orderID, nil, nil)
	})
	return d, nil
}

// Resync memeriksa ulang pembayaran TERAKHIR order ke gateway lalu menerapkan hasilnya (webhook hilang/terlambat). Idempoten.
func (a *AdminOrders) Resync(ctx context.Context, p *auth.Principal, orderID string) (map[string]any, error) {
	pay, err := a.s.Q.LatestPaymentForOrder(ctx, orderID)
	if err != nil || pay.ProviderRef == nil {
		return nil, apperr.NotFound("pembayaran untuk pesanan ini")
	}
	st, err := a.s.Gateway.Status(ctx, *pay.ProviderRef)
	if err != nil {
		return nil, apperr.New(502, apperr.CodePaymentFailed, "gateway tidak dapat dihubungi: "+err.Error())
	}
	if err := a.s.Payment.apply(ctx, pay, st); err != nil {
		return nil, err
	}
	o, _ := a.s.Q.GetOrder(ctx, orderID)
	_ = a.s.InTx(ctx, func(tx *sqlcgen.Queries) error {
		return a.s.Audit.Log(ctx, tx, p, "admin.order_resync", "order", orderID, nil, map[string]any{"gateway_status": string(st.Status)})
	})
	return map[string]any{"gateway_status": st.Status, "order_status": o.Status}, nil
}

// Refund oleh finance: refund PENUH pesanan lunas (tiket dibatalkan, ledger dibalik) memakai jalur yang sama dengan refund pembeli
// (gateway bila memungkinkan, manual bila tidak). Ditolak bila ada tiket yang sudah dipakai masuk.
func (a *AdminOrders) Refund(ctx context.Context, p *auth.Principal, orderID, reason string) (*RefundView, error) {
	if !p.MFAPassed {
		return nil, apperr.ForbiddenCode(apperr.CodeMFARequired, "verifikasi dua langkah diperlukan").With("setup", true)
	}
	if len([]rune(strings.TrimSpace(reason))) < 5 {
		return nil, apperr.Validation("alasan refund wajib diisi").With("field", "reason")
	}
	var rf sqlcgen.Refund
	err := a.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		ord, err := q.GetOrderForUpdate(ctx, orderID)
		if err != nil {
			return apperr.NotFound("pesanan")
		}
		if ord.Status != "paid" || ord.Total <= 0 {
			return apperr.Unprocessable(apperr.CodeRefundNotAllowed, "hanya pesanan lunas berbayar yang dapat di-refund")
		}
		if used, _ := q.UsedTicketsInOrder(ctx, ord.ID); used > 0 {
			return apperr.Unprocessable(apperr.CodeRefundNotAllowed, "ada tiket yang sudah dipakai masuk")
		}
		if _, err := q.OpenRefundForOrder(ctx, ord.ID); err == nil {
			return apperr.Conflict(apperr.CodeConflict, "sudah ada refund berjalan untuk pesanan ini")
		}
		pay, err := q.SettledPaymentForOrder(ctx, ord.ID)
		if err != nil {
			return apperr.Conflict(apperr.CodeInvalidState, "pembayaran lunas tidak ditemukan")
		}
		rf, err = a.s.Refund.insertTx(ctx, q, ord, pay, ord.Total, reason, "admin", "order")
		if err != nil {
			return err
		}
		return a.s.Audit.Log(ctx, q, p, "admin.order_refund", "refund", rf.ID, nil, map[string]any{"order": orderID, "amount": ord.Total, "reason": reason})
	})
	if err != nil {
		return nil, err
	}
	a.s.Refund.kick(ctx, orderID)
	v := refundView(rf)
	return &v, nil
}
