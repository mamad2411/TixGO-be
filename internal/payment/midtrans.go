package payment

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Midtrans = klien HTTP Core API & Snap (tanpa SDK agar mudah diuji dengan httptest).
// Endpoint (docs.midtrans.com, dibaca 2026-10-04):
//
//	Snap   POST {app.(sandbox.)midtrans.com}/snap/v1/transactions     (Basic auth: serverKey:)
//	Status GET  {api.(sandbox.)midtrans.com}/v2/{order_id}/status
//	Refund POST {api...}/v2/{order_id}/refund            body {refund_key, amount, reason}
//	Direct POST {api...}/v2/{order_id}/refund/online/direct   (GoPay, QRIS, ShopeePay, kartu, Akulaku, Kredivo)
//	Expire POST {api...}/v2/{order_id}/expire
//
// Bentuk body Snap (transaction_details, item_details, customer_details, expiry) mengikuti dokumentasi
// publik Midtrans; verifikasi akhir dilakukan terhadap sandbox saat kunci tersedia.
type Midtrans struct {
	serverKey string
	snapURL   string
	coreURL   string
	hc        *http.Client
}

func NewMidtrans(serverKey, env, snapURL, coreURL string, hc *http.Client) *Midtrans {
	if snapURL == "" {
		snapURL = "https://app.sandbox.midtrans.com"
		if env == "production" {
			snapURL = "https://app.midtrans.com"
		}
	}
	if coreURL == "" {
		coreURL = "https://api.sandbox.midtrans.com"
		if env == "production" {
			coreURL = "https://api.midtrans.com"
		}
	}
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	return &Midtrans{serverKey: serverKey, snapURL: strings.TrimRight(snapURL, "/"), coreURL: strings.TrimRight(coreURL, "/"), hc: hc}
}

func (m *Midtrans) Name() string { return "midtrans" }

func (m *Midtrans) ParseNotification(raw []byte, _ http.Header) (Notification, error) {
	return ParseNotification(raw)
}

func (m *Midtrans) VerifyNotification(n Notification) bool { return VerifySignature(n, m.serverKey) }

func (m *Midtrans) do(ctx context.Context, method, rawURL string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rd)
	if err != nil {
		return 0, nil, err
	}
	req.SetBasicAuth(m.serverKey, "")
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := m.hc.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %v", ErrGateway, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, b, err
}

func (m *Midtrans) CreateTransaction(ctx context.Context, r CreateTxReq) (CreateTxRes, error) {
	items := make([]map[string]any, 0, len(r.Items))
	for _, it := range r.Items {
		items = append(items, map[string]any{"id": it.ID, "name": truncate(it.Name, 50), "price": it.Price, "quantity": it.Quantity})
	}
	body := map[string]any{
		"transaction_details": map[string]any{"order_id": r.OrderRef, "gross_amount": r.Amount},
		"item_details":        items,
		"customer_details":    map[string]any{"first_name": truncate(r.Customer.Name, 50), "email": r.Customer.Email, "phone": r.Customer.Phone},
	}
	if !r.ExpiresAt.IsZero() && r.ExpiresAt.After(r.Now) {
		mins := int(r.ExpiresAt.Sub(r.Now).Minutes())
		if mins < 1 {
			mins = 1
		}
		body["expiry"] = map[string]any{"start_time": r.Now.Format("2006-01-02 15:04:05 -0700"), "unit": "minutes", "duration": mins}
	}
	if r.CallbackURL != "" {
		body["callbacks"] = map[string]any{"finish": r.CallbackURL}
	}
	code, b, err := m.do(ctx, http.MethodPost, m.snapURL+"/snap/v1/transactions", body)
	if err != nil {
		return CreateTxRes{}, err
	}
	var out struct {
		Token         string   `json:"token"`
		RedirectURL   string   `json:"redirect_url"`
		ErrorMessages []string `json:"error_messages"`
	}
	_ = json.Unmarshal(b, &out)
	if code/100 != 2 || out.Token == "" {
		return CreateTxRes{}, fmt.Errorf("%w: snap %d: %s", ErrGateway, code, strings.Join(out.ErrorMessages, "; "))
	}
	return CreateTxRes{Token: out.Token, RedirectURL: out.RedirectURL}, nil
}

type statusResp struct {
	StatusCode        string `json:"status_code"`
	StatusMessage     string `json:"status_message"`
	TransactionID     string `json:"transaction_id"`
	OrderID           string `json:"order_id"`
	GrossAmount       string `json:"gross_amount"`
	PaymentType       string `json:"payment_type"`
	TransactionStatus string `json:"transaction_status"`
	FraudStatus       string `json:"fraud_status"`
	SettlementTime    string `json:"settlement_time"`
	RefundID          any    `json:"refund_chargeback_id"`
	RefundAmount      string `json:"refund_amount"`
}

