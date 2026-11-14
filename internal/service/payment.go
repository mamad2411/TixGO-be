package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"
	"tixgo/backend/internal/metrics"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/notify"
	"tixgo/backend/internal/payment"
	"tixgo/backend/internal/platform"
	"tixgo/backend/internal/repository/sqlcgen"
)

// PaymentService = webhook gateway (idempoten), penetapan lunas, pelunasan setelah kedaluwarsa, rekonsiliasi
// (docs/04 §5, 20 M7, M47). Prinsip: jangan percaya body webhook — verifikasi signature, cocokkan nominal, cek ulang via API.
type PaymentService struct{ s *Services }

func newPaymentService(s *Services) *PaymentService { return &PaymentService{s: s} }

var errNoStock = errors.New("stok tidak tersedia lagi")

// HandleNotification memproses notifikasi HTTP gateway. nil → 200 ke gateway (jangan retry).
func (p *PaymentService) HandleNotification(ctx context.Context, raw []byte, hdr http.Header) error {
	n, err := p.s.Gateway.ParseNotification(raw, hdr)
	if err != nil {
		return apperr.Validation("notifikasi tidak valid")
	}
	if !p.s.Gateway.VerifyNotification(n) {
		metrics.Webhooks.WithLabelValues(p.s.Gateway.Name(), "bad_signature").Inc()
		p.s.Log.Warn("signature webhook salah", "order_id", n.OrderID)
		return apperr.Unauthorized("signature tidak valid")
	}
	pay, err := p.s.Q.GetPaymentByRef(ctx, sqlcgen.GetPaymentByRefParams{Provider: p.s.Gateway.Name(), ProviderRef: &n.OrderID})
	if err != nil {
		if platform.IsNoRows(err) {
			return apperr.NotFound("transaksi")
		}
		return err
	}
	amt, err := payment.GrossAmountInt(n.GrossAmount)
	if err != nil || amt != pay.Amount {
		p.recon(ctx, "amount_mismatch", &pay, pay.OrderID, map[string]any{"notified": n.GrossAmount, "expected": pay.Amount})
		return nil // jangan proses; tidak perlu retry gateway
	}
	pid := pay.ID
	inserted, err := p.s.Q.InsertPaymentEvent(ctx, sqlcgen.InsertPaymentEventParams{ID: id.New(), PaymentID: &pid, ProviderEventID: n.EventID(), Payload: raw})
	if err != nil {
		return err
	}
	if inserted == 0 {
		if ev, err := p.s.Q.GetPaymentEvent(ctx, n.EventID()); err == nil && ev.ProcessedAt != nil {
			return nil // sudah diproses (T05: webhook ganda tidak menerbitkan tiket ganda)
		}
	}
	metrics.Webhooks.WithLabelValues(p.s.Gateway.Name(), "accepted").Inc()
	st, err := p.s.Gateway.Status(ctx, n.OrderID)
	if err != nil {
		if !errors.Is(err, payment.ErrNotFound) {
			return fmt.Errorf("cek status gateway: %w", err) // 5xx → gateway mencoba lagi
		}
		st = payment.TxStatus{OrderRef: n.OrderID, Status: n.NormalizedStatus(), PaymentType: n.PaymentType, GrossAmount: amt}
	}
	if st.GrossAmount != 0 && st.GrossAmount != pay.Amount {
		p.recon(ctx, "amount_mismatch", &pay, pay.OrderID, map[string]any{"status_api": st.GrossAmount, "expected": pay.Amount})
		return nil
	}
	if st.PaymentType == "" {
		st.PaymentType = n.PaymentType
	}
	if err := p.apply(ctx, pay, st); err != nil {
		return err
	}
	return p.s.Q.MarkPaymentEventProcessed(ctx, n.EventID())
}

func (p *PaymentService) recon(ctx context.Context, kind string, pay *sqlcgen.Payment, orderID string, detail map[string]any) {
	var pid, oid *string
	if pay != nil {
		pid = &pay.ID
	}
	if orderID != "" {
		oid = &orderID
	}
	if err := p.s.Q.InsertReconException(ctx, sqlcgen.InsertReconExceptionParams{ID: id.New(), Kind: kind, PaymentID: pid, OrderID: oid, Detail: mustJSON(detail)}); err != nil {
		p.s.Log.Error("catat recon exception gagal", "error", err)
	}
}

