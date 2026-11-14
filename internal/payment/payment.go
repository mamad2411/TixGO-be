// Package payment = antarmuka gateway pembayaran (Q1: Midtrans) + normalisasi status + jendela refund.
// Rujukan: docs/22-KEPUTUSAN-PEMBAYARAN-REFUND.md; rumus signature & kriteria sukses diverifikasi
// terhadap docs.midtrans.com (2026-10-04).
package payment

import (
	"context"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var (
	ErrRefundUnsupported = errors.New("metode bayar tidak mendukung refund via API")
	ErrRefundWindow      = errors.New("di luar jendela waktu refund gateway")
	ErrRefundRejected    = errors.New("refund ditolak gateway/bank")
	ErrNotFound          = errors.New("transaksi tidak ditemukan di gateway")
	ErrGateway           = errors.New("gateway error")
)

// Status ternormalisasi.
type Status string

const (
	StatusPending       Status = "pending"
	StatusSettled       Status = "settled"
	StatusFailed        Status = "failed"
	StatusExpired       Status = "expired"
	StatusCancelled     Status = "cancelled"
	StatusRefunded      Status = "refunded"
	StatusPartialRefund Status = "partial_refund"
	StatusChallenge     Status = "challenge"
)

type Customer struct{ Name, Email, Phone string }

type Item struct {
	ID       string
	Name     string
	Price    int64
	Quantity int
}

type CreateTxReq struct {
	OrderRef    string
	Amount      int64
	Customer    Customer
	Items       []Item
	ExpiresAt   time.Time
	Now         time.Time
	CallbackURL string
}

type CreateTxRes struct {
	Token       string
	RedirectURL string
}

// Notification = isi notifikasi HTTP Midtrans (field yang dipakai verifikasi & penentuan status).
type Notification struct {
	OrderID           string `json:"order_id"`
	StatusCode        string `json:"status_code"`
	GrossAmount       string `json:"gross_amount"`
	SignatureKey      string `json:"signature_key"`
	TransactionStatus string `json:"transaction_status"`
	FraudStatus       string `json:"fraud_status"`
	PaymentType       string `json:"payment_type"`
	TransactionID     string `json:"transaction_id"`
	Raw               []byte `json:"-"`
}

// ParseNotification mendekode body notifikasi.
func ParseNotification(raw []byte) (Notification, error) {
	var n Notification
	if err := json.Unmarshal(raw, &n); err != nil {
		return n, fmt.Errorf("notifikasi bukan JSON valid: %w", err)
	}
	n.Raw = raw
	if n.OrderID == "" || n.StatusCode == "" || n.GrossAmount == "" || n.SignatureKey == "" {
		return n, errors.New("notifikasi tidak lengkap")
	}
	return n, nil
}

// EventID = kunci idempotensi webhook (unik per perubahan status transaksi).
func (n Notification) EventID() string {
	return n.TransactionID + ":" + n.TransactionStatus + ":" + n.StatusCode
}

// NormalizedStatus menerapkan kriteria sukses docs: settlement, atau capture dengan fraud_status accept (bila ada).
func (n Notification) NormalizedStatus() Status {
	return Normalize(n.TransactionStatus, n.FraudStatus)
}

// Normalize memetakan transaction_status Midtrans.
func Normalize(txStatus, fraud string) Status {
	switch txStatus {
	case "settlement":
		return StatusSettled
	case "capture":
		if fraud == "" || fraud == "accept" {
			return StatusSettled
		}
		return StatusChallenge
	case "authorize":
		return StatusPending
	case "pending":
		return StatusPending
	case "deny", "failure":
		return StatusFailed
	case "cancel":
		return StatusCancelled
	case "expire":
		return StatusExpired
	case "refund":
		return StatusRefunded
	case "partial_refund":
		return StatusPartialRefund
	default:
		return StatusPending
	}
}

// Signature menghitung SHA512(order_id + status_code + gross_amount + serverKey).
func Signature(orderID, statusCode, grossAmount, serverKey string) string {
	sum := sha512.Sum512([]byte(orderID + statusCode + grossAmount + serverKey))
	return hex.EncodeToString(sum[:])
}

// VerifySignature membandingkan secara constant-time.
func VerifySignature(n Notification, serverKey string) bool {
	if serverKey == "" {
		return false
	}
	want := Signature(n.OrderID, n.StatusCode, n.GrossAmount, serverKey)
	return subtle.ConstantTimeCompare([]byte(want), []byte(strings.ToLower(n.SignatureKey))) == 1
}

// GrossAmountInt mengurai gross_amount ("150000.00" atau "150000") ke rupiah utuh.
func GrossAmountInt(s string) (int64, error) {
	if i := strings.IndexByte(s, '.'); i >= 0 {
		frac := strings.TrimRight(s[i+1:], "0")
		if frac != "" {
			return 0, fmt.Errorf("gross_amount berpecahan: %q", s)
		}
		s = s[:i]
	}
	return strconv.ParseInt(s, 10, 64)
}

type TxStatus struct {
	OrderRef      string
	TransactionID string
	Status        Status
	RawStatus     string
	PaymentType   string
	GrossAmount   int64
	FraudStatus   string
	StatusCode    string
	SettledAt     time.Time
}

type RefundReq struct {
	OrderRef    string
	Key         string // idempoten; pakai ulang untuk retry
	Amount      int64
	Reason      string
	PaymentType string
	PaidAt      time.Time
	Now         time.Time
}

type RefundRes struct {
	RefundID string
	Amount   int64
}

// Gateway = kontrak adapter gateway. Semua kode domain hanya memakai antarmuka ini.
type Gateway interface {
	Name() string
	CreateTransaction(ctx context.Context, req CreateTxReq) (CreateTxRes, error)
	Status(ctx context.Context, orderRef string) (TxStatus, error)
	// Refund: ErrRefundUnsupported / ErrRefundWindow / ErrRefundRejected bila tidak bisa via API.
	Refund(ctx context.Context, req RefundReq) (RefundRes, error)
	// Expire mengakhiri transaksi pending di gateway (idempoten).
	Expire(ctx context.Context, orderRef string) error
	// ParseNotification mem-parse body (+header) callback ke bentuk Notification; keabsahan tetap diperiksa lewat VerifyNotification.
	ParseNotification(raw []byte, h http.Header) (Notification, error)
	VerifyNotification(n Notification) bool
}

// RefundWindow mengembalikan jendela refund via API per payment_type (konservatif, docs/22 §1).
// ok=false → metode tidak mendukung refund API sama sekali (jalur manual).
// Nilai diturunkan dari docs Midtrans (Refund / Direct Refund) dan HARUS diverifikasi ulang saat go-live.
func RefundWindow(paymentType string) (window time.Duration, ok bool) {
	day := 24 * time.Hour
	switch paymentType {
	case "gopay":
		return 45 * day, true // GoPay 45–180 hari (diambil terendah)
	case "qris":
		return 7 * day, true // QRIS OFF-US 7 hari (konservatif; ON-US/ShopeePay lebih panjang)
	case "shopeepay":
		return 365 * day, true
	case "dana":
		return 45 * day, true
	case "ovo":
		return 7 * day, true
	case "kredivo":
		return 14 * day, true
	case "akulaku":
		return 14 * day, true
	case "credit_card", "cc":
		return 90 * day, true // jendela kartu tidak ditemukan di docs yang dibaca; konservatif, kegagalan → manual
	default:
		// bank_transfer, echannel, permata, bca_klikpay, cstore (Alfamart/Indomaret), dll.
		return 0, false
	}
}

// CanRefundViaAPI menentukan jalur refund: true = coba gateway; false = manual.
func CanRefundViaAPI(paymentType string, paidAt, now time.Time) bool {
	w, ok := RefundWindow(paymentType)
	if !ok {
		return false
	}
	return now.Sub(paidAt) <= w
}

// Unconfigured = gateway pengganti bila kunci belum diisi: semua operasi gagal keras dengan pesan jelas.
type Unconfigured struct{}

var errUnconfigured = fmt.Errorf("%w: gateway belum dikonfigurasi (isi MIDTRANS_SERVER_KEY)", ErrGateway)

func (Unconfigured) Name() string { return "unconfigured" }
func (Unconfigured) CreateTransaction(context.Context, CreateTxReq) (CreateTxRes, error) {
	return CreateTxRes{}, errUnconfigured
}
func (Unconfigured) Status(context.Context, string) (TxStatus, error) {
	return TxStatus{}, errUnconfigured
}
func (Unconfigured) Refund(context.Context, RefundReq) (RefundRes, error) {
	return RefundRes{}, errUnconfigured
}
func (Unconfigured) Expire(context.Context, string) error { return errUnconfigured }
func (Unconfigured) VerifyNotification(Notification) bool { return false }
func (Unconfigured) ParseNotification(raw []byte, _ http.Header) (Notification, error) {
	return ParseNotification(raw)
}
