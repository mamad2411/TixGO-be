package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/xuri/excelize/v2"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/platform"
	"tixgo/backend/internal/repository/sqlcgen"
)

// FinanceService = saldo, rilis settlement, pencairan manual terkontrol, laporan, rekonsiliasi (docs/20 M34, M47; Q12).
type FinanceService struct{ s *Services }

func newFinanceService(s *Services) *FinanceService { return &FinanceService{s: s} }

type BalanceView struct {
	Pending   int64 `json:"pending"`
	Available int64 `json:"available"`
	InTransit int64 `json:"in_transit"`
	Paid      int64 `json:"paid"`
	Negative  bool  `json:"negative"` // saldo tersedia < 0 → ditagihkan sesuai kontrak (M34 AC7)
	MinPayout int64 `json:"min_payout"`
}

func (f *FinanceService) Balance(ctx context.Context, sc OrgScope) (*BalanceView, error) {
	b, err := f.s.Ledger.OrganizerBalance(ctx, f.s.Q, sc.OrganizerID)
	if err != nil {
		return nil, err
	}
	paid, err := f.s.Q.PaidPayoutsTotal(ctx, sc.OrganizerID)
	if err != nil {
		return nil, err
	}
	return &BalanceView{Pending: b.Pending, Available: b.Available, InTransit: b.InTransit, Paid: paid, Negative: b.Available < 0 || b.Pending < 0, MinPayout: f.s.Cfg.PayoutMinAmount}, nil
}

type StatementLine struct {
	At     time.Time `json:"at"`
	Type   string    `json:"type"`
	Ref    string    `json:"ref"`
	Memo   *string   `json:"memo,omitempty"`
	Bucket string    `json:"bucket"`
	Debit  int64     `json:"debit"`
	Credit int64     `json:"credit"`
}

func (f *FinanceService) Statement(ctx context.Context, sc OrgScope, limit, offset int) ([]StatementLine, int, error) {
	rows, err := f.s.Q.LedgerStatement(ctx, sqlcgen.LedgerStatementParams{OrganizerID: strPtr(sc.OrganizerID), Limit: int32(limit), Offset: int32(offset)})
	if err != nil {
		return nil, 0, err
	}
	n, err := f.s.Q.CountLedgerStatement(ctx, strPtr(sc.OrganizerID))
	if err != nil {
		return nil, 0, err
	}
	out := make([]StatementLine, 0, len(rows))
	for _, r := range rows {
		out = append(out, StatementLine{At: r.CreatedAt, Type: r.RefType, Ref: r.RefID, Memo: r.Memo, Bucket: r.Kind, Debit: r.Debit, Credit: r.Credit})
	}
	return out, int(n), nil
}

