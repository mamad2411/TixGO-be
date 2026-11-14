package payment

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func xenditServer(t *testing.T, h http.HandlerFunc) (*Xendit, *httptest.Server) {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return NewXendit("xnd_test_key", "tok-123", s.URL, nil), s
}

func TestXenditCreateStatusExpireMapping(t *testing.T) {
	var created map[string]any
	x, _ := xenditServer(t, func(w http.ResponseWriter, r *http.Request) {
		if u, _, ok := r.BasicAuth(); !ok || u != "xnd_test_key" {
			http.Error(w, `{"error_code":"INVALID_API_KEY"}`, http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == "POST" && r.URL.Path == "/v2/invoices":
			_ = json.NewDecoder(r.Body).Decode(&created)
			_, _ = w.Write([]byte(`{"id":"inv1","external_id":"ORD-1","status":"PENDING","invoice_url":"https://checkout.example/inv1"}`))
		case r.Method == "GET" && r.URL.Path == "/v2/invoices":
			if r.URL.Query().Get("external_id") == "ORD-1" {
				_, _ = w.Write([]byte(`[{"id":"inv1","external_id":"ORD-1","status":"PAID","amount":150000,"payment_method":"EWALLET","payment_channel":"DANA","paid_at":"2026-10-05T04:00:00Z"}]`))
				return
			}
			_, _ = w.Write([]byte(`[]`))
		case r.Method == "POST" && r.URL.Path == "/invoices/inv1/expire!":
			_, _ = w.Write([]byte(`{"id":"inv1","status":"EXPIRED"}`))
		default:
			http.NotFound(w, r)
		}
	})
	now := time.Now()
	res, err := x.CreateTransaction(context.Background(), CreateTxReq{OrderRef: "ORD-1", Amount: 150000, Customer: Customer{Email: "a@b.id"}, Items: []Item{{Name: "Tiket", Price: 150000, Quantity: 1}}, ExpiresAt: now.Add(10 * time.Minute), Now: now})
	if err != nil || res.Token != "inv1" || !strings.HasPrefix(res.RedirectURL, "https://checkout.example/") {
		t.Fatalf("create: %+v %v", res, err)
	}
	if created["external_id"] != "ORD-1" || created["amount"].(float64) != 150000 || created["invoice_duration"].(float64) < 590 || created["currency"] != "IDR" {
		t.Fatalf("body: %v", created)
	}
	st, err := x.Status(context.Background(), "ORD-1")
	if err != nil || st.Status != StatusSettled || st.PaymentType != "dana" || st.GrossAmount != 150000 || st.TransactionID != "inv1" || st.SettledAt.IsZero() {
		t.Fatalf("status: %+v %v", st, err)
	}
	if _, err := x.Status(context.Background(), "TIDAK-ADA"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tidak ada → ErrNotFound: %v", err)
	}
	if err := x.Expire(context.Background(), "TIDAK-ADA"); err != nil {
		t.Fatalf("expire tak ada = idempoten: %v", err)
	}
}

func TestXenditNotificationTokenVerification(t *testing.T) {
	x := NewXendit("k", "tok-123", "http://x", nil)
	body := []byte(`{"id":"inv1","external_id":"ORD-1","status":"PAID","amount":150000,"payment_method":"QR_CODE"}`)
	h := http.Header{}
	h.Set("x-callback-token", "tok-123")
	n, err := x.ParseNotification(body, h)
	if err != nil || n.OrderID != "ORD-1" || n.GrossAmount != "150000" || n.PaymentType != "qris" || n.EventID() == "" {
		t.Fatalf("parse: %+v %v", n, err)
	}
	if !x.VerifyNotification(n) {
		t.Fatal("token benar harus lolos")
	}
	h.Set("x-callback-token", "salah")
	n2, _ := x.ParseNotification(body, h)
	if x.VerifyNotification(n2) {
		t.Fatal("token salah harus ditolak")
	}
	n3, _ := x.ParseNotification(body, http.Header{})
	if x.VerifyNotification(n3) {
		t.Fatal("tanpa token harus ditolak")
	}
	if _, err := x.ParseNotification([]byte(`{"status":"PAID"}`), h); err == nil {
		t.Fatal("notifikasi tak lengkap harus ditolak")
	}
	if NewXendit("k", "", "http://x", nil).VerifyNotification(n) {
		t.Fatal("token server kosong tidak boleh meloloskan apa pun")
	}
}

func TestXenditRefundRoutesByWindowAndMapsErrors(t *testing.T) {
	calls := 0
	x, _ := xenditServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET":
			_, _ = w.Write([]byte(`[{"id":"inv1","external_id":"ORD-1","status":"PAID"}]`))
		case r.URL.Path == "/refunds":
			calls++
			if r.Header.Get("Idempotency-key") != "rf-1" {
				t.Errorf("Idempotency-key hilang")
			}
			if calls == 1 {
				_, _ = w.Write([]byte(`{"id":"rfd1","status":"SUCCEEDED"}`))
				return
			}
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error_code":"REFUND_NOT_SUPPORTED","message":"no"}`))
		}
	})
	now := time.Now()
	req := RefundReq{OrderRef: "ORD-1", Key: "rf-1", Amount: 1000, PaymentType: "qris", PaidAt: now.Add(-time.Hour), Now: now}
	if r, err := x.Refund(context.Background(), req); err != nil || r.RefundID != "rfd1" {
		t.Fatalf("refund ok: %+v %v", r, err)
	}
	if _, err := x.Refund(context.Background(), req); !errors.Is(err, ErrRefundRejected) {
		t.Fatalf("400 → ditolak: %v", err)
	}
	req.PaymentType = "bank_transfer"
	if _, err := x.Refund(context.Background(), req); !errors.Is(err, ErrRefundUnsupported) {
		t.Fatalf("VA → manual: %v", err)
	}
}

// Uji ke sandbox NYATA bila XENDIT_SANDBOX_KEY diisi (tidak pernah jalan di CI tanpa key). Membuat lalu meng-expire-kan satu invoice.
func TestXenditSandboxRealRoundTrip(t *testing.T) {
	key := os.Getenv("XENDIT_SANDBOX_KEY")
	if key == "" {
		t.Skip("XENDIT_SANDBOX_KEY tidak diisi")
	}
	x := NewXendit(key, "unused", "", nil)
	ref := "tixgo-test-" + time.Now().Format("150405.000")
	now := time.Now()
	res, err := x.CreateTransaction(context.Background(), CreateTxReq{OrderRef: ref, Amount: 15000, Customer: Customer{Email: "tes@example.com"}, Items: []Item{{Name: "Tiket uji", Price: 15000, Quantity: 1}}, ExpiresAt: now.Add(15 * time.Minute), Now: now})
	if err != nil || res.Token == "" || !strings.Contains(res.RedirectURL, "xendit") {
		t.Fatalf("create: %+v %v", res, err)
	}
	st, err := x.Status(context.Background(), ref)
	if err != nil || st.Status != StatusPending || st.GrossAmount != 15000 {
		t.Fatalf("status: %+v %v", st, err)
	}
	if err := x.Expire(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if st, _ := x.Status(context.Background(), ref); st.Status != StatusExpired {
		t.Fatalf("setelah expire: %+v", st)
	}
	if err := x.Expire(context.Background(), ref); err != nil {
		t.Fatalf("expire kedua idempoten: %v", err)
	}
}
