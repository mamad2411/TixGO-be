package payment

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// FakeServerKey dipakai Fake untuk menandatangani notifikasi uji.
const FakeServerKey = "fake-server-key"

// Fake = gateway palsu terkendali untuk tes integrasi (tanpa jaringan).
type Fake struct {
	mu       sync.Mutex
	Txs      map[string]*FakeTx
	Refunds  []RefundReq
	Expired  []string
	FailNext struct {
		Create bool
		Refund error
	}
	Clock func() time.Time
}

type FakeTx struct {
	Req         CreateTxReq
	Status      Status
	RawStatus   string
	PaymentType string
	TxID        string
	SettledAt   time.Time
}

func NewFake(clock func() time.Time) *Fake {
	if clock == nil {
		clock = time.Now
	}
	return &Fake{Txs: map[string]*FakeTx{}, Clock: clock}
}

func (f *Fake) Name() string { return "midtrans" }

func (f *Fake) CreateTransaction(_ context.Context, r CreateTxReq) (CreateTxRes, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailNext.Create {
		f.FailNext.Create = false
		return CreateTxRes{}, fmt.Errorf("%w: gagal (fake)", ErrGateway)
	}
	f.Txs[r.OrderRef] = &FakeTx{Req: r, Status: StatusPending, RawStatus: "pending", TxID: "tx-" + r.OrderRef}
	return CreateTxRes{Token: "snap-" + r.OrderRef, RedirectURL: "https://pay.fake/" + r.OrderRef}, nil
}

func (f *Fake) Status(_ context.Context, orderRef string) (TxStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	tx, ok := f.Txs[orderRef]
	if !ok {
		return TxStatus{}, ErrNotFound
	}
	return TxStatus{OrderRef: orderRef, TransactionID: tx.TxID, Status: tx.Status, RawStatus: tx.RawStatus,
		PaymentType: tx.PaymentType, GrossAmount: tx.Req.Amount, StatusCode: "200", SettledAt: tx.SettledAt}, nil
}

func (f *Fake) Refund(_ context.Context, r RefundReq) (RefundRes, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.FailNext.Refund; err != nil {
		f.FailNext.Refund = nil
		return RefundRes{}, err
	}
	if !CanRefundViaAPI(r.PaymentType, r.PaidAt, r.Now) {
		if _, ok := RefundWindow(r.PaymentType); !ok {
			return RefundRes{}, ErrRefundUnsupported
		}
		return RefundRes{}, ErrRefundWindow
	}
	f.Refunds = append(f.Refunds, r)
	if tx, ok := f.Txs[r.OrderRef]; ok {
		tx.Status, tx.RawStatus = StatusRefunded, "refund"
	}
	return RefundRes{RefundID: "rf-" + r.Key, Amount: r.Amount}, nil
}

func (f *Fake) Expire(_ context.Context, orderRef string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Expired = append(f.Expired, orderRef)
	if tx, ok := f.Txs[orderRef]; ok && tx.Status == StatusPending {
		tx.Status, tx.RawStatus = StatusExpired, "expire"
	}
	return nil
}

func (f *Fake) ParseNotification(raw []byte, _ http.Header) (Notification, error) {
	return ParseNotification(raw)
}

func (f *Fake) VerifyNotification(n Notification) bool { return VerifySignature(n, FakeServerKey) }

// Settle menandai transaksi lunas di sisi gateway dan mengembalikan body notifikasi bertanda tangan benar.
func (f *Fake) Settle(orderRef, paymentType string) []byte {
	return f.notify(orderRef, paymentType, "settlement", "")
}

// Notify menghasilkan body notifikasi untuk status mentah tertentu (expire, deny, cancel, capture, ...).
func (f *Fake) Notify(orderRef, paymentType, rawStatus, fraud string) []byte {
	return f.notify(orderRef, paymentType, rawStatus, fraud)
}

func (f *Fake) notify(orderRef, paymentType, rawStatus, fraud string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	tx := f.Txs[orderRef]
	if tx == nil {
		panic("fake: transaksi tidak ada: " + orderRef)
	}
	tx.Status = Normalize(rawStatus, fraud)
	tx.RawStatus = rawStatus
	tx.PaymentType = paymentType
	if tx.Status == StatusSettled {
		tx.SettledAt = f.Clock()
	}
	gross := strconv.FormatInt(tx.Req.Amount, 10) + ".00"
	code := "200"
	if rawStatus == "pending" {
		code = "201"
	}
	n := map[string]any{
		"order_id": orderRef, "status_code": code, "gross_amount": gross,
		"signature_key":      Signature(orderRef, code, gross, FakeServerKey),
		"transaction_status": rawStatus, "payment_type": paymentType, "transaction_id": tx.TxID,
	}
	if fraud != "" {
		n["fraud_status"] = fraud
	}
	b, _ := json.Marshal(n)
	return b
}