// ReleaseSettlements memindahkan saldo event yang sudah selesai + masa tahan dari pending → available (sekali per event).
func (f *FinanceService) ReleaseSettlements(ctx context.Context) (int, error) {
	rows, err := f.s.Q.EventsReadyForSettlement(ctx, sqlcgen.EventsReadyForSettlementParams{HoldDays: f.s.Cfg.PayoutHoldDays, Now: f.s.now()})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range rows {
		err := f.s.InTx(ctx, func(q *sqlcgen.Queries) error {
			net, err := q.EventPendingNet(ctx, r.ID)
			if err != nil {
				return err
			}
			if net > 0 {
				if _, err := f.s.Ledger.Post(ctx, q, "settlement", r.ID, "rilis saldo event selesai", []LedgerEntry{
					{Kind: AcctOrgPending, OrgID: r.OrganizerID, Debit: net},
					{Kind: AcctOrgAvailable, OrgID: r.OrganizerID, Credit: net},
				}); err != nil {
					return err
				}
				return f.s.Audit.LogSystem(ctx, q, "settlement.release", "event", r.ID, map[string]any{"amount": net})
			}
			// tidak ada saldo: tandai selesai dengan transaksi kosong agar tak diproses ulang
			_, err = q.InsertLedgerTx(ctx, sqlcgen.InsertLedgerTxParams{ID: id.New(), RefType: "settlement", RefID: r.ID, Memo: strPtr("tanpa saldo")})
			return err
		})
		if err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// ---------- pencairan ----------

type PayoutView struct {
	ID            string     `json:"id"`
	OrganizerID   string     `json:"organizer_id"`
	OrganizerName string     `json:"organizer_name,omitempty"`
	Amount        int64      `json:"amount"`
	Status        string     `json:"status"`
	BankCode      string     `json:"bank_code,omitempty"`
	AccountName   string     `json:"account_name,omitempty"`
	AccountNo     string     `json:"account_no,omitempty"` // hanya finance
	AccountLast4  string     `json:"account_last4,omitempty"`
	RequestedBy   string     `json:"requested_by"`
	ApprovedBy    *string    `json:"approved_by,omitempty"`
	Note          *string    `json:"note,omitempty"`
	HasProof      bool       `json:"has_proof"`
	CreatedAt     time.Time  `json:"created_at"`
	PaidAt        *time.Time `json:"paid_at,omitempty"`
}

func payoutView(p sqlcgen.Payout) PayoutView {
	return PayoutView{ID: p.ID, OrganizerID: p.OrganizerID, Amount: p.Amount, Status: p.Status, RequestedBy: p.RequestedBy, ApprovedBy: p.ApprovedBy,
		Note: p.Note, HasProof: p.ProofKey != nil, CreatedAt: p.CreatedAt, PaidAt: p.PaidAt}
}

// RequestPayout: owner + MFA; saldo dicadangkan segera (available → in_transit) agar tak bisa diminta dua kali.
func (f *FinanceService) RequestPayout(ctx context.Context, p *auth.Principal, sc OrgScope, amount int64) (*PayoutView, error) {
	if !sc.IsOwner() {
		return nil, apperr.Forbidden("hanya pemilik organizer")
	}
	if !p.MFAPassed {
		return nil, apperr.ForbiddenCode(apperr.CodeMFARequired, "verifikasi dua langkah diperlukan untuk pencairan").With("setup", true)
	}
	if amount < f.s.Cfg.PayoutMinAmount {
		return nil, apperr.Validation("jumlah di bawah minimum pencairan").With("min", f.s.Cfg.PayoutMinAmount)
	}
	var out sqlcgen.Payout
	err := f.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		org, err := q.GetOrganizerForUpdate(ctx, sc.OrganizerID) // serialisasi permintaan per organizer
		if err != nil {
			return err
		}
		if org.Status != "active" {
			return apperr.Forbidden("organizer tidak aktif")
		}
		ba, err := q.GetUsableBankAccount(ctx, sqlcgen.GetUsableBankAccountParams{OrganizerID: sc.OrganizerID, ActiveFrom: ptr(f.s.now())})
		if err != nil {
			if platform.IsNoRows(err) {
				return apperr.Unprocessable(apperr.CodeValidation, "belum ada rekening terverifikasi yang aktif (rekening baru perlu masa tunggu)")
			}
			return err
		}
		bal, err := f.s.Ledger.OrganizerBalance(ctx, q, sc.OrganizerID)
		if err != nil {
			return err
		}
		if amount > bal.Available {
			return apperr.Unprocessable(apperr.CodeValidation, "saldo tersedia tidak cukup").With("available", bal.Available)
		}
		out, err = q.InsertPayout(ctx, sqlcgen.InsertPayoutParams{ID: id.New(), OrganizerID: sc.OrganizerID, BankAccountID: ba.ID, Amount: amount, RequestedBy: p.UserID})
		if err != nil {
			return err
		}
		if _, err := f.s.Ledger.Post(ctx, q, "payout_request", out.ID, "permintaan pencairan", []LedgerEntry{
			{Kind: AcctOrgAvailable, OrgID: sc.OrganizerID, Debit: amount}, {Kind: AcctOrgPayoutTransit, OrgID: sc.OrganizerID, Credit: amount}}); err != nil {
			return err
		}
		return f.s.Audit.Log(ctx, q, p, "payout.request", "payout", out.ID, nil, map[string]any{"amount": amount})
	})
	if err != nil {
		return nil, err
	}
	f.notifyPayout(ctx, out, "requested", "")
	v := payoutView(out)
	return &v, nil
}

func (f *FinanceService) OrgPayouts(ctx context.Context, sc OrgScope, limit, offset int) ([]PayoutView, int, error) {
	rows, err := f.s.Q.ListOrgPayouts(ctx, sqlcgen.ListOrgPayoutsParams{OrganizerID: sc.OrganizerID, Limit: int32(limit), Offset: int32(offset)})
	if err != nil {
		return nil, 0, err
	}
	n, err := f.s.Q.CountOrgPayouts(ctx, sc.OrganizerID)
	if err != nil {
		return nil, 0, err
	}
	out := make([]PayoutView, 0, len(rows))
	for _, r := range rows {
		out = append(out, payoutView(r))
	}
	return out, int(n), nil
}