var dbPaymentStatus = map[payment.Status]string{
	payment.StatusPending: "pending", payment.StatusChallenge: "pending", payment.StatusSettled: "settled", payment.StatusFailed: "failed",
	payment.StatusCancelled: "failed", payment.StatusExpired: "expired", payment.StatusRefunded: "refunded", payment.StatusPartialRefund: "settled",
}

// apply menerapkan status gateway ke pembayaran & order secara idempoten.
func (p *PaymentService) apply(ctx context.Context, pay sqlcgen.Payment, st payment.TxStatus) error {
	method := strPtr(st.PaymentType)
	switch st.Status {
	case payment.StatusSettled:
		return p.applySettled(ctx, pay, st)
	case payment.StatusRefunded, payment.StatusPartialRefund:
		p.recon(ctx, "external_refund", &pay, pay.OrderID, map[string]any{"status": string(st.Status)})
		return nil
	case payment.StatusChallenge:
		p.recon(ctx, "fraud_challenge", &pay, pay.OrderID, map[string]any{"fraud_status": st.FraudStatus})
		return p.s.Q.UpdatePaymentStatus(ctx, sqlcgen.UpdatePaymentStatusParams{ID: pay.ID, Status: "pending", Method: method})
	default:
		if pay.Status == "settled" {
			return nil // jangan menurunkan status lunas
		}
		return p.s.Q.UpdatePaymentStatus(ctx, sqlcgen.UpdatePaymentStatusParams{ID: pay.ID, Status: dbPaymentStatus[st.Status], Method: method})
	}
}

func (p *PaymentService) applySettled(ctx context.Context, pay sqlcgen.Payment, st payment.TxStatus) error {
	var ev sqlcgen.Event
	var ord sqlcgen.Order
	var settled, noStock bool
	err := p.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		cur, err := q.GetPaymentByRefForUpdate(ctx, sqlcgen.GetPaymentByRefForUpdateParams{Provider: pay.Provider, ProviderRef: pay.ProviderRef})
		if err != nil {
			return err
		}
		if cur.Status == "settled" {
			return nil
		}
		ord, err = q.GetOrderForUpdate(ctx, cur.OrderID)
		if err != nil {
			return err
		}
		ev, err = q.GetEvent(ctx, ord.EventID)
		if err != nil {
			return err
		}
		paidAt := st.SettledAt
		if paidAt.IsZero() {
			paidAt = p.s.now()
		}
		if err := q.UpdatePaymentStatus(ctx, sqlcgen.UpdatePaymentStatusParams{ID: cur.ID, Status: "settled", Method: strPtr(st.PaymentType), SettledAt: &paidAt}); err != nil {
			return err
		}
		switch ord.Status {
		case "pending":
			err := p.settleTx(ctx, q, ev, &ord, paidAt, st.PaymentType, false)
			switch {
			case err == nil:
				settled = true
			case errors.Is(err, errNoStock): // resale: tiket sudah tidak tersedia → refund otomatis
				noStock = true
				if err := p.s.Refund.autoCreateTx(ctx, q, ord, cur, "tiket resale sudah tidak tersedia", "admin"); err != nil {
					return err
				}
				p.reconTx(ctx, q, "paid_resale_unavailable", cur.ID, ord.ID, nil)
			default:
				return err
			}
		case "expired", "cancelled":
			// bayar setelah kedaluwarsa (M47 AC2): bila stok masih bebas → tetap terbitkan; jika tidak → refund otomatis
			err := p.settleTx(ctx, q, ev, &ord, paidAt, st.PaymentType, true)
			switch {
			case err == nil:
				settled = true
				p.reconTx(ctx, q, "paid_after_expire_fulfilled", cur.ID, ord.ID, nil)
			case errors.Is(err, errNoStock):
				noStock = true
				if err := p.s.Refund.autoCreateTx(ctx, q, ord, cur, "bayar setelah pesanan kedaluwarsa; kursi sudah diambil", "admin"); err != nil {
					return err
				}
				p.reconTx(ctx, q, "paid_after_expire_refunded", cur.ID, ord.ID, nil)
			default:
				return err
			}
		case "paid":
			// sudah lunas lewat percobaan pembayaran lain → pembayaran ganda: refund otomatis
			if err := p.s.Refund.autoCreateTx(ctx, q, ord, cur, "pembayaran ganda untuk pesanan yang sudah lunas", "admin"); err != nil {
				return err
			}
			p.reconTx(ctx, q, "duplicate_payment", cur.ID, ord.ID, nil)
			noStock = true
		default:
			p.reconTx(ctx, q, "paid_unexpected_state", cur.ID, ord.ID, map[string]any{"order_status": ord.Status})
		}
		return nil
	})
	if err != nil {
		return err
	}
	user, _ := p.s.Q.GetUserByID(ctx, ord.UserID)
	if settled {
		p.afterSettle(ctx, ev, ord, user)
	}
	if noStock {
		p.s.Refund.kick(ctx, ord.ID)
	}
	return nil
}