func (m *Midtrans) Status(ctx context.Context, orderRef string) (TxStatus, error) {
	code, b, err := m.do(ctx, http.MethodGet, m.coreURL+"/v2/"+url.PathEscape(orderRef)+"/status", nil)
	if err != nil {
		return TxStatus{}, err
	}
	var r statusResp
	if err := json.Unmarshal(b, &r); err != nil {
		return TxStatus{}, fmt.Errorf("%w: status bukan JSON (%d)", ErrGateway, code)
	}
	if r.StatusCode == "404" || code == http.StatusNotFound {
		return TxStatus{}, ErrNotFound
	}
	if code/100 != 2 && r.StatusCode != "200" && r.StatusCode != "201" {
		return TxStatus{}, fmt.Errorf("%w: status %d/%s: %s", ErrGateway, code, r.StatusCode, r.StatusMessage)
	}
	amt, _ := GrossAmountInt(r.GrossAmount)
	ts := TxStatus{
		OrderRef: r.OrderID, TransactionID: r.TransactionID, Status: Normalize(r.TransactionStatus, r.FraudStatus),
		RawStatus: r.TransactionStatus, PaymentType: r.PaymentType, GrossAmount: amt, FraudStatus: r.FraudStatus, StatusCode: r.StatusCode,
	}
	if t, err := time.Parse("2006-01-02 15:04:05", r.SettlementTime); err == nil {
		ts.SettledAt = t.Add(-7 * time.Hour).UTC() // Midtrans melaporkan WIB (UTC+7)
	}
	return ts, nil
}

// directRefundTypes = metode yang mendukung Direct Refund (docs/22 §1).
var directRefundTypes = map[string]bool{"gopay": true, "qris": true, "shopeepay": true, "credit_card": true, "cc": true, "akulaku": true, "kredivo": true}

func (m *Midtrans) Refund(ctx context.Context, r RefundReq) (RefundRes, error) {
	if !CanRefundViaAPI(r.PaymentType, r.PaidAt, r.Now) {
		if _, ok := RefundWindow(r.PaymentType); !ok {
			return RefundRes{}, ErrRefundUnsupported
		}
		return RefundRes{}, ErrRefundWindow
	}
	path := "/v2/" + url.PathEscape(r.OrderRef) + "/refund"
	if directRefundTypes[r.PaymentType] {
		path += "/online/direct"
	}
	body := map[string]any{"refund_key": r.Key, "amount": r.Amount, "reason": truncate(r.Reason, 255)}
	code, b, err := m.do(ctx, http.MethodPost, m.coreURL+path, body)
	if err != nil {
		return RefundRes{}, err
	}
	var out statusResp
	_ = json.Unmarshal(b, &out)
	sc := out.StatusCode
	if sc == "" {
		sc = strconv.Itoa(code)
	}
	switch sc {
	case "200":
		amt, _ := GrossAmountInt(out.RefundAmount)
		id := fmt.Sprint(out.RefundID)
		if out.RefundID == nil {
			id = r.Key
		}
		return RefundRes{RefundID: id, Amount: amt}, nil
	case "202": // ditolak bank
		return RefundRes{}, fmt.Errorf("%w: %s", ErrRefundRejected, out.StatusMessage)
	case "406": // refund_key sudah dipakai: refund itu sudah ada → idempoten
		return RefundRes{RefundID: r.Key, Amount: r.Amount}, nil
	case "412", "414":
		return RefundRes{}, fmt.Errorf("%w: %s", ErrRefundRejected, out.StatusMessage)
	default:
		return RefundRes{}, fmt.Errorf("%w: refund %s: %s", ErrGateway, sc, out.StatusMessage)
	}
}

func (m *Midtrans) Expire(ctx context.Context, orderRef string) error {
	code, b, err := m.do(ctx, http.MethodPost, m.coreURL+"/v2/"+url.PathEscape(orderRef)+"/expire", nil)
	if err != nil {
		return err
	}
	var out statusResp
	_ = json.Unmarshal(b, &out)
	switch out.StatusCode {
	case "200", "404", "412": // 404: tidak ada; 412: sudah final → tidak ada yang perlu dilakukan
		return nil
	}
	if code/100 == 2 {
		return nil
	}
	return fmt.Errorf("%w: expire %d/%s: %s", ErrGateway, code, out.StatusCode, out.StatusMessage)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