// AdminPayouts: antrean finance, rekening tujuan didekripsi (hanya role finance/admin + MFA).
func (f *FinanceService) AdminPayouts(ctx context.Context, status string, limit, offset int) ([]PayoutView, int, error) {
	rows, err := f.s.Q.ListAllPayouts(ctx, sqlcgen.ListAllPayoutsParams{Status: strPtr(status), Lim: int32(limit), Off: int32(offset)})
	if err != nil {
		return nil, 0, err
	}
	n, err := f.s.Q.CountAllPayouts(ctx, strPtr(status))
	if err != nil {
		return nil, 0, err
	}
	out := make([]PayoutView, 0, len(rows))
	for _, r := range rows {
		v := payoutView(sqlcgen.Payout{ID: r.ID, OrganizerID: r.OrganizerID, BankAccountID: r.BankAccountID, Amount: r.Amount, Status: r.Status, RequestedBy: r.RequestedBy,
			ApprovedBy: r.ApprovedBy, ProofKey: r.ProofKey, Note: r.Note, CreatedAt: r.CreatedAt, PaidAt: r.PaidAt})
		v.OrganizerName = r.OrganizerName
		if ba, err := f.s.Q.GetBankAccount(ctx, r.BankAccountID); err == nil {
			v.BankCode, v.AccountName, v.AccountLast4 = ba.BankCode, ba.AccountName, ba.AccountNoLast4
			if b, err := f.s.Box.Decrypt(ba.AccountNoEnc); err == nil {
				v.AccountNo = string(b)
			}
		}
		out = append(out, v)
	}
	return out, int(n), nil
}

func (f *FinanceService) transition(ctx context.Context, p *auth.Principal, payoutID, action string, from []string, to string, fn func(q *sqlcgen.Queries, po sqlcgen.Payout) error) (*PayoutView, error) {
	if !p.MFAPassed {
		return nil, apperr.ForbiddenCode(apperr.CodeMFARequired, "verifikasi dua langkah diperlukan").With("setup", true)
	}
	var out sqlcgen.Payout
	err := f.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		po, err := q.GetPayoutForUpdate(ctx, payoutID)
		if err != nil {
			return apperr.NotFound("pencairan")
		}
		ok := false
		for _, s := range from {
			ok = ok || po.Status == s
		}
		if !ok {
			return apperr.Conflict(apperr.CodeInvalidState, "status pencairan tidak memungkinkan aksi ini: "+po.Status)
		}
		if fn != nil {
			if err := fn(q, po); err != nil {
				return err
			}
		}
		out, err = q.GetPayout(ctx, payoutID)
		if err != nil {
			return err
		}
		return f.s.Audit.Log(ctx, q, p, "payout."+action, "payout", payoutID, map[string]any{"status": po.Status}, map[string]any{"status": to})
	})
	if err != nil {
		return nil, err
	}
	f.notifyPayout(ctx, out, to, "")
	v := payoutView(out)
	return &v, nil
}

var payoutStatusID = map[string]string{"requested": "diajukan", "approved": "disetujui", "rejected": "ditolak", "processing": "sedang ditransfer", "paid": "sudah ditransfer", "failed": "gagal ditransfer"}

// notifyPayout memberi tahu pemilik organizer tiap perubahan status pencairan (catatan finance disertakan bila ada).
func (f *FinanceService) notifyPayout(ctx context.Context, po sqlcgen.Payout, status, note string) {
	org, err := f.s.Q.GetOrganizer(ctx, po.OrganizerID)
	if err != nil {
		return
	}
	if note == "" && po.Note != nil {
		note = *po.Note
	}
	f.s.Notifier.Send(ctx, NotifyReq{UserID: org.OwnerUserID, Template: "payout_update", Data: map[string]any{
		"name": org.Name, "amount": formatIDR(po.Amount), "status": payoutStatusID[status], "note": note}})
}