func (p *PaymentService) reconTx(ctx context.Context, q *sqlcgen.Queries, kind, paymentID, orderID string, detail map[string]any) {
	_ = q.InsertReconException(ctx, sqlcgen.InsertReconExceptionParams{ID: id.New(), Kind: kind, PaymentID: &paymentID, OrderID: &orderID, Detail: mustJSON(detail)})
}

// settleTx: inventori held→sold, order paid, tiket, invoice, ledger, event outbox — satu transaksi.
func (p *PaymentService) settleTx(ctx context.Context, q *sqlcgen.Queries, ev sqlcgen.Event, ord *sqlcgen.Order, paidAt time.Time, method string, resell bool) error {
	if ord.ResaleListingID != nil {
		return p.s.Resale.settleTx(ctx, q, ev, ord, paidAt, method)
	}
	var seatIDs []string
	tierQty := map[string]int{}
	if ord.HoldID != nil {
		items, err := q.ListHoldItems(ctx, *ord.HoldID)
		if err != nil {
			return err
		}
		for _, it := range items {
			if it.SeatID != nil {
				seatIDs = append(seatIDs, *it.SeatID)
			} else {
				tierQty[it.TierID] += it.Qty
			}
		}
	}
	sort.Strings(seatIDs)
	if len(seatIDs) > 0 {
		var n int64
		var err error
		if resell {
			n, err = q.ReSellFreeSeats(ctx, seatIDs)
		} else {
			n, err = q.SellSeats(ctx, seatIDs)
		}
		if err != nil {
			return err
		}
		if int(n) != len(seatIDs) {
			if resell {
				return errNoStock
			}
			return fmt.Errorf("inventori kursi tidak konsisten untuk order %s: %d/%d", ord.ID, n, len(seatIDs))
		}
	}
	tids := make([]string, 0, len(tierQty))
	for t := range tierQty {
		tids = append(tids, t)
	}
	sort.Strings(tids)
	for _, t := range tids {
		var n int64
		var err error
		if resell {
			n, err = q.SellTierFree(ctx, sqlcgen.SellTierFreeParams{ID: t, Qty: tierQty[t]})
		} else {
			n, err = q.SellTierHeld(ctx, sqlcgen.SellTierHeldParams{ID: t, Qty: tierQty[t]})
		}
		if err != nil {
			return err
		}
		if n == 0 {
			if resell {
				return errNoStock
			}
			return fmt.Errorf("inventori tier tidak konsisten untuk order %s tier %s", ord.ID, t)
		}
	}
	if err := q.MarkOrderPaid(ctx, sqlcgen.MarkOrderPaidParams{ID: ord.ID, PaidAt: &paidAt, PaymentMethod: strPtr(method)}); err != nil {
		return err
	}
	ord.Status, ord.PaidAt, ord.PaymentMethod = "paid", &paidAt, strPtr(method)
	if ord.HoldID != nil {
		if err := q.SetHoldStatus(ctx, sqlcgen.SetHoldStatusParams{ID: *ord.HoldID, Status: "converted", OrderID: &ord.ID}); err != nil {
			return err
		}
	}
	tickets, err := p.s.Ticket.issue(ctx, q, ev, *ord)
	if err != nil {
		return err
	}
	if err := p.s.Squad.markPaidTx(ctx, q, ord.ID); err != nil {
		return err
	}
	if _, err := p.s.Invoice.Issue(ctx, q, *ord, paidAt); err != nil {
		return err
	}
	if err := p.s.Ledger.PostOrderPaid(ctx, q, *ord, ev.OrganizerID); err != nil {
		return err
	}
	if len(seatIDs) > 0 {
		if err := emit(ctx, q, "event."+ev.ID, "seat.updated", map[string]any{"seat_ids": seatIDs, "status": "sold"}); err != nil {
			return err
		}
	}
	for _, t := range tids {
		if err := emit(ctx, q, "event."+ev.ID, "inventory.updated", map[string]any{"tier_id": t}); err != nil {
			return err
		}
	}
	tids2 := make([]string, 0, len(tickets))
	for _, t := range tickets {
		tids2 = append(tids2, t.ID)
	}
	if err := emit(ctx, q, "user."+ord.UserID, "order.paid", map[string]any{"order_id": ord.ID, "ticket_ids": tids2}); err != nil {
		return err
	}
	return emit(ctx, q, "org."+ev.ID, "sales.tick", map[string]any{"order_id": ord.ID, "total": ord.Total, "tickets": len(tickets)})
}

