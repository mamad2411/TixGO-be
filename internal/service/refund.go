package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"tixgo/backend/internal/metrics"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/jobs"
	"tixgo/backend/internal/notify"
	"tixgo/backend/internal/payment"
	"tixgo/backend/internal/platform"
	"tixgo/backend/internal/repository/sqlcgen"
)

// RefundService = refund dua jalur (gateway/manual), kebijakan per event, efek konsisten (docs/22, 11 §3, 20 M17).
type RefundService struct{ s *Services }

const (
	jobRefundProcess  = "refund:process"
	maxRefundAttempts = 5
)

func newRefundService(s *Services) *RefundService {
	r := &RefundService{s: s}
	s.Reg.Handle(jobRefundProcess, r.handleProcess)
	return r
}

type RefundView struct {
	ID           string     `json:"id"`
	OrderID      string     `json:"order_id"`
	Amount       int64      `json:"amount"`
	Status       string     `json:"status"`
	Method       string     `json:"method"`
	InitiatedBy  string     `json:"initiated_by"`
	Reason       *string    `json:"reason,omitempty"`
	Note         *string    `json:"note,omitempty"`
	NeedsAccount bool       `json:"needs_account"`
	DueAt        *time.Time `json:"due_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

func refundView(r sqlcgen.Refund) RefundView {
	return RefundView{ID: r.ID, OrderID: r.OrderID, Amount: r.Amount, Status: r.Status, Method: r.Method, InitiatedBy: r.InitiatedBy, Reason: r.Reason,
		Note: r.Note, NeedsAccount: r.Status == "awaiting_account", DueAt: r.DueAt, CreatedAt: r.CreatedAt}
}

// ---------- kelayakan & jumlah ----------

// EligibleAmount menghitung jumlah refund sukarela menurut SNAPSHOT kebijakan saat bayar (tidak berlaku surut).
func (r *RefundService) eligibleAmount(ord sqlcgen.Order, ev sqlcgen.Event, now time.Time) (int64, string, error) {
	pol := ParseRefundPolicy(ord.RefundPolicySnapshot, r.s.Cfg.RefundRescheduleWindowDays)
	// jadwal ulang / ditunda: tiket tetap berlaku, refund penuh dalam jendela sejak pengumuman
	if (ev.ScheduleState == "rescheduled" || ev.ScheduleState == "postponed") && ev.ScheduleChangedAt != nil {
		until := ev.ScheduleChangedAt.Add(time.Duration(pol.OnReschedule.WindowDays) * 24 * time.Hour)
		if !now.After(until) {
			return ord.Total, "reschedule_window", nil
		}
	}
	if pol.Type == "refundable" && pol.RefundableUntil != nil && !now.After(*pol.RefundableUntil) {
		paidPart := ord.Subtotal - ord.Discount + ord.AddonsTotal
		deduction := (paidPart*int64(pol.DeductionPct) + 99) / 100 // potongan dibulatkan ke atas
		amt := paidPart - deduction
		if pol.FeeRefundable {
			amt += ord.Fee
		}
		if amt <= 0 {
			return 0, "", apperr.Unprocessable(apperr.CodeRefundNotAllowed, "jumlah refund 0 menurut kebijakan")
		}
		return amt, "policy", nil
	}
	return 0, "", apperr.Unprocessable(apperr.CodeRefundNotAllowed, "pesanan ini tidak dapat di-refund menurut kebijakan event").
		With("policy", pol.Type).With("summary", pol.Summary("id"))
}

// Eligibility = pratinjau untuk UI ("berapa yang kembali bila batal?").
type Eligibility struct {
	Eligible bool   `json:"eligible"`
	Amount   int64  `json:"amount"`
	Basis    string `json:"basis,omitempty"`
	Summary  string `json:"summary"`
	Reason   string `json:"reason,omitempty"`
}

func (r *RefundService) Preview(ctx context.Context, userID, orderID string) (*Eligibility, error) {
	ord, err := r.s.Order.own(ctx, r.s.Q, userID, orderID)
	if err != nil {
		return nil, err
	}
	pol := ParseRefundPolicy(ord.RefundPolicySnapshot, r.s.Cfg.RefundRescheduleWindowDays)
	out := &Eligibility{Summary: pol.Summary("id")}
	if err := r.checkOrderRefundable(ctx, r.s.Q, ord); err != nil {
		out.Reason = err.Error()
		return out, nil
	}
	ev, err := r.s.Q.GetEvent(ctx, ord.EventID)
	if err != nil {
		return nil, err
	}
	amt, basis, err := r.eligibleAmount(ord, ev, r.s.now())
	if err != nil {
		out.Reason = err.Error()
		return out, nil
	}
	out.Eligible, out.Amount, out.Basis = true, amt, basis
	return out, nil
}

func (r *RefundService) checkOrderRefundable(ctx context.Context, q *sqlcgen.Queries, ord sqlcgen.Order) error {
	if ord.Status != "paid" {
		return apperr.Unprocessable(apperr.CodeRefundNotAllowed, "hanya pesanan lunas yang dapat di-refund")
	}
	if ord.Total <= 0 {
		return apperr.Unprocessable(apperr.CodeRefundNotAllowed, "pesanan gratis tidak ada yang di-refund")
	}
	tks, err := q.ListOrderTickets(ctx, ord.ID)
	if err != nil {
		return err
	}
	for _, t := range tks {
		if l, err := q.ActiveListingForTicket(ctx, t.ID); err == nil && l.ID != "" {
			return apperr.Unprocessable(apperr.CodeRefundNotAllowed, "tiket sedang dijual ulang; batalkan listing dulu")
		}
		if t.Status != "valid" || t.OwnerUserID != ord.UserID {
			return apperr.Unprocessable(apperr.CodeRefundNotAllowed, "tiket sudah dipakai, ditransfer, atau tidak aktif")
		}
	}
	if _, err := q.OpenRefundForOrder(ctx, ord.ID); err == nil {
		return apperr.Conflict(apperr.CodeConflict, "sudah ada permintaan refund yang berjalan untuk pesanan ini")
	}
	return nil
}

// ---------- permintaan pembeli ----------

func (r *RefundService) Request(ctx context.Context, userID, orderID, reason string) (*RefundView, error) {
	var created sqlcgen.Refund
	err := r.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		ord, err := q.GetOrderForUpdate(ctx, orderID)
		if err != nil || ord.UserID != userID {
			return apperr.NotFound("pesanan")
		}
		if err := r.checkOrderRefundable(ctx, q, ord); err != nil {
			return err
		}
		ev, err := q.GetEvent(ctx, ord.EventID)
		if err != nil {
			return err
		}
		amt, _, err := r.eligibleAmount(ord, ev, r.s.now())
		if err != nil {
			return err
		}
		pay, err := q.SettledPaymentForOrder(ctx, ord.ID)
		if err != nil {
			return apperr.Conflict(apperr.CodeInvalidState, "pembayaran lunas tidak ditemukan")
		}
		created, err = r.insertTx(ctx, q, ord, pay, amt, reason, "buyer", "order")
		if err != nil {
			return err
		}
		return r.s.Audit.Log(ctx, q, &auth.Principal{UserID: userID, Role: "customer"}, "refund.request", "refund", created.ID, nil, map[string]any{"order": orderID, "amount": amt})
	})
	if err != nil {
		return nil, err
	}
	r.kick(ctx, orderID)
	v := refundView(created)
	return &v, nil
}

// insertTx membuat baris refund; jalur ditentukan dari metode bayar + umur transaksi (docs/22 §1).
func (r *RefundService) insertTx(ctx context.Context, q *sqlcgen.Queries, ord sqlcgen.Order, pay sqlcgen.Payment, amount int64, reason, by, scope string) (sqlcgen.Refund, error) {
	method := "manual"
	paidAt := r.s.now()
	if pay.SettledAt != nil {
		paidAt = *pay.SettledAt
	}
	if payment.CanRefundViaAPI(deref(pay.Method), paidAt, r.s.now()) {
		method = "gateway"
	}
	status := "approved"
	if method == "manual" {
		status = "awaiting_account"
	}
	due := r.s.now().Add(time.Duration(r.s.Cfg.RefundSLADays) * 24 * time.Hour)
	pid := pay.ID
	ref, err := q.InsertRefund(ctx, sqlcgen.InsertRefundParams{ID: id.New(), OrderID: ord.ID, PaymentID: &pid, Scope: scope, Amount: amount, Reason: strPtr(reason),
		Status: status, Method: method, InitiatedBy: by, DueAt: &due})
	if err != nil {
		if platform.IsUniqueViolation(err) {
			return ref, apperr.Conflict(apperr.CodeConflict, "sudah ada permintaan refund yang berjalan untuk pesanan ini")
		}
		return ref, err
	}
	return ref, nil
}

// autoCreateTx: refund otomatis penuh untuk pembayaran tertentu (bayar-setelah-expire tanpa stok, pembayaran ganda).
func (r *RefundService) autoCreateTx(ctx context.Context, q *sqlcgen.Queries, ord sqlcgen.Order, pay sqlcgen.Payment, reason, by string) error {
	_, err := r.insertTx(ctx, q, ord, pay, pay.Amount, reason, by, "payment")
	return err
}

// kick mengantrekan pemrosesan refund terbuka milik order.
func (r *RefundService) Kick(ctx context.Context, orderID string) { r.kick(ctx, orderID) }

func (r *RefundService) kick(ctx context.Context, orderID string) {
	rows, err := r.s.Q.ListRefundsForOrder(ctx, orderID)
	if err != nil {
		return
	}
	for _, rf := range rows {
		if rf.Status == "approved" || rf.Status == "refunding" || rf.Status == "awaiting_account" {
			_ = r.s.Jobs.Enqueue(ctx, jobRefundProcess, map[string]string{"id": rf.ID}, jobs.MaxRetry(maxRefundAttempts), jobs.Unique("refund:"+rf.ID, time.Minute))
		}
	}
}

func (r *RefundService) handleProcess(ctx context.Context, payload []byte) error {
	p, err := jobs.Decode[map[string]string](payload)
	if err != nil {
		return nil
	}
	return r.Process(ctx, p["id"])
}

// Process menjalankan satu refund. Jalur gateway: panggil API; bila tidak didukung/ditolak → manual (eksplisit & tercatat).
func (r *RefundService) Process(ctx context.Context, refundID string) error {
	rf, err := r.s.Q.GetRefund(ctx, refundID)
	if err != nil {
		return err
	}
	switch rf.Status {
	case "awaiting_account":
		r.notifyAccountNeeded(ctx, rf)
		return nil
	case "approved", "refunding":
	default:
		return nil
	}
	if rf.Method != "gateway" {
		return nil // jalur manual diproses finance
	}
	n, err := r.s.Q.StartRefundAttempt(ctx, rf.ID)
	if err != nil || n == 0 {
		return err
	}
	rf, _ = r.s.Q.GetRefund(ctx, refundID)
	pay, err := r.s.Q.GetPayment(ctx, deref(rf.PaymentID))
	if err != nil {
		return err
	}
	paidAt := r.s.now()
	if pay.SettledAt != nil {
		paidAt = *pay.SettledAt
	}
	res, gerr := r.s.Gateway.Refund(ctx, payment.RefundReq{OrderRef: deref(pay.ProviderRef), Key: rf.ID, Amount: rf.Amount, Reason: deref(rf.Reason),
		PaymentType: deref(pay.Method), PaidAt: paidAt, Now: r.s.now()})
	if gerr != nil {
		switch {
		case errors.Is(gerr, payment.ErrRefundUnsupported), errors.Is(gerr, payment.ErrRefundWindow), errors.Is(gerr, payment.ErrRefundRejected):
			return r.fallbackManual(ctx, rf, "gateway tidak dapat memproses: "+gerr.Error())
		default:
			if int(rf.Attempts) >= maxRefundAttempts {
				return r.fallbackManual(ctx, rf, fmt.Sprintf("gateway gagal setelah %d percobaan: %v", rf.Attempts, gerr))
			}
			_ = r.s.Q.SetRefundFailedNote(ctx, sqlcgen.SetRefundFailedNoteParams{ID: rf.ID, Note: strPtr(gerr.Error())})
			return gerr // retry oleh asynq
		}
	}
	return r.finalize(ctx, rf.ID, &res.RefundID, nil, nil, nil)
}

// fallbackManual mengalihkan ke jalur manual (tercatat; dana tidak hilang).
func (r *RefundService) fallbackManual(ctx context.Context, rf sqlcgen.Refund, why string) error {
	due := r.s.now().Add(time.Duration(r.s.Cfg.RefundAccountDeadlineDays) * 24 * time.Hour)
	if err := r.s.Q.SetRefundManual(ctx, sqlcgen.SetRefundManualParams{ID: rf.ID, Note: strPtr(why), DueAt: &due}); err != nil {
		return err
	}
	_ = r.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		return r.s.Audit.LogSystem(ctx, q, "refund.fallback_manual", "refund", rf.ID, map[string]any{"why": why})
	})
	rf2, _ := r.s.Q.GetRefund(ctx, rf.ID)
	r.notifyAccountNeeded(ctx, rf2)
	return nil
}

func (r *RefundService) notifyAccountNeeded(ctx context.Context, rf sqlcgen.Refund) {
	ord, err := r.s.Q.GetOrder(ctx, rf.OrderID)
	if err != nil {
		return
	}
	ev, _ := r.s.Q.GetEvent(ctx, ord.EventID)
	deadline := r.s.now().Add(time.Duration(r.s.Cfg.RefundAccountDeadlineDays) * 24 * time.Hour)
	if rf.DueAt != nil {
		deadline = *rf.DueAt
	}
	r.s.Notifier.Send(ctx, NotifyReq{UserID: ord.UserID, Template: "refund_account_needed", Data: map[string]any{
		"name": "", "event_title": ev.Title, "amount": notify.FormatIDR(rf.Amount), "link": r.s.Cfg.PublicWebURL + "/refund", "deadline": deadline.Format("02 Jan 2006")}})
}

var acctRe = regexp.MustCompile(`^[0-9]{6,20}$`)

// SetAccount: pembeli mengisi rekening tujuan (jalur manual). Nomor dienkripsi.
func (r *RefundService) SetAccount(ctx context.Context, userID, refundID, bankCode, accountName, accountNo string) (*RefundView, error) {
	rf, err := r.s.Q.GetRefund(ctx, refundID)
	if err != nil {
		return nil, apperr.NotFound("refund")
	}
	ord, err := r.s.Q.GetOrder(ctx, rf.OrderID)
	if err != nil || ord.UserID != userID {
		return nil, apperr.NotFound("refund")
	}
	if rf.Status != "awaiting_account" {
		return nil, apperr.Conflict(apperr.CodeInvalidState, "refund tidak menunggu rekening")
	}
	bankCode = strings.ToUpper(strings.TrimSpace(bankCode))
	accountNo = strings.NewReplacer(" ", "", "-", "").Replace(accountNo)
	accountName = strings.TrimSpace(accountName)
	if !bankCodeRe.MatchString(bankCode) || !acctRe.MatchString(accountNo) || len([]rune(accountName)) < 2 {
		return nil, apperr.Validation("bank_code, account_no (6–20 digit), dan account_name wajib valid")
	}
	enc, err := r.s.Box.Encrypt([]byte(accountNo))
	if err != nil {
		return nil, err
	}
	if err := r.s.Q.SetRefundAccount(ctx, sqlcgen.SetRefundAccountParams{ID: refundID, RefundBankCode: &bankCode, RefundAccountName: &accountName, RefundAccountEnc: enc}); err != nil {
		return nil, err
	}
	nr, _ := r.s.Q.GetRefund(ctx, refundID)
	v := refundView(nr)
	return &v, nil
}

func (r *RefundService) Get(ctx context.Context, userID, refundID string) (*RefundView, error) {
	rf, err := r.s.Q.GetRefund(ctx, refundID)
	if err != nil {
		return nil, apperr.NotFound("refund")
	}
	ord, err := r.s.Q.GetOrder(ctx, rf.OrderID)
	if err != nil || ord.UserID != userID {
		return nil, apperr.NotFound("refund")
	}
	v := refundView(rf)
	return &v, nil
}

func (r *RefundService) ForOrder(ctx context.Context, userID, orderID string) ([]RefundView, error) {
	if _, err := r.s.Order.own(ctx, r.s.Q, userID, orderID); err != nil {
		return nil, err
	}
	rows, err := r.s.Q.ListRefundsForOrder(ctx, orderID)
	if err != nil {
		return nil, err
	}
	out := make([]RefundView, 0, len(rows))
	for _, x := range rows {
		out = append(out, refundView(x))
	}
	return out, nil
}

// ---------- finance (jalur manual) ----------

type FinanceRefundView struct {
	RefundView
	BankCode    *string `json:"bank_code,omitempty"`
	AccountName *string `json:"account_name,omitempty"`
	AccountNo   string  `json:"account_no,omitempty"` // didekripsi hanya untuk finance
	OrderTotal  int64   `json:"order_total"`
}

func (r *RefundService) FinanceList(ctx context.Context, p *auth.Principal, status, method string, limit, offset int) ([]FinanceRefundView, int, error) {
	rows, err := r.s.Q.ListFinanceRefunds(ctx, sqlcgen.ListFinanceRefundsParams{Status: strPtr(status), Method: strPtr(method), Lim: int32(limit), Off: int32(offset)})
	if err != nil {
		return nil, 0, err
	}
	n, err := r.s.Q.CountFinanceRefunds(ctx, sqlcgen.CountFinanceRefundsParams{Status: strPtr(status), Method: strPtr(method)})
	if err != nil {
		return nil, 0, err
	}
	out := make([]FinanceRefundView, 0, len(rows))
	for _, x := range rows {
		rf := sqlcgen.Refund{ID: x.ID, OrderID: x.OrderID, Amount: x.Amount, Reason: x.Reason, Status: x.Status, Method: x.Method, InitiatedBy: x.InitiatedBy, Note: x.Note, DueAt: x.DueAt, CreatedAt: x.CreatedAt}
		fv := FinanceRefundView{RefundView: refundView(rf), BankCode: x.RefundBankCode, AccountName: x.RefundAccountName, OrderTotal: x.OrderTotal}
		if len(x.RefundAccountEnc) > 0 {
			if b, err := r.s.Box.Decrypt(x.RefundAccountEnc); err == nil {
				fv.AccountNo = string(b)
			}
		}
		out = append(out, fv)
	}
	return out, int(n), nil
}

// MarkPaid: finance menandai transfer manual selesai + bukti. Pemisahan tugas: pemroses ≠ penyetuju (bila ada).
func (r *RefundService) MarkPaid(ctx context.Context, p *auth.Principal, refundID string, proof []byte, note string) (*RefundView, error) {
	rf, err := r.s.Q.GetRefund(ctx, refundID)
	if err != nil {
		return nil, apperr.NotFound("refund")
	}
	if rf.Method != "manual" || rf.Status != "refunding" {
		return nil, apperr.Conflict(apperr.CodeInvalidState, "refund manual harus berstatus refunding (rekening sudah diisi)")
	}
	if rf.ApprovedBy != nil && *rf.ApprovedBy == p.UserID {
		return nil, apperr.Forbidden("penyetuju tidak boleh memproses transfer yang sama")
	}
	if len(proof) == 0 {
		return nil, apperr.Validation("bukti transfer wajib diunggah").With("field", "proof")
	}
	key := "refund-proof/" + rf.ID
	if err := r.s.Store.Put(ctx, key, bytes.NewReader(proof), int64(len(proof)), "application/octet-stream"); err != nil {
		return nil, err
	}
	if err := r.finalize(ctx, rf.ID, nil, &key, &p.UserID, strPtr(note)); err != nil {
		return nil, err
	}
	nr, _ := r.s.Q.GetRefund(ctx, refundID)
	v := refundView(nr)
	return &v, nil
}

// ---------- efek akhir ----------

// finalize menandai refund selesai dan menjalankan SEMUA efek dalam satu transaksi (AC7):
// tiket, order, stok, credit note, ledger, outbox. Idempoten (refund sudah 'refunded' → no-op).
func (r *RefundService) finalize(ctx context.Context, refundID string, providerRef, proofKey, processedBy, note *string) error {
	var rf sqlcgen.Refund
	var ord sqlcgen.Order
	var ev sqlcgen.Event
	err := r.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		rf, err = q.GetRefundForUpdate(ctx, refundID)
		if err != nil {
			return err
		}
		if rf.Status == "refunded" {
			return nil
		}
		ord, err = q.GetOrderForUpdate(ctx, rf.OrderID)
		if err != nil {
			return err
		}
		ev, err = q.GetEvent(ctx, ord.EventID)
		if err != nil {
			return err
		}
		if err := q.SetRefundDone(ctx, sqlcgen.SetRefundDoneParams{ID: rf.ID, ProviderRef: providerRef, ProofKey: proofKey, ProcessedBy: processedBy, Note: note}); err != nil {
			return err
		}
		if rf.Scope == "order" && ord.Status == "paid" {
			if err := r.applyOrderRefundEffects(ctx, q, ev, ord, rf); err != nil {
				return err
			}
		}
		return emit(ctx, q, "user."+ord.UserID, "refund.done", map[string]any{"refund_id": rf.ID, "order_id": ord.ID})
	})
	if err != nil {
		return err
	}
	if rf.Status == "refunded" {
		return nil
	}
	metrics.Refunds.WithLabelValues(rf.Method, "done").Inc()
	r.s.Integration.Emit(ctx, ev.OrganizerID, "ticket.refunded", map[string]any{"order_id": ord.ID, "refund_id": rf.ID, "amount": rf.Amount})
	r.s.Catalog.InvalidateAvailability(ctx, ord.EventID)
	r.s.Notifier.Send(ctx, NotifyReq{UserID: ord.UserID, Template: "refund_update", Data: map[string]any{
		"name": "", "order_id": ord.ID, "amount": notify.FormatIDR(rf.Amount), "status": "selesai", "note": "Dana dikembalikan melalui " + map[string]string{"gateway": "metode pembayaran asal", "manual": "transfer ke rekening yang kamu isi"}[rf.Method] + "."}})
	if ev.ScheduleState != "cancelled" {
		r.s.Waitlist.OnInventoryFreed(ctx, ord.EventID)
	}
	return nil
}

func (r *RefundService) applyOrderRefundEffects(ctx context.Context, q *sqlcgen.Queries, ev sqlcgen.Event, ord sqlcgen.Order, rf sqlcgen.Refund) error {
	if _, err := q.VoidOrderTickets(ctx, sqlcgen.VoidOrderTicketsParams{OrderID: ord.ID, Status: "refunded"}); err != nil {
		return err
	}
	if err := q.SetOrderStatus(ctx, sqlcgen.SetOrderStatusParams{ID: ord.ID, Status: "refunded"}); err != nil {
		return err
	}
	if ord.ResaleListingID != nil { // resale: kursi tidak dilepas ke penjualan umum; pembukuan khusus
		if _, err := r.s.Invoice.IssueCreditNote(ctx, q, ord.ID, rf.ID, rf.Amount, r.s.now()); err != nil {
			return err
		}
		return r.s.Resale.reverseLedger(ctx, q, ord, rf)
	}
	// kembalikan stok untuk dijual lagi, kecuali acara batal
	if ev.ScheduleState != "cancelled" && ev.Status != stCancelled {
		items, err := q.ListOrderItems(ctx, ord.ID)
		if err != nil {
			return err
		}
		var seatIDs []string
		tierQty := map[string]int{}
		for _, it := range items {
			if it.SeatID != nil {
				seatIDs = append(seatIDs, *it.SeatID)
			} else {
				tierQty[it.TierID]++
			}
		}
		if len(seatIDs) > 0 {
			if _, err := q.UnsellSeats(ctx, seatIDs); err != nil {
				return err
			}
			if err := emit(ctx, q, "event."+ev.ID, "seat.updated", map[string]any{"seat_ids": seatIDs, "status": "available"}); err != nil {
				return err
			}
		}
		for t, n := range tierQty {
			if err := q.ReleaseTierSold(ctx, sqlcgen.ReleaseTierSoldParams{ID: t, Qty: n}); err != nil {
				return err
			}
		}
		ads, _ := q.ListOrderAddons(ctx, ord.ID)
		for _, a := range ads {
			if err := q.ReleaseAddon(ctx, sqlcgen.ReleaseAddonParams{ID: a.AddonID, Qty: a.Qty}); err != nil {
				return err
			}
		}
	}
	if _, err := r.s.Invoice.IssueCreditNote(ctx, q, ord.ID, rf.ID, rf.Amount, r.s.now()); err != nil {
		return err
	}
	if has, _ := q.LedgerTxExists(ctx, sqlcgen.LedgerTxExistsParams{RefType: "order_paid", RefID: ord.ID}); has {
		return r.postLedger(ctx, q, ev, ord, rf)
	}
	return nil
}

// postLedger membalik pembukuan sesuai kebijakan: biaya layanan hanya kembali bila dikembalikan ke pembeli / refund penuh.
func (r *RefundService) postLedger(ctx context.Context, q *sqlcgen.Queries, ev sqlcgen.Event, ord sqlcgen.Order, rf sqlcgen.Refund) error {
	full := rf.Amount == ord.Total
	feeBack := int64(0)
	switch {
	case full:
		feeBack = ord.Fee
	case rf.Amount > ord.Subtotal-ord.Discount+ord.AddonsTotal:
		feeBack = rf.Amount - (ord.Subtotal - ord.Discount + ord.AddonsTotal) // sebagian fee dikembalikan
	}
	orgFee := ord.PlatformFee - ord.Fee
	if orgFee < 0 {
		orgFee = 0
	}
	platformDebit := feeBack
	var orgCreditBack int64
	if full && orgFee > 0 { // fee yang ditanggung organizer tidak dipungut untuk pesanan yang dibatalkan penuh
		platformDebit += orgFee
		orgCreditBack = orgFee
	}
	orgDebit := rf.Amount - feeBack
	if orgDebit < 0 {
		orgDebit = 0
	}
	pend, _ := r.s.Ledger.OrganizerBalance(ctx, q, ev.OrganizerID)
	fromPending := orgDebit
	if fromPending > pend.Pending && pend.Pending >= 0 {
		fromPending = pend.Pending
	}
	entries := []LedgerEntry{
		{Kind: AcctPaymentClearing, Credit: rf.Amount},
		{Kind: AcctPlatformRevenue, Debit: platformDebit},
		{Kind: AcctOrgPending, OrgID: ev.OrganizerID, Debit: fromPending},
		{Kind: AcctOrgAvailable, OrgID: ev.OrganizerID, Debit: orgDebit - fromPending},
	}
	if orgCreditBack > 0 {
		entries = append(entries, LedgerEntry{Kind: AcctOrgPending, OrgID: ev.OrganizerID, Credit: orgCreditBack})
	}
	// Dr clearing? Uang keluar dari kliring: Cr payment_clearing. Seimbangkan: total debit = rf.Amount + orgCreditBack.
	// Debit = platformDebit + orgDebit ; Kredit = rf.Amount + orgCreditBack ; platformDebit + orgDebit = (feeBack+orgCreditBack)+(rf.Amount-feeBack) ✓
	_, err := r.s.Ledger.Post(ctx, q, "refund", rf.ID, "refund pesanan "+ord.ID, entries)
	return err
}

// ---------- pembatalan event massal ----------

// CancelEventRefunds membuat refund penuh untuk SEMUA order lunas event batal (idempoten; dipanggil lifecycle).
// Mengembalikan jumlah refund baru.
func (r *RefundService) CancelEventRefunds(ctx context.Context, eventID string) (int, error) {
	total := 0
	for {
		var created []string
		err := r.s.InTx(ctx, func(q *sqlcgen.Queries) error {
			ids, err := q.PaidOrdersWithoutRefund(ctx, sqlcgen.PaidOrdersWithoutRefundParams{EventID: eventID, Limit: 200})
			if err != nil {
				return err
			}
			for _, oid := range ids {
				ord, err := q.GetOrderForUpdate(ctx, oid)
				if err != nil {
					return err
				}
				if ord.Status != "paid" || ord.Total <= 0 {
					continue
				}
				pay, err := q.SettledPaymentForOrder(ctx, oid)
				if err != nil {
					continue
				}
				rf, err := r.insertTx(ctx, q, ord, pay, ord.Total, "acara dibatalkan oleh penyelenggara", "organizer_cancel", "order")
				if err != nil {
					return err
				}
				created = append(created, rf.ID)
			}
			return nil
		})
		if err != nil {
			return total, err
		}
		for _, id := range created {
			_ = r.s.Jobs.Enqueue(ctx, jobRefundProcess, map[string]string{"id": id}, jobs.MaxRetry(maxRefundAttempts), jobs.Unique("refund:"+id, time.Minute))
		}
		total += len(created)
		if len(created) < 200 {
			return total, nil
		}
	}
}

// ProcessOpen (worker): proses ulang refund yang tertahan.
func (r *RefundService) ProcessOpen(ctx context.Context) (int, error) {
	rows, err := r.s.Q.ListFinanceRefunds(ctx, sqlcgen.ListFinanceRefundsParams{Status: strPtr("approved"), Method: strPtr("gateway"), Lim: 200})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, x := range rows {
		if err := r.Process(ctx, x.ID); err == nil {
			n++
		}
	}
	return n, nil
}

var _ = notify.FormatIDR

// EscalateOverdue (scheduler): refund jalur manual yang rekeningnya tak kunjung diisi melewati tenggat → pengecualian rekonsiliasi untuk finance/support
// + pengingat ke pembeli. Dana tidak hilang; idempoten (satu pengecualian terbuka per refund).
func (r *RefundService) EscalateOverdue(ctx context.Context) (int, error) {
	rows, err := r.s.Q.OverdueAccountRefunds(ctx, ptr(r.s.now()))
	if err != nil {
		return 0, err
	}
	n := 0
	for _, x := range rows {
		pid := deref(x.PaymentID)
		if pid != "" {
			if open, _ := r.s.Q.OpenReconExists(ctx, sqlcgen.OpenReconExistsParams{Kind: "refund_account_overdue", PaymentID: &pid}); open {
				continue
			}
		}
		oid := x.OrderID
		_ = r.s.Q.InsertReconException(ctx, sqlcgen.InsertReconExceptionParams{ID: id.New(), Kind: "refund_account_overdue", PaymentID: strPtr(pid), OrderID: &oid,
			Detail: mustJSON(map[string]any{"refund_id": x.ID, "amount": x.Amount, "due_at": x.DueAt})})
		ord, err := r.s.Q.GetOrder(ctx, x.OrderID)
		if err == nil {
			ev, _ := r.s.Q.GetEvent(ctx, ord.EventID)
			r.s.Notifier.Send(ctx, NotifyReq{UserID: x.UserID, Template: "refund_account_needed", Data: map[string]any{
				"name": "", "event_title": ev.Title, "amount": notify.FormatIDR(x.Amount), "link": r.s.Cfg.PublicWebURL + "/refund", "deadline": "SEGERA — melewati tenggat; dana kamu tetap aman"}})
		}
		n++
	}
	return n, nil
}