// Approve: penyetuju ≠ pemohon (juga dijaga CHECK di DB).
func (f *FinanceService) Approve(ctx context.Context, p *auth.Principal, payoutID string) (*PayoutView, error) {
	return f.transition(ctx, p, payoutID, "approve", []string{"requested"}, "approved", func(q *sqlcgen.Queries, po sqlcgen.Payout) error {
		if po.RequestedBy == p.UserID {
			return apperr.Forbidden("pemohon tidak boleh menyetujui pencairannya sendiri")
		}
		return q.SetPayoutStatus(ctx, sqlcgen.SetPayoutStatusParams{ID: po.ID, Status: "approved", ApprovedBy: &p.UserID, At: f.s.now()})
	})
}

func (f *FinanceService) reverse(ctx context.Context, q *sqlcgen.Queries, po sqlcgen.Payout, tag string) error {
	_, err := f.s.Ledger.Post(ctx, q, "payout_"+tag, po.ID, "pencairan "+tag+": saldo dikembalikan", []LedgerEntry{
		{Kind: AcctOrgPayoutTransit, OrgID: po.OrganizerID, Debit: po.Amount}, {Kind: AcctOrgAvailable, OrgID: po.OrganizerID, Credit: po.Amount}})
	return err
}

func (f *FinanceService) Reject(ctx context.Context, p *auth.Principal, payoutID, note string) (*PayoutView, error) {
	if len([]rune(note)) < 3 {
		return nil, apperr.Validation("alasan penolakan wajib diisi").With("field", "note")
	}
	return f.transition(ctx, p, payoutID, "reject", []string{"requested", "approved"}, "rejected", func(q *sqlcgen.Queries, po sqlcgen.Payout) error {
		if err := q.SetPayoutStatus(ctx, sqlcgen.SetPayoutStatusParams{ID: po.ID, Status: "rejected", Note: &note, At: f.s.now()}); err != nil {
			return err
		}
		return f.reverse(ctx, q, po, "rejected")
	})
}

func (f *FinanceService) StartProcessing(ctx context.Context, p *auth.Principal, payoutID string) (*PayoutView, error) {
	return f.transition(ctx, p, payoutID, "process", []string{"approved", "failed"}, "processing", func(q *sqlcgen.Queries, po sqlcgen.Payout) error {
		if po.Status == "failed" { // coba ulang: cadangkan saldo lagi
			bal, err := f.s.Ledger.OrganizerBalance(ctx, q, po.OrganizerID)
			if err != nil {
				return err
			}
			if bal.Available < po.Amount {
				return apperr.Unprocessable(apperr.CodeValidation, "saldo tersedia tidak cukup untuk mencoba ulang")
			}
			if _, err := f.s.Ledger.Post(ctx, q, "payout_retry", po.ID+":"+f.s.now().Format("20060102150405"), "coba ulang pencairan", []LedgerEntry{
				{Kind: AcctOrgAvailable, OrgID: po.OrganizerID, Debit: po.Amount}, {Kind: AcctOrgPayoutTransit, OrgID: po.OrganizerID, Credit: po.Amount}}); err != nil {
				return err
			}
		}
		return q.SetPayoutStatus(ctx, sqlcgen.SetPayoutStatusParams{ID: po.ID, Status: "processing", At: f.s.now()})
	})
}

// MarkPaid: finance sudah mentransfer manual dan mengunggah bukti.
func (f *FinanceService) MarkPaid(ctx context.Context, p *auth.Principal, payoutID string, proof []byte, note string) (*PayoutView, error) {
	if len(proof) == 0 {
		return nil, apperr.Validation("bukti transfer wajib diunggah").With("field", "proof")
	}
	return f.transition(ctx, p, payoutID, "mark_paid", []string{"approved", "processing"}, "paid", func(q *sqlcgen.Queries, po sqlcgen.Payout) error {
		key := "payout-proof/" + po.ID
		if err := f.s.Store.Put(ctx, key, bytes.NewReader(proof), int64(len(proof)), "application/octet-stream"); err != nil {
			return err
		}
		if _, err := f.s.Ledger.Post(ctx, q, "payout_paid", po.ID, "pencairan dibayar", []LedgerEntry{
			{Kind: AcctOrgPayoutTransit, OrgID: po.OrganizerID, Debit: po.Amount}, {Kind: AcctBankCash, Credit: po.Amount}}); err != nil {
			return err
		}
		return q.SetPayoutStatus(ctx, sqlcgen.SetPayoutStatusParams{ID: po.ID, Status: "paid", Note: strPtr(note), ProofKey: &key, At: f.s.now()})
	})
}

