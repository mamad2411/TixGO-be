package payment

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Xendit = klien HTTP Invoice API (tanpa SDK). Endpoint diverifikasi langsung ke sandbox (2026-10-05):
//
//	Buat   POST /v2/invoices                     (Basic auth: secretKey:) → id, status, invoice_url, expiry_date
//	Status GET  /v2/invoices?external_id={ref}   → [invoice] (status PENDING|PAID|SETTLED|EXPIRED)
//	Expire POST /invoices/{id}/expire!           → status EXPIRED
//	Refund POST /refunds {invoice_id, reference_id, amount, reason}  (Idempotency-key) — bentuk respons diverifikasi saat ada transaksi lunas di sandbox
//
// Callback invoice: header `x-callback-token` = token dari dashboard (Settings → Webhooks), dibandingkan constant-time.
// external_id = OrderRef (unik per percobaan bayar); invoice.id disimpan sebagai TransactionID.
type Xendit struct {
	secretKey     string
	callbackToken string
	baseURL       string
	hc            *http.Client
}

func NewXendit(secretKey, callbackToken, baseURL string, hc *http.Client) *Xendit {
	if baseURL == "" {
		baseURL = "https://api.xendit.co"
	}
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	return &Xendit{secretKey: secretKey, callbackToken: callbackToken, baseURL: strings.TrimRight(baseURL, "/"), hc: hc}
}

func (x *Xendit) Name() string { return "xendit" }

func (x *Xendit) do(ctx context.Context, method, path string, body any, hdr map[string]string) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, x.baseURL+path, rd) //nolint:gosec // baseURL dari konfigurasi operator; segmen path di-escape pemanggil
	if err != nil {
		return 0, nil, err
	}
	req.SetBasicAuth(x.secretKey, "")
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := x.hc.Do(req) //nolint:gosec // lihat di atas
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %v", ErrGateway, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, b, err
}

type xInvoice struct {
	ID             string    `json:"id"`
	ExternalID     string    `json:"external_id"`
	Status         string    `json:"status"`
	Amount         float64   `json:"amount"`
	PaidAmount     float64   `json:"paid_amount"`
	PaymentMethod  string    `json:"payment_method"`
	PaymentChannel string    `json:"payment_channel"`
	PaidAt         time.Time `json:"paid_at"`
	InvoiceURL     string    `json:"invoice_url"`
	ErrorCode      string    `json:"error_code"`
	Message        string    `json:"message"`
}

// xenditPaymentType memetakan metode Xendit ke tipe yang dipakai RefundWindow (ditulis huruf kecil).
func xenditPaymentType(method, channel string) string {
	m, c := strings.ToUpper(method), strings.ToLower(channel)
	switch m {
	case "EWALLET":
		switch c {
		case "ovo", "dana", "shopeepay":
			return c
		}
		return "ewallet_" + c
	case "QR_CODE":
		return "qris"
	case "CREDIT_CARD":
		return "credit_card"
	case "":
		return ""
	}
	return strings.ToLower(m) // bank_transfer, retail_outlet, direct_debit, paylater → jalur manual
}

// NormalizeXendit memetakan status invoice Xendit.
func NormalizeXendit(s string) Status {
	switch strings.ToUpper(s) {
	case "PAID", "SETTLED":
		return StatusSettled
	case "EXPIRED":
		return StatusExpired
	case "FAILED":
		return StatusFailed
	default:
		return StatusPending
	}
}

func (x *Xendit) CreateTransaction(ctx context.Context, r CreateTxReq) (CreateTxRes, error) {
	items := make([]map[string]any, 0, len(r.Items))
	for _, it := range r.Items {
		items = append(items, map[string]any{"name": truncate(it.Name, 100), "quantity": it.Quantity, "price": it.Price})
	}
	dur := 0
	if !r.ExpiresAt.IsZero() && r.ExpiresAt.After(r.Now) {
		dur = int(r.ExpiresAt.Sub(r.Now).Seconds())
	}
	if dur < 60 {
		dur = 60
	}
	body := map[string]any{
		"external_id": r.OrderRef, "amount": r.Amount, "currency": "IDR", "description": "Pesanan TixGo " + r.OrderRef,
		"invoice_duration": dur, "items": items, "should_send_email": false,
	}
	if r.Customer.Email != "" {
		body["payer_email"] = r.Customer.Email
	}
	if r.CallbackURL != "" {
		body["success_redirect_url"] = r.CallbackURL
	}
	code, b, err := x.do(ctx, http.MethodPost, "/v2/invoices", body, nil)
	if err != nil {
		return CreateTxRes{}, err
	}
	var out xInvoice
	_ = json.Unmarshal(b, &out)
	if code/100 != 2 || out.ID == "" || out.InvoiceURL == "" {
		return CreateTxRes{}, fmt.Errorf("%w: xendit invoice %d: %s %s", ErrGateway, code, out.ErrorCode, out.Message)
	}
	return CreateTxRes{Token: out.ID, RedirectURL: out.InvoiceURL}, nil
}