// afterSettle: efek samping setelah commit (email, analitik, cache, webhook organizer).
func (p *PaymentService) afterSettle(ctx context.Context, ev sqlcgen.Event, ord sqlcgen.Order, user sqlcgen.User) {
	pol := ParseRefundPolicy(ord.RefundPolicySnapshot, p.s.Cfg.RefundRescheduleWindowDays)
	p.s.Notifier.Send(ctx, NotifyReq{UserID: ord.UserID, Locale: user.Locale, Template: "order_paid", Data: map[string]any{
		"name": user.Name, "event_title": ev.Title, "total": formatIDR(ord.Total), "order_id": ord.ID,
		"tickets_url": p.s.Cfg.PublicWebURL + "/tiket", "refund_summary": pol.Summary(user.Locale)}})
	if ord.ResaleListingID != nil {
		p.s.Resale.afterSettle(ctx, ord)
	}
	p.s.Analytics.Track(ctx, ev.ID, "paid")
	p.s.Analytics.Track(ctx, ev.ID, "ticket")
	p.s.Catalog.InvalidateAvailability(ctx, ev.ID)
	p.s.Queue.FreeSlot(ctx, ev.ID, ord.UserID, true)
	// webhook organizer (tanpa PII pembeli): order.paid + ticket.issued
	p.s.Integration.Emit(ctx, ev.OrganizerID, "order.paid", map[string]any{"order_id": ord.ID, "event_id": ev.ID, "total": ord.Total, "paid_at": ord.PaidAt})
	if tks, err := p.s.Q.ListOrderTickets(ctx, ord.ID); err == nil {
		ids := make([]string, 0, len(tks))
		for _, tk := range tks {
			ids = append(ids, tk.ID)
		}
		p.s.Integration.Emit(ctx, ev.OrganizerID, "ticket.issued", map[string]any{"order_id": ord.ID, "event_id": ev.ID, "ticket_ids": ids})
	}
}

// Reconcile (worker, ±5 menit): transaksi pending yang webhook-nya tidak kunjung datang dicek ke gateway (T39).
func (p *PaymentService) Reconcile(ctx context.Context) (int, error) {
	now := p.s.now()
	rows, err := p.s.Q.StalePendingPayments(ctx, sqlcgen.StalePendingPaymentsParams{Before: now.Add(-5 * time.Minute), After: now.Add(-72 * time.Hour), Lim: 200})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, pay := range rows {
		st, err := p.s.Gateway.Status(ctx, deref(pay.ProviderRef))
		if err != nil {
			if errors.Is(err, payment.ErrNotFound) {
				continue
			}
			p.s.Log.Error("reconcile status gagal", "payment", pay.ID, "error", err)
			continue
		}
		if st.Status == payment.StatusPending {
			continue
		}
		if st.GrossAmount != 0 && st.GrossAmount != pay.Amount {
			p.recon(ctx, "amount_mismatch", &pay, pay.OrderID, map[string]any{"status_api": st.GrossAmount, "expected": pay.Amount})
			continue
		}
		if err := p.apply(ctx, pay, st); err != nil {
			p.s.Log.Error("reconcile apply gagal", "payment", pay.ID, "error", err)
			continue
		}
		n++
	}
	return n, nil
}

func formatIDR(v int64) string { return notify.FormatIDR(v) }