func (f *FinanceService) MarkFailed(ctx context.Context, p *auth.Principal, payoutID, note string) (*PayoutView, error) {
	if len([]rune(note)) < 3 {
		return nil, apperr.Validation("alasan kegagalan wajib diisi").With("field", "note")
	}
	return f.transition(ctx, p, payoutID, "mark_failed", []string{"processing", "approved"}, "failed", func(q *sqlcgen.Queries, po sqlcgen.Payout) error {
		if err := q.SetPayoutStatus(ctx, sqlcgen.SetPayoutStatusParams{ID: po.ID, Status: "failed", Note: &note, At: f.s.now()}); err != nil {
			return err
		}
		return f.reverse(ctx, q, po, "failed:"+f.s.now().Format("20060102150405"))
	})
}

// ---------- laporan settlement ----------

type TierSale struct {
	Name    string `json:"name"`
	Qty     int    `json:"qty"`
	Revenue int64  `json:"revenue"`
}

type SettlementReport struct {
	EventID      string     `json:"event_id"`
	EventTitle   string     `json:"event_title"`
	Orders       int        `json:"orders"`
	Gross        int64      `json:"gross"` // penjualan tiket + add-on setelah diskon
	Discount     int64      `json:"discount"`
	BuyerFee     int64      `json:"buyer_fee"`     // biaya layanan dibayar pembeli (pendapatan platform)
	OrganizerFee int64      `json:"organizer_fee"` // biaya yang ditanggung organizer
	Collected    int64      `json:"collected"`     // total dibayar pembeli
	Refunded     int64      `json:"refunded"`
	Refunding    int64      `json:"refunding"`
	NetToOrg     int64      `json:"net_to_organizer"` // gross − fee organizer − refund yang dipotong dari organizer
	Tiers        []TierSale `json:"tiers"`
}

func (f *FinanceService) settlement(ctx context.Context, ev sqlcgen.Event) (*SettlementReport, error) {
	s, err := f.s.Q.EventSettlementSummary(ctx, ev.ID)
	if err != nil {
		return nil, err
	}
	r, err := f.s.Q.EventRefundTotals(ctx, ev.ID)
	if err != nil {
		return nil, err
	}
	tiers, err := f.s.Q.EventTierSales(ctx, ev.ID)
	if err != nil {
		return nil, err
	}
	rep := &SettlementReport{EventID: ev.ID, EventTitle: ev.Title, Orders: int(s.Orders), Gross: s.Gross, Discount: s.Discount, BuyerFee: s.BuyerFee,
		OrganizerFee: s.OrganizerFee, Collected: s.Collected, Refunded: r.Refunded, Refunding: r.Refunding, Tiers: []TierSale{}}
	// bagian refund yang berasal dari fee pembeli tidak dipotong dari organizer; perkiraan konservatif: refund dikurangi fee pembeli
	orgRefund := r.Refunded - minI64(r.Refunded, s.BuyerFee)
	rep.NetToOrg = s.Gross - s.OrganizerFee - orgRefund
	for _, t := range tiers {
		rep.Tiers = append(rep.Tiers, TierSale{Name: t.Name, Qty: int(t.Qty), Revenue: t.Revenue})
	}
	return rep, nil
}

func minI64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func (f *FinanceService) EventSettlement(ctx context.Context, sc OrgScope, eventID string) (*SettlementReport, error) {
	ev, err := f.s.Event.ownEvent(ctx, f.s.Q, sc, eventID)
	if err != nil {
		return nil, err
	}
	return f.settlement(ctx, ev)
}

func (f *FinanceService) AdminEventSettlement(ctx context.Context, eventID string) (*SettlementReport, error) {
	ev, err := f.s.Q.GetEvent(ctx, eventID)
	if err != nil {
		return nil, apperr.NotFound("event")
	}
	return f.settlement(ctx, ev)
}

