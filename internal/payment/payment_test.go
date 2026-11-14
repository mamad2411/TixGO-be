package payment

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSignatureMatchesDocsFormula(t *testing.T) {
	// SHA512(order_id + status_code + gross_amount + ServerKey), docs.midtrans.com.
	n := Notification{OrderID: "ORD1", StatusCode: "200", GrossAmount: "10000.00"}
	n.SignatureKey = Signature(n.OrderID, n.StatusCode, n.GrossAmount, "key")
	if !VerifySignature(n, "key") {
		t.Fatal("signature benar harus lolos")
	}
	if VerifySignature(n, "other") {
		t.Fatal("server key lain harus ditolak")
	}
	n.GrossAmount = "10001.00"
	if VerifySignature(n, "key") {
		t.Fatal("gross_amount diubah harus ditolak")
	}
	if VerifySignature(Notification{SignatureKey: "x"}, "") {
		t.Fatal("server key kosong tidak boleh pernah lolos")
	}
}

func TestNormalizeFollowsDocsSuccessCriteria(t *testing.T) {
	cases := []struct {
		tx, fraud string
		want      Status
	}{
		{"settlement", "", StatusSettled},
		{"capture", "accept", StatusSettled},
		{"capture", "", StatusSettled},
		{"capture", "challenge", StatusChallenge},
		{"pending", "", StatusPending},
		{"expire", "", StatusExpired},
		{"cancel", "", StatusCancelled},
		{"deny", "", StatusFailed},
		{"failure", "", StatusFailed},
		{"refund", "", StatusRefunded},
		{"partial_refund", "", StatusPartialRefund},
	}
	for _, c := range cases {
		if got := Normalize(c.tx, c.fraud); got != c.want {
			t.Errorf("Normalize(%s,%s)=%s want %s", c.tx, c.fraud, got, c.want)
		}
	}
}

func TestGrossAmountInt(t *testing.T) {
	if v, err := GrossAmountInt("150000.00"); err != nil || v != 150000 {
		t.Fatalf("got %d %v", v, err)
	}
	if v, err := GrossAmountInt("150000"); err != nil || v != 150000 {
		t.Fatalf("got %d %v", v, err)
	}
	if _, err := GrossAmountInt("150000.50"); err == nil {
		t.Fatal("pecahan harus ditolak")
	}
}

func TestRefundRouteByMethodAndWindow(t *testing.T) {
	now := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	if CanRefundViaAPI("bank_transfer", now.Add(-time.Hour), now) {
		t.Fatal("VA/transfer bank harus jalur manual")
	}
	if CanRefundViaAPI("cstore", now.Add(-time.Hour), now) {
		t.Fatal("gerai harus jalur manual")
	}
	if !CanRefundViaAPI("gopay", now.Add(-24*time.Hour), now) {
		t.Fatal("gopay dalam jendela harus via API")
	}
	if CanRefundViaAPI("ovo", now.Add(-8*24*time.Hour), now) {
		t.Fatal("ovo > 7 hari harus manual")
	}
}

func newMock(t *testing.T, h http.HandlerFunc) (*Midtrans, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return NewMidtrans("SERVERKEY", "sandbox", srv.URL, srv.URL, srv.Client()), srv
}

func TestMidtransCreateTransaction(t *testing.T) {
	var gotAuth, gotPath string
	var body map[string]any
	m, _ := newMock(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "tok", "redirect_url": "https://x"})
	})
	now := time.Now()
	res, err := m.CreateTransaction(context.Background(), CreateTxReq{OrderRef: "O1", Amount: 150000, Now: now, ExpiresAt: now.Add(10 * time.Minute),
		Customer: Customer{Name: "Rara", Email: "r@x.id"}, Items: []Item{{ID: "t1", Name: "CAT1", Price: 150000, Quantity: 1}}})
	if err != nil || res.Token != "tok" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if gotPath != "/snap/v1/transactions" || !strings.HasPrefix(gotAuth, "Basic ") {
		t.Fatalf("path=%s auth=%s", gotPath, gotAuth)
	}
	td := body["transaction_details"].(map[string]any)
	if td["order_id"] != "O1" || td["gross_amount"].(float64) != 150000 {
		t.Fatalf("body salah: %v", body)
	}
	if body["expiry"].(map[string]any)["duration"].(float64) != 10 {
		t.Fatalf("expiry salah: %v", body["expiry"])
	}
}

