package service

import (
	"context"
	"fmt"

	"tixgo/backend/internal/id"
	"tixgo/backend/internal/repository/sqlcgen"
)

// LedgerService = pembukuan berganda sederhana (docs/20 M34 AC1): setiap kejadian uang = satu transaksi seimbang,
// idempoten lewat (ref_type, ref_id), ditulis dalam transaksi DB yang sama dengan perubahan status order/refund/payout.
// DB menjaga keseimbangan lewat constraint trigger (migrasi 0004).
type LedgerService struct{ s *Services }

func newLedgerService(s *Services) *LedgerService { return &LedgerService{s: s} }

// Jenis akun
const (
	AcctPlatformRevenue  = "platform_revenue"
	AcctPaymentClearing  = "payment_clearing"
	AcctBankCash         = "bank_cash"
	AcctOrgPending       = "organizer_pending"
	AcctOrgAvailable     = "organizer_available"
	AcctOrgPayoutTransit = "organizer_payout_in_transit"
	AcctOrgPaid          = "organizer_paid"
	AcctAdjustments      = "adjustments"
)

type LedgerEntry struct {
	Kind   string
	OrgID  string // kosong untuk akun platform
	Debit  int64
	Credit int64
}

func (l *LedgerService) accountID(ctx context.Context, q *sqlcgen.Queries, kind, orgID string) (string, error) {
	if err := q.UpsertLedgerAccount(ctx, sqlcgen.UpsertLedgerAccountParams{ID: id.New(), Kind: kind, OrganizerID: strPtr(orgID)}); err != nil {
		return "", err
	}
	a, err := q.GetLedgerAccount(ctx, sqlcgen.GetLedgerAccountParams{Kind: kind, OrganizerID: strPtr(orgID)})
	if err != nil {
		return "", err
	}
	return a.ID, nil
}

// Post menulis transaksi. posted=false bila ref sudah pernah diposting (idempoten, bukan error).
func (l *LedgerService) Post(ctx context.Context, q *sqlcgen.Queries, refType, refID, memo string, entries []LedgerEntry) (bool, error) {
	var d, c int64
	for _, e := range entries {
		if e.Debit < 0 || e.Credit < 0 || (e.Debit > 0 && e.Credit > 0) {
			return false, fmt.Errorf("ledger: entri tidak valid: %+v", e)
		}
		d, c = d+e.Debit, c+e.Credit
	}
	if d != c {
		return false, fmt.Errorf("ledger tidak seimbang (%s/%s): debit=%d kredit=%d", refType, refID, d, c)
	}
	if d == 0 {
		return false, nil
	}
	txID := id.New()
	n, err := q.InsertLedgerTx(ctx, sqlcgen.InsertLedgerTxParams{ID: txID, RefType: refType, RefID: refID, Memo: strPtr(memo)})
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	for _, e := range entries {
		if e.Debit == 0 && e.Credit == 0 {
			continue
		}
		acc, err := l.accountID(ctx, q, e.Kind, e.OrgID)
		if err != nil {
			return false, err
		}
		if err := q.InsertLedgerEntry(ctx, sqlcgen.InsertLedgerEntryParams{TxID: txID, AccountID: acc, Debit: e.Debit, Credit: e.Credit}); err != nil {
			return false, err
		}
	}
	return true, nil
}

type OrgBalance struct {
	Pending   int64 `json:"pending"`
	Available int64 `json:"available"`
	InTransit int64 `json:"in_transit"`
	Paid      int64 `json:"paid"`
}

func (l *LedgerService) OrganizerBalance(ctx context.Context, q *sqlcgen.Queries, orgID string) (OrgBalance, error) {
	rows, err := q.OrganizerBalances(ctx, strPtr(orgID))
	if err != nil {
		return OrgBalance{}, err
	}
	var b OrgBalance
	for _, r := range rows {
		switch r.Kind {
		case AcctOrgPending:
			b.Pending = r.Balance
		case AcctOrgAvailable:
			b.Available = r.Balance
		case AcctOrgPayoutTransit:
			b.InTransit = r.Balance
		case AcctOrgPaid:
			b.Paid = r.Balance
		}
	}
	return b, nil
}

// PostOrderPaid: Dr kliring pembayaran; Cr pendapatan platform (fee) + saldo pending organizer (sisanya).
func (l *LedgerService) PostOrderPaid(ctx context.Context, q *sqlcgen.Queries, o sqlcgen.Order, orgID string) error {
	organizerGross := o.Subtotal - o.Discount + o.AddonsTotal
	orgFee := o.PlatformFee - o.Fee // fee platform yang ditanggung organizer (bila fee_payer=organizer)
	if orgFee < 0 {
		orgFee = 0
	}
	entries := []LedgerEntry{
		{Kind: AcctPaymentClearing, Debit: o.Total},
		{Kind: AcctOrgPending, OrgID: orgID, Credit: organizerGross - orgFee},
		{Kind: AcctPlatformRevenue, Credit: o.Fee + orgFee},
	}
	_, err := l.Post(ctx, q, "order_paid", o.ID, "pembayaran order", entries)
	return err
}