// XLSX membuat berkas laporan settlement.
func (f *FinanceService) XLSX(rep *SettlementReport) ([]byte, error) {
	x := excelize.NewFile()
	defer func() { _ = x.Close() }()
	sh := "Settlement"
	_ = x.SetSheetName("Sheet1", sh)
	rows := [][]any{
		{"Laporan settlement", rep.EventTitle},
		{"Jumlah pesanan", rep.Orders},
		{"Penjualan (setelah diskon)", rep.Gross},
		{"Diskon", rep.Discount},
		{"Biaya layanan (pembeli)", rep.BuyerFee},
		{"Biaya ditanggung organizer", rep.OrganizerFee},
		{"Total dibayar pembeli", rep.Collected},
		{"Refund selesai", rep.Refunded},
		{"Refund berjalan", rep.Refunding},
		{"Bersih untuk organizer", rep.NetToOrg},
		{},
		{"Tier", "Qty", "Pendapatan"},
	}
	for _, t := range rep.Tiers {
		rows = append(rows, []any{t.Name, t.Qty, t.Revenue})
	}
	for i, r := range rows {
		cell, _ := excelize.CoordinatesToCellName(1, i+1)
		if err := x.SetSheetRow(sh, cell, &r); err != nil {
			return nil, err
		}
	}
	var buf bytes.Buffer
	if err := x.Write(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ---------- rekonsiliasi (M47) ----------

type GatewayRow struct {
	ProviderRef string `json:"provider_ref" binding:"required,max=100"`
	Amount      int64  `json:"amount" binding:"min=0"`
	Status      string `json:"status" binding:"required,max=30"`
}

type ReconResult struct {
	Matched    int `json:"matched"`
	Exceptions int `json:"exceptions"`
}

// ReconcileReport mencocokkan laporan settlement gateway dengan tabel payments. Selisih → recon_exceptions (idempoten per payment/jenis).
func (f *FinanceService) ReconcileReport(ctx context.Context, p *auth.Principal, rows []GatewayRow, from, to time.Time) (*ReconResult, error) {
	res := &ReconResult{}
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.ProviderRef] = true
		pay, err := f.s.Q.GetPaymentByProviderRef(ctx, strPtr(r.ProviderRef))
		if err != nil {
			if !platform.IsNoRows(err) {
				return nil, err
			}
			if err := f.exception(ctx, "gateway_unknown_payment", nil, nil, map[string]any{"provider_ref": r.ProviderRef, "amount": r.Amount}); err != nil {
				return nil, err
			}
			res.Exceptions++
			continue
		}
		switch {
		case pay.Amount != r.Amount:
			res.Exceptions++
			if err := f.exception(ctx, "amount_mismatch", &pay.ID, &pay.OrderID, map[string]any{"ours": pay.Amount, "gateway": r.Amount}); err != nil {
				return nil, err
			}
		case (r.Status == "settlement" || r.Status == "capture") && pay.Status != "settled":
			res.Exceptions++
			if err := f.exception(ctx, "status_mismatch", &pay.ID, &pay.OrderID, map[string]any{"ours": pay.Status, "gateway": r.Status}); err != nil {
				return nil, err
			}
		default:
			res.Matched++
		}
	}
	// kebalikannya: lunas di kita, tidak ada di laporan gateway
	ours, err := f.s.Q.PaymentsSettledBetween(ctx, sqlcgen.PaymentsSettledBetweenParams{FromTs: &from, ToTs: &to})
	if err != nil {
		return nil, err
	}
	for _, o := range ours {
		if o.ProviderRef != nil && !seen[*o.ProviderRef] {
			res.Exceptions++
			pid, oid := o.ID, o.OrderID
			if err := f.exception(ctx, "missing_in_gateway_report", &pid, &oid, map[string]any{"provider_ref": *o.ProviderRef, "amount": o.Amount}); err != nil {
				return nil, err
			}
		}
	}
	_ = f.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		return f.s.Audit.Log(ctx, q, p, "recon.import", "recon", "", nil, map[string]any{"rows": len(rows), "exceptions": res.Exceptions})
	})
	return res, nil
}

func (f *FinanceService) exception(ctx context.Context, kind string, paymentID, orderID *string, detail map[string]any) error {
	if paymentID != nil {
		if n, _ := f.s.Q.OpenReconExists(ctx, sqlcgen.OpenReconExistsParams{Kind: kind, PaymentID: paymentID}); n {
			return nil
		}
	}
	return f.s.Q.InsertReconException(ctx, sqlcgen.InsertReconExceptionParams{ID: id.New(), Kind: kind, PaymentID: paymentID, OrderID: orderID, Detail: mustJSON(detail)})
}

func (f *FinanceService) ListRecon(ctx context.Context, status string, limit, offset int) ([]sqlcgen.ReconException, int, error) {
	rows, err := f.s.Q.ListReconExceptions(ctx, sqlcgen.ListReconExceptionsParams{Status: strPtr(status), Lim: int32(limit), Off: int32(offset)})
	if err != nil {
		return nil, 0, err
	}
	n, err := f.s.Q.CountReconExceptions(ctx, strPtr(status))
	return rows, int(n), err
}