func TestMidtransCreateTransactionError(t *testing.T) {
	m, _ := newMock(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error_messages":["order_id sudah dipakai"]}`))
	})
	_, err := m.CreateTransaction(context.Background(), CreateTxReq{OrderRef: "O1", Amount: 1})
	if !errors.Is(err, ErrGateway) || !strings.Contains(err.Error(), "order_id sudah dipakai") {
		t.Fatalf("err=%v", err)
	}
}

func TestMidtransStatusAndNotFound(t *testing.T) {
	m, _ := newMock(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "MISSING") {
			_, _ = w.Write([]byte(`{"status_code":"404","status_message":"not found"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status_code":"200","order_id":"O1","transaction_id":"T1","gross_amount":"150000.00","payment_type":"qris","transaction_status":"settlement","fraud_status":"accept","settlement_time":"2026-10-04 10:00:00"}`))
	})
	st, err := m.Status(context.Background(), "O1")
	if err != nil || st.Status != StatusSettled || st.GrossAmount != 150000 || st.PaymentType != "qris" {
		t.Fatalf("st=%+v err=%v", st, err)
	}
	if st.SettledAt.UTC().Hour() != 3 { // 10:00 WIB = 03:00 UTC
		t.Fatalf("settled_at harus dikonversi dari WIB: %v", st.SettledAt)
	}
	if _, err := m.Status(context.Background(), "MISSING"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
}

func TestMidtransRefundPathsAndErrors(t *testing.T) {
	var path string
	var status = "200"
	m, _ := newMock(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"status_code":"` + status + `","refund_chargeback_id":7,"refund_amount":"5000.00","status_message":"msg"}`))
	})
	now := time.Now()
	req := func(pt string) RefundReq {
		return RefundReq{OrderRef: "O1", Key: "rk", Amount: 5000, PaymentType: pt, PaidAt: now.Add(-time.Hour), Now: now}
	}
	if _, err := m.Refund(context.Background(), req("gopay")); err != nil || path != "/v2/O1/refund/online/direct" {
		t.Fatalf("gopay harus direct refund: path=%s err=%v", path, err)
	}
	if _, err := m.Refund(context.Background(), req("dana")); err != nil || path != "/v2/O1/refund" {
		t.Fatalf("dana harus refund biasa: path=%s err=%v", path, err)
	}
	if _, err := m.Refund(context.Background(), req("bank_transfer")); !errors.Is(err, ErrRefundUnsupported) {
		t.Fatalf("VA harus ErrRefundUnsupported: %v", err)
	}
	old := req("ovo")
	old.PaidAt = now.Add(-10 * 24 * time.Hour)
	if _, err := m.Refund(context.Background(), old); !errors.Is(err, ErrRefundWindow) {
		t.Fatalf("di luar jendela harus ErrRefundWindow: %v", err)
	}
	status = "202"
	if _, err := m.Refund(context.Background(), req("gopay")); !errors.Is(err, ErrRefundRejected) {
		t.Fatalf("202 harus ErrRefundRejected: %v", err)
	}
	status = "406" // refund_key duplikat → idempoten
	if res, err := m.Refund(context.Background(), req("gopay")); err != nil || res.RefundID != "rk" {
		t.Fatalf("406 harus dianggap sudah ada: %+v %v", res, err)
	}
}

func TestParseNotification(t *testing.T) {
	if _, err := ParseNotification([]byte(`{"order_id":"a"}`)); err == nil {
		t.Fatal("notifikasi tidak lengkap harus ditolak")
	}
	if _, err := ParseNotification([]byte(`bukan json`)); err == nil {
		t.Fatal("bukan JSON harus ditolak")
	}
	f := NewFake(nil)
	_, _ = f.CreateTransaction(context.Background(), CreateTxReq{OrderRef: "O9", Amount: 1000})
	n, err := ParseNotification(f.Settle("O9", "qris"))
	if err != nil || !f.VerifyNotification(n) || n.NormalizedStatus() != StatusSettled {
		t.Fatalf("n=%+v err=%v", n, err)
	}
}