func (x *Xendit) find(ctx context.Context, orderRef string) (*xInvoice, error) {
	code, b, err := x.do(ctx, http.MethodGet, "/v2/invoices?external_id="+url.QueryEscape(orderRef), nil, nil)
	if err != nil {
		return nil, err
	}
	if code/100 != 2 {
		return nil, fmt.Errorf("%w: xendit status %d: %s", ErrGateway, code, truncate(string(b), 200))
	}
	var list []xInvoice
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("%w: respons invoice tidak terbaca", ErrGateway)
	}
	if len(list) == 0 {
		return nil, ErrNotFound
	}
	return &list[0], nil
}

func (x *Xendit) Status(ctx context.Context, orderRef string) (TxStatus, error) {
	inv, err := x.find(ctx, orderRef)
	if err != nil {
		return TxStatus{}, err
	}
	st := TxStatus{OrderRef: orderRef, TransactionID: inv.ID, Status: NormalizeXendit(inv.Status), RawStatus: inv.Status,
		PaymentType: xenditPaymentType(inv.PaymentMethod, inv.PaymentChannel), GrossAmount: int64(inv.Amount), StatusCode: "200"}
	if st.Status == StatusSettled {
		st.SettledAt = inv.PaidAt
	}
	return st, nil
}

func (x *Xendit) Expire(ctx context.Context, orderRef string) error {
	inv, err := x.find(ctx, orderRef)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	if NormalizeXendit(inv.Status) != StatusPending {
		return nil // sudah EXPIRED/PAID: idempoten
	}
	code, b, err := x.do(ctx, http.MethodPost, "/invoices/"+url.PathEscape(inv.ID)+"/expire!", nil, nil)
	if err != nil {
		return err
	}
	if code/100 != 2 {
		return fmt.Errorf("%w: xendit expire %d: %s", ErrGateway, code, truncate(string(b), 200))
	}
	return nil
}

func (x *Xendit) Refund(ctx context.Context, r RefundReq) (RefundRes, error) {
	if !CanRefundViaAPI(r.PaymentType, r.PaidAt, r.Now) {
		if _, ok := RefundWindow(r.PaymentType); !ok {
			return RefundRes{}, ErrRefundUnsupported
		}
		return RefundRes{}, ErrRefundWindow
	}
	inv, err := x.find(ctx, r.OrderRef)
	if err != nil {
		return RefundRes{}, err
	}
	body := map[string]any{"invoice_id": inv.ID, "reference_id": r.Key, "amount": r.Amount, "currency": "IDR", "reason": "REQUESTED_BY_CUSTOMER"}
	code, b, err := x.do(ctx, http.MethodPost, "/refunds", body, map[string]string{"Idempotency-key": r.Key})
	if err != nil {
		return RefundRes{}, err
	}
	var out struct {
		ID        string `json:"id"`
		Status    string `json:"status"`
		ErrorCode string `json:"error_code"`
		Message   string `json:"message"`
	}
	_ = json.Unmarshal(b, &out)
	switch {
	case code/100 == 2 && out.ID != "" && out.Status != "FAILED":
		return RefundRes{RefundID: out.ID, Amount: r.Amount}, nil
	case code/100 == 5 || code == http.StatusTooManyRequests:
		return RefundRes{}, fmt.Errorf("%w: xendit refund %d", ErrGateway, code) // sementara → retry
	default:
		return RefundRes{}, fmt.Errorf("%w: %s %s", ErrRefundRejected, out.ErrorCode, out.Message)
	}
}

// ParseNotification: body callback invoice + header x-callback-token (disalin ke SignatureKey untuk VerifyNotification).
func (x *Xendit) ParseNotification(raw []byte, h http.Header) (Notification, error) {
	var in xInvoice
	if err := json.Unmarshal(raw, &in); err != nil {
		return Notification{}, fmt.Errorf("notifikasi bukan JSON valid: %w", err)
	}
	if in.ID == "" || in.ExternalID == "" || in.Status == "" {
		return Notification{}, errors.New("notifikasi tidak lengkap")
	}
	return Notification{
		OrderID: in.ExternalID, StatusCode: "200", GrossAmount: fmt.Sprintf("%d", int64(in.Amount)), SignatureKey: h.Get("x-callback-token"),
		TransactionStatus: strings.ToLower(in.Status), PaymentType: xenditPaymentType(in.PaymentMethod, in.PaymentChannel), TransactionID: in.ID, Raw: raw,
	}, nil
}

func (x *Xendit) VerifyNotification(n Notification) bool {
	if x.callbackToken == "" || n.SignatureKey == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(x.callbackToken), []byte(n.SignatureKey)) == 1
}