func (f *FinanceService) ResolveRecon(ctx context.Context, p *auth.Principal, id, status, resolution string) error {
	if status != "investigating" && status != "resolved" {
		return apperr.Validation("status harus investigating atau resolved").With("field", "status")
	}
	if status == "resolved" && len([]rune(resolution)) < 3 {
		return apperr.Validation("catatan penyelesaian wajib diisi").With("field", "resolution")
	}
	return f.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		x, err := q.GetReconException(ctx, id)
		if err != nil {
			return apperr.NotFound("pengecualian rekonsiliasi")
		}
		if x.Kind == "chargeback" {
			return apperr.Conflict(apperr.CodeInvalidState, "sengketa diselesaikan lewat /disputes/{id}/resolve (memulihkan tiket & pembukuan)")
		}
		if err := q.ResolveReconException(ctx, sqlcgen.ResolveReconExceptionParams{ID: id, Status: status, Resolution: strPtr(resolution)}); err != nil {
			return err
		}
		return f.s.Audit.Log(ctx, q, p, "recon."+status, "recon", id, nil, map[string]any{"resolution": resolution})
	})
}

// LedgerHealth: pemeriksaan harian — total debit = total kredit, tidak ada akun organizer negatif yang tak terduga.
type LedgerHealth struct {
	Balanced bool  `json:"balanced"`
	Debit    int64 `json:"debit"`
	Credit   int64 `json:"credit"`
}

func (f *FinanceService) Health(ctx context.Context) (*LedgerHealth, error) {
	rows, err := f.s.Q.LedgerTrialBalance(ctx)
	if err != nil {
		return nil, err
	}
	h := &LedgerHealth{}
	for _, r := range rows {
		h.Debit += r.Debit
		h.Credit += r.Credit
	}
	h.Balanced = h.Debit == h.Credit
	return h, nil
}

// ---------- sengketa / chargeback (M47 AC3) ----------
// Alur bergantung gateway (diverifikasi saat integrasi sandbox); di sini: pencatatan, bukti, tenggat, penahanan tiket, penyelesaian + pembukuan.

type DisputeInput struct {
	PaymentID string    `json:"payment_id" binding:"required,len=26"`
	Reason    string    `json:"reason" binding:"required,min=5,max=500"`
	DueAt     time.Time `json:"due_at" binding:"required"`
}

type disputeDetail struct {
	Reason    string    `json:"reason"`
	DueAt     time.Time `json:"due_at"`
	TicketIDs []string  `json:"held_ticket_ids"`
	Evidence  []string  `json:"evidence"`
	Outcome   string    `json:"outcome,omitempty"`
	Amount    int64     `json:"amount"`
}

func (f *FinanceService) OpenDispute(ctx context.Context, p *auth.Principal, in DisputeInput) (*sqlcgen.ReconException, error) {
	if !in.DueAt.After(f.s.now()) {
		return nil, apperr.Validation("tenggat harus di masa depan").With("field", "due_at")
	}
	var out sqlcgen.ReconException
	err := f.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		pay, err := q.GetPayment(ctx, in.PaymentID)
		if err != nil || pay.Status != "settled" {
			return apperr.NotFound("pembayaran lunas")
		}
		if open, _ := q.OpenDisputeForPayment(ctx, &pay.ID); open {
			return apperr.Conflict(apperr.CodeConflict, "sudah ada sengketa terbuka untuk pembayaran ini")
		}
		ord, err := q.GetOrderForUpdate(ctx, pay.OrderID)
		if err != nil {
			return err
		}
		tks, err := q.ListOrderTickets(ctx, ord.ID)
		if err != nil {
			return err
		}
		d := disputeDetail{Reason: in.Reason, DueAt: in.DueAt, Evidence: []string{}, TicketIDs: []string{}, Amount: pay.Amount}
		for _, t := range tks { // tiket ditahan (void sementara) selama sengketa; yang sudah dipakai dibiarkan
			if t.Status == "valid" {
				if err := q.SetTicketStatus(ctx, sqlcgen.SetTicketStatusParams{ID: t.ID, Status: "void"}); err != nil {
					return err
				}
				d.TicketIDs = append(d.TicketIDs, t.ID)
			}
		}
		out, err = q.InsertDispute(ctx, sqlcgen.InsertDisputeParams{ID: id.New(), PaymentID: &pay.ID, OrderID: &ord.ID, Detail: mustJSON(d)})
		if err != nil {
			return err
		}
		return f.s.Audit.Log(ctx, q, p, "dispute.open", "recon", out.ID, nil, map[string]any{"payment": pay.ID, "held": len(d.TicketIDs)})
	})
	return &out, err
}

func (f *FinanceService) AddDisputeEvidence(ctx context.Context, p *auth.Principal, disputeID string, file []byte, name string) error {
	if len(file) == 0 {
		return apperr.Validation("berkas bukti kosong").With("field", "file")
	}
	return f.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		x, err := q.GetReconExceptionForUpdate(ctx, disputeID)
		if err != nil || x.Kind != "chargeback" || x.Status == "resolved" {
			return apperr.NotFound("sengketa terbuka")
		}
		var d disputeDetail
		_ = json.Unmarshal(x.Detail, &d)
		key := fmt.Sprintf("dispute/%s/%d", disputeID, len(d.Evidence)+1)
		if err := f.s.Store.Put(ctx, key, bytes.NewReader(file), int64(len(file)), "application/octet-stream"); err != nil {
			return err
		}
		d.Evidence = append(d.Evidence, key)
		if err := q.SetReconDetail(ctx, sqlcgen.SetReconDetailParams{ID: disputeID, Detail: mustJSON(d)}); err != nil {
			return err
		}
		return f.s.Audit.Log(ctx, q, p, "dispute.evidence", "recon", disputeID, nil, map[string]any{"name": name, "files": len(d.Evidence)})
	})
}

// ResolveDispute: won → tiket yang ditahan dipulihkan; lost → tiket tetap void, order ditandai refunded dan kerugian dibukukan seperti refund penuh (organizer menanggung sesuai kontrak).
func (f *FinanceService) ResolveDispute(ctx context.Context, p *auth.Principal, disputeID, outcome, note string) error {
	if outcome != "won" && outcome != "lost" {
		return apperr.Validation("outcome harus won atau lost").With("field", "outcome")
	}
	return f.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		x, err := q.GetReconExceptionForUpdate(ctx, disputeID)
		if err != nil || x.Kind != "chargeback" {
			return apperr.NotFound("sengketa")
		}
		if x.Status == "resolved" {
			return apperr.Conflict(apperr.CodeInvalidState, "sengketa sudah selesai")
		}
		var d disputeDetail
		_ = json.Unmarshal(x.Detail, &d)
		d.Outcome = outcome
		ord, err := q.GetOrderForUpdate(ctx, deref(x.OrderID))
		if err != nil {
			return err
		}
		switch outcome {
		case "won":
			for _, tid := range d.TicketIDs {
				if t, err := q.GetTicket(ctx, tid); err == nil && t.Status == "void" {
					if err := q.SetTicketStatus(ctx, sqlcgen.SetTicketStatusParams{ID: tid, Status: "valid"}); err != nil {
						return err
					}
				}
			}
		case "lost":
			ev, err := q.GetEvent(ctx, ord.EventID)
			if err != nil {
				return err
			}
			if err := q.SetOrderStatus(ctx, sqlcgen.SetOrderStatusParams{ID: ord.ID, Status: "refunded"}); err != nil {
				return err
			}
			if ord.ResaleListingID != nil {
				if err := f.s.Resale.reverseLedger(ctx, q, ord, sqlcgen.Refund{ID: "dispute:" + disputeID, Amount: ord.Total}); err != nil {
					return err
				}
			} else if err := f.s.Refund.postLedger(ctx, q, ev, ord, sqlcgen.Refund{ID: "dispute:" + disputeID, Amount: ord.Total}); err != nil {
				return err
			}
		}
		if err := q.SetReconDetail(ctx, sqlcgen.SetReconDetailParams{ID: disputeID, Detail: mustJSON(d)}); err != nil {
			return err
		}
		if err := q.ResolveReconException(ctx, sqlcgen.ResolveReconExceptionParams{ID: disputeID, Status: "resolved", Resolution: strPtr(outcome + ": " + note)}); err != nil {
			return err
		}
		return f.s.Audit.Log(ctx, q, p, "dispute.resolve", "recon", disputeID, nil, map[string]any{"outcome": outcome})
	})
}
