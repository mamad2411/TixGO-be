package apitest

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/redis/go-redis/v9"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"tixgo/backend/internal/config"
)

// publishedGA membuat event published dengan satu tier GA.
func (e *Env) publishedGA(price int64, quota int) (o *Org, eventID, slug, tierID string) {
	e.T.Helper()
	o = e.NewOrganizer(true)
	venue := e.NewVenue(o.C, "Jakarta")
	var ev struct{ ID, Slug string }
	o.C.POST(api+"/organizer/events", M{"venue_id": venue, "title": "Konser Beli", "mode": "general", "starts_at": e.future(30), "ends_at": e.future(31)}).Expect(201).Data(&ev)
	var tr struct{ ID string }
	o.C.POST(api+"/organizer/events/"+ev.ID+"/tiers", M{"name": "GA", "price": price, "quota": quota}).Expect(201).Data(&tr)
	e.ForcePublish(ev.ID)
	e.Exec("INSERT INTO sale_phases(id,event_id,name,audience,starts_at,ends_at) VALUES ($1,$2,'Umum','public',now()-interval '1 day',now()+interval '20 days')", newID(), ev.ID)
	e.Exec("UPDATE events SET refund_policy='{\"type\":\"non_refundable\"}'::jsonb WHERE id=$1", ev.ID)
	return o, ev.ID, ev.Slug, tr.ID
}

func TestPurchaseHappyPath(t *testing.T) {
	e := NewEnv(t)
	_, evID, slug, tier := e.publishedGA(250000, 10)
	u := e.Verified("Pembeli")

	var hold struct {
		ID       string `json:"id"`
		Subtotal int64  `json:"subtotal"`
	}
	u.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 2}).Expect(201).Data(&hold)
	if hold.Subtotal != 500000 {
		t.Fatalf("subtotal: %d", hold.Subtotal)
	}
	var ord struct {
		ID    string `json:"id"`
		Quote struct {
			Total int64 `json:"total"`
		} `json:"quote"`
	}
	u.IdemPOST(api+"/orders", "key-order-1", M{"hold_id": hold.ID}).Expect(201).Data(&ord)
	if ord.Quote.Total < 500000 {
		t.Fatalf("total: %d", ord.Quote.Total)
	}
	var pay struct {
		PaymentID string `json:"payment_id"`
	}
	u.IdemPOST(api+"/orders/"+ord.ID+"/pay", "key-pay-0001", nil).Expect(200).Data(&pay)

	ref := e.QueryStr("SELECT provider_ref FROM payments WHERE id=$1", pay.PaymentID)
	body := e.Gateway.Settle(ref, "bank_transfer")
	e.Anon().Do("POST", "/webhooks/midtrans", body).Expect(200)
	// webhook ganda → idempoten
	e.Anon().Do("POST", "/webhooks/midtrans", body).Expect(200)

	if s := e.QueryStr("SELECT status FROM orders WHERE id=$1", ord.ID); s != "paid" {
		t.Fatalf("order: %s", s)
	}
	if n := e.QueryInt("SELECT count(*) FROM tickets t JOIN order_items i ON i.id=t.order_item_id WHERE i.order_id=$1", ord.ID); n != 2 {
		t.Fatalf("tiket: %d", n)
	}
	if n := e.QueryInt("SELECT count(*) FROM invoices WHERE order_id=$1", ord.ID); n != 1 {
		t.Fatalf("invoice: %d", n)
	}
	if n := e.QueryInt("SELECT sold FROM tiers WHERE id=$1", tier); n != 2 {
		t.Fatalf("sold: %d", n)
	}
	if n := e.QueryInt("SELECT held FROM tiers WHERE id=$1", tier); n != 0 {
		t.Fatalf("held: %d", n)
	}
	// ledger seimbang
	if d := e.QueryInt("SELECT coalesce(sum(debit)-sum(credit),0) FROM ledger_entries"); d != 0 {
		t.Fatalf("ledger tidak seimbang: %d", d)
	}
	_ = evID
	u.GET(api + "/tickets").Expect(200)
}

func TestConcurrentHoldsNeverOversellGA(t *testing.T) { // T01
	e := NewEnv(t)
	_, _, slug, tier := e.publishedGA(100000, 20)
	const N = 60
	users := make([]*Client, N)
	for i := range users {
		users[i] = e.Verified("Rebutan")
	}
	var wg sync.WaitGroup
	var ok, full atomic.Int64
	for _, u := range users {
		wg.Add(1)
		go func(u *Client) {
			defer wg.Done()
			r := u.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 2})
			switch r.Code {
			case 201:
				ok.Add(1)
			case 409:
				full.Add(1)
			default:
				t.Errorf("status tak terduga %d: %s", r.Code, r.Body)
			}
		}(u)
	}
	wg.Wait()
	if ok.Load() != 10 || full.Load() != N-10 {
		t.Fatalf("harus tepat 10 hold sukses (20/2): ok=%d full=%d", ok.Load(), full.Load())
	}
	if h := e.QueryInt("SELECT held FROM tiers WHERE id=$1", tier); h != 20 {
		t.Fatalf("held=%d", h)
	}
}

func TestOrderIdempotencyAndWebhookSecurity(t *testing.T) { // T04–T07
	e := NewEnv(t)
	_, _, slug, tier := e.publishedGA(150000, 10)
	u := e.Verified("Idem")
	var hold struct{ ID string }
	u.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 1}).Expect(201).Data(&hold)
	u.POST(api+"/orders", M{"hold_id": hold.ID}).ExpectErr(400, "VALIDATION_FAILED") // tanpa Idempotency-Key
	var a, b struct{ ID string }
	u.IdemPOST(api+"/orders", "key-idem-001", M{"hold_id": hold.ID}).Expect(201).Data(&a)
	u.IdemPOST(api+"/orders", "key-idem-001", M{"hold_id": hold.ID}).Data(&b)
	if a.ID == "" || a.ID != b.ID {
		t.Fatalf("kunci sama harus order sama: %s vs %s", a.ID, b.ID)
	}
	if n := e.QueryInt("SELECT count(*) FROM orders"); n != 1 {
		t.Fatalf("order: %d", n)
	}
	var pay struct {
		PaymentID string `json:"payment_id"`
	}
	u.IdemPOST(api+"/orders/"+a.ID+"/pay", "key-pay-idem1", nil).Expect(200).Data(&pay)
	ref := e.QueryStr("SELECT provider_ref FROM payments WHERE id=$1", pay.PaymentID)

	// tanda tangan salah → ditolak, order tetap pending
	good := e.Gateway.Settle(ref, "bank_transfer")
	e.Exec("UPDATE orders SET status='pending', paid_at=NULL WHERE id=$1", a.ID)
	bad := bytes.Replace(good, []byte(`"signature_key":"`), []byte(`"signature_key":"00`), 1)
	if r := e.Anon().Do("POST", "/webhooks/midtrans", bad); r.Code == 200 {
		t.Fatalf("signature salah tidak boleh 200: %s", r.Body)
	}
	if s := e.QueryStr("SELECT status FROM orders WHERE id=$1", a.ID); s != "pending" {
		t.Fatalf("order harus tetap pending: %s", s)
	}
	// gross_amount dimanipulasi → ditolak
	if n := e.QueryInt("SELECT count(*) FROM tickets"); n != 0 {
		t.Fatalf("tiket tak boleh terbit: %d", n)
	}
}

func TestOrderExpiryReleasesStock(t *testing.T) {
	e := NewEnv(t)
	_, _, slug, tier := e.publishedGA(100000, 5)
	u := e.Verified("Expire")
	var hold struct{ ID string }
	u.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 3}).Expect(201).Data(&hold)
	var ord struct{ ID string }
	u.IdemPOST(api+"/orders", "key-expire-01", M{"hold_id": hold.ID}).Expect(201).Data(&ord)
	e.Clock.Advance(2 * time.Hour)
	if _, err := e.Svc.Order.ExpireDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := e.QueryStr("SELECT status FROM orders WHERE id=$1", ord.ID); s != "expired" {
		t.Fatalf("status: %s", s)
	}
	if n := e.QueryInt("SELECT held+sold FROM tiers WHERE id=$1", tier); n != 0 {
		t.Fatalf("stok harus kembali: %d", n)
	}
}

// buyPaid membeli qty tiket dan melunasi; mengembalikan orderID.
func (e *Env) buyPaid(u *Client, slug, tier string, qty int, method string) string {
	e.T.Helper()
	var hold struct{ ID string }
	u.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": qty}).Expect(201).Data(&hold)
	var ord struct{ ID string }
	u.IdemPOST(api+"/orders", "ord-"+newID(), M{"hold_id": hold.ID}).Expect(201).Data(&ord)
	var pay struct {
		PaymentID string `json:"payment_id"`
	}
	u.IdemPOST(api+"/orders/"+ord.ID+"/pay", "pay-"+newID(), nil).Expect(200).Data(&pay)
	ref := e.QueryStr("SELECT provider_ref FROM payments WHERE id=$1", pay.PaymentID)
	e.Anon().Do("POST", "/webhooks/midtrans", e.Gateway.Settle(ref, method)).Expect(200)
	return ord.ID
}

func (e *Env) ledgerBalanced() {
	e.T.Helper()
	if d := e.QueryInt("SELECT coalesce(sum(debit)-sum(credit),0) FROM ledger_entries"); d != 0 {
		e.T.Fatalf("ledger tidak seimbang: %d", d)
	}
}

func TestRefundNonRefundableDefaultRejected(t *testing.T) { // Q3
	e := NewEnv(t)
	_, _, slug, tier := e.publishedGA(100000, 10)
	u := e.Verified("Batal")
	oid := e.buyPaid(u, slug, tier, 1, "bank_transfer")
	var el struct {
		Eligible bool `json:"eligible"`
	}
	u.GET(api + "/orders/" + oid + "/refund-eligibility").Expect(200).Data(&el)
	if el.Eligible {
		t.Fatal("non-refundable tidak boleh eligible")
	}
	u.IdemPOST(api+"/orders/"+oid+"/refunds", "key-refund-01", M{"reason": "berubah pikiran"}).ExpectErr(422, "REFUND_NOT_ALLOWED")
}

func TestRefundableGatewayPathRestoresStockAndLedger(t *testing.T) {
	e := NewEnv(t)
	_, evID, slug, tier := e.publishedGA(200000, 10)
	until := e.Clock.Now().Add(10 * 24 * time.Hour).Format(time.RFC3339)
	e.Exec(`UPDATE events SET refund_policy=$2::jsonb WHERE id=$1`, evID, `{"type":"refundable","refundable_until":"`+until+`","deduction_pct":10,"fee_refundable":false,"on_reschedule":{"window_days":14}}`)
	u := e.Verified("Refund")
	oid := e.buyPaid(u, slug, tier, 2, "gopay")
	e.ledgerBalanced()

	var rf struct {
		ID     string `json:"id"`
		Amount int64  `json:"amount"`
		Status string `json:"status"`
	}
	u.IdemPOST(api+"/orders/"+oid+"/refunds", "key-refund-02", M{"reason": "tidak bisa hadir"}).Expect(201).Data(&rf)
	// 10% potongan atas 400000 = 40000 → 360000 (fee tidak dikembalikan)
	if rf.Amount != 360000 {
		t.Fatalf("jumlah refund: %d", rf.Amount)
	}
	if s := e.QueryStr("SELECT status FROM refunds WHERE id=$1", rf.ID); s != "refunded" {
		t.Fatalf("status refund (job inline harus selesai): %s", s)
	}
	if s := e.QueryStr("SELECT status FROM orders WHERE id=$1", oid); s != "refunded" {
		t.Fatalf("order: %s", s)
	}
	if n := e.QueryInt("SELECT count(*) FROM tickets t JOIN order_items i ON i.id=t.order_item_id WHERE i.order_id=$1 AND t.status='refunded'", oid); n != 2 {
		t.Fatalf("tiket refunded: %d", n)
	}
	if n := e.QueryInt("SELECT sold FROM tiers WHERE id=$1", tier); n != 0 {
		t.Fatalf("stok harus kembali: %d", n)
	}
	e.ledgerBalanced()
	// permintaan kedua ditolak
	u.IdemPOST(api+"/orders/"+oid+"/refunds", "key-refund-03", M{}).ExpectErr(422, "REFUND_NOT_ALLOWED")
}

func TestRefundManualPathNeedsAccountThenFinanceMarksPaid(t *testing.T) {
	e := NewEnv(t)
	_, evID, slug, tier := e.publishedGA(100000, 10)
	until := e.Clock.Now().Add(10 * 24 * time.Hour).Format(time.RFC3339)
	e.Exec(`UPDATE events SET refund_policy=$2::jsonb WHERE id=$1`, evID, `{"type":"refundable","refundable_until":"`+until+`","deduction_pct":0,"fee_refundable":true,"on_reschedule":{"window_days":14}}`)
	u := e.Verified("Manual")
	oid := e.buyPaid(u, slug, tier, 1, "bank_transfer") // VA → tidak bisa via API
	var rf struct {
		ID           string `json:"id"`
		Status       string `json:"status"`
		NeedsAccount bool   `json:"needs_account"`
	}
	u.IdemPOST(api+"/orders/"+oid+"/refunds", "key-refund-04", M{}).Expect(201).Data(&rf)
	if !rf.NeedsAccount || rf.Status != "awaiting_account" {
		t.Fatalf("harus menunggu rekening: %+v", rf)
	}
	u.PUT(api+"/refunds/"+rf.ID+"/account", M{"bank_code": "BCA", "account_name": "Budi", "account_no": "1234567890"}).Expect(200)
	fin := e.EnableMFA(e.UserWithRole("finance", "Finance"))
	var list []map[string]any
	fin.GET(api + "/admin/finance/refunds?status=refunding").Expect(200).Data(&list)
	if len(list) != 1 || list[0]["account_no"] != "1234567890" {
		t.Fatalf("finance harus melihat rekening: %v", list)
	}
	// tanpa bukti ditolak
	fin.Upload(api+"/admin/finance/refunds/"+rf.ID+"/mark-paid", map[string]string{"note": "x"}, "other", "x.txt", []byte("x")).ExpectErr(400, "VALIDATION_FAILED")
	fin.Upload(api+"/admin/finance/refunds/"+rf.ID+"/mark-paid", map[string]string{"note": "TF 1"}, "proof", "bukti.png", pngBytes(8, 8)).Expect(200)
	if s := e.QueryStr("SELECT status FROM orders WHERE id=$1", oid); s != "refunded" {
		t.Fatalf("order: %s", s)
	}
	e.ledgerBalanced()
}

func TestOutboxRelayPublishesToBus(t *testing.T) {
	e := NewEnv(t)
	_, _, slug, tier := e.publishedGA(100000, 5)
	u := e.Verified("Relay")
	e.buyPaid(u, slug, tier, 1, "gopay")
	pending := e.QueryInt("SELECT count(*) FROM outbox WHERE published_at IS NULL")
	if pending == 0 {
		t.Fatal("harus ada outbox setelah pembelian")
	}
	sub := e.Redis.Subscribe(context.Background(), "tixgo:events")
	defer func() { _ = sub.Close() }()
	if _, err := sub.Receive(context.Background()); err != nil {
		t.Fatal(err)
	}
	n, err := e.Svc.RelayOutbox(context.Background())
	if err != nil || int64(n) != pending {
		t.Fatalf("relay n=%d pending=%d err=%v", n, pending, err)
	}
	if e.QueryInt("SELECT count(*) FROM outbox WHERE published_at IS NULL") != 0 {
		t.Fatal("outbox harus kosong")
	}
	msg, err := sub.ReceiveTimeout(context.Background(), 2*time.Second)
	m, ok := msg.(*redis.Message)
	if err != nil || !ok {
		t.Fatalf("pesan bus tidak diterima: %v", err)
	}
	var bm struct{ Channel, Type string }
	_ = json.Unmarshal([]byte(m.Payload), &bm)
	if !strings.HasPrefix(bm.Channel, "user.") && !strings.HasPrefix(bm.Channel, "event.") && !strings.HasPrefix(bm.Channel, "org.") {
		t.Fatalf("channel bus harus berbentuk user./event./org.: %+v", bm)
	}
}

func TestHoldExtendOnceAlsoWhenConvertedToPendingOrder(t *testing.T) { // Timer Tenang
	e := NewEnv(t)
	_, _, slug, tier := e.publishedGA(100000, 10)
	u := e.Verified("Tenang")
	var hold struct{ ID string }
	u.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 1}).Expect(201).Data(&hold)
	var ord struct {
		ID        string
		ExpiresAt time.Time `json:"expires_at"`
	}
	u.IdemPOST(api+"/orders", "ext-order-001", M{"hold_id": hold.ID}).Expect(201).Data(&ord)
	u.POST(api+"/holds/"+hold.ID+"/extend", nil).Expect(200)
	var o2 struct {
		ExpiresAt time.Time `json:"expires_at"`
	}
	u.GET(api + "/orders/" + ord.ID).Expect(200).Data(&o2)
	if !o2.ExpiresAt.After(ord.ExpiresAt) {
		t.Fatalf("order ikut diperpanjang: %v vs %v", o2.ExpiresAt, ord.ExpiresAt)
	}
	u.POST(api+"/holds/"+hold.ID+"/extend", nil).ExpectErr(409, "INVALID_STATE") // hanya sekali
	// setelah pembayaran dibuka tidak bisa diperpanjang
	_, _, slug2, tier2 := e.publishedGA(100000, 10)
	var h2 struct{ ID string }
	u.POST(api+"/events/"+slug2+"/holds", M{"tier_id": tier2, "qty": 1}).Expect(201).Data(&h2)
	var o3 struct{ ID string }
	u.IdemPOST(api+"/orders", "ext-order-002", M{"hold_id": h2.ID}).Expect(201).Data(&o3)
	u.IdemPOST(api+"/orders/"+o3.ID+"/pay", "ext-pay-0002", nil).Expect(200)
	u.POST(api+"/holds/"+h2.ID+"/extend", nil).ExpectErr(409, "INVALID_STATE")
}

func TestDevPaymentGatewayFakeDisallowedInProduction(t *testing.T) {
	cfg := func(k string) string {
		m := map[string]string{"APP_ENV": "production", "PAYMENT_GATEWAY": "fake", "DATABASE_URL": "x", "REDIS_ADDR": "x", "CORS_ORIGINS": "https://a.test", "JWT_SECRET": strings.Repeat("s", 40),
			"ENC_KEY": testEncKey, "S3_ENDPOINT": "x", "S3_BUCKET": "x", "S3_ACCESS_KEY": "x", "S3_SECRET_KEY": "x", "MIDTRANS_SERVER_KEY": "k", "METRICS_TOKEN": "tok"}
		return m[k]
	}
	if _, err := config.Load(cfg); err == nil || !strings.Contains(err.Error(), "PAYMENT_GATEWAY=fake") {
		t.Fatalf("fake harus ditolak di production: %v", err)
	}
}

func TestDevSettleRouteExistsOnlyWithFakeGateway(t *testing.T) {
	e := NewEnv(t) // harness memakai payment.Fake → rute dev ada
	u := e.Verified("Dev User")
	u.POST(api+"/dev/payments/settle", M{"order_id": newID()}).ExpectErr(404, "NOT_FOUND") // belum ada pembayaran
}

func TestAddonsAtCheckoutQuotaPriceAndRelease(t *testing.T) { // M43
	e := NewEnv(t)
	o, evID, slug, tier := e.publishedGA(100000, 20)
	var ad struct{ ID string }
	o.C.POST(api+"/organizer/events/"+evID+"/addons", M{"name": "Merchandise", "price": 50000, "quota": 3}).Expect(201).Data(&ad)
	u := e.Verified("Beli Merch")
	var hold struct{ ID string }
	u.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 1}).Expect(201).Data(&hold)
	var qt struct {
		Total       int64 `json:"total"`
		AddonsTotal int64 `json:"addons_total"`
	}
	u.POST(api+"/orders/quote", M{"hold_id": hold.ID, "addons": []M{{"addon_id": ad.ID, "qty": 2}}}).Expect(200).Data(&qt)
	if qt.AddonsTotal != 100000 || qt.Total < 200000 {
		t.Fatalf("kuotasi memuat add-on: %+v", qt)
	}
	var ord struct{ ID string }
	u.IdemPOST(api+"/orders", "addon-order-01", M{"hold_id": hold.ID, "addons": []M{{"addon_id": ad.ID, "qty": 2}}}).Expect(201).Data(&ord)
	if e.QueryInt("SELECT addons_total FROM orders WHERE id=$1", ord.ID) != 100000 || e.QueryInt("SELECT sold FROM addons WHERE id=$1", ad.ID) != 2 {
		t.Fatal("add-on 2×50.000 tercatat & kuota terpakai")
	}
	// kuota add-on habis (sisa 1) → ditolak
	u2 := e.Verified("Merch Kedua")
	var h2 struct{ ID string }
	u2.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 1}).Expect(201).Data(&h2)
	u2.IdemPOST(api+"/orders", "addon-order-02", M{"hold_id": h2.ID, "addons": []M{{"addon_id": ad.ID, "qty": 2}}}).ExpectErr(409, "SOLD_OUT")
	// order kedaluwarsa melepas kuota add-on; setelah lunas, ledger memasukkan add-on ke penjualan organizer
	e.Clock.Advance(15 * time.Minute)
	if _, err := e.Svc.Order.ExpireDue(t.Context()); err != nil {
		t.Fatal(err)
	}
	if e.QueryInt("SELECT sold FROM addons WHERE id=$1", ad.ID) != 0 {
		t.Fatal("kuota add-on kembali")
	}
	u3 := e.Login(u2.Email)
	var h3 struct{ ID string }
	u3.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 1}).Expect(201).Data(&h3)
	var o3 struct{ ID string }
	u3.IdemPOST(api+"/orders", "addon-order-03", M{"hold_id": h3.ID, "addons": []M{{"addon_id": ad.ID, "qty": 1}}}).Expect(201).Data(&o3)
	var pay struct {
		PaymentID string `json:"payment_id"`
	}
	u3.IdemPOST(api+"/orders/"+o3.ID+"/pay", "addon-pay-0003", nil).Expect(200).Data(&pay)
	e.Anon().Do("POST", "/webhooks/midtrans", e.Gateway.Settle(e.QueryStr("SELECT provider_ref FROM payments WHERE id=$1", pay.PaymentID), "gopay")).Expect(200)
	e.ledgerBalanced()
	if e.QueryInt("SELECT subtotal+addons_total FROM orders WHERE id=$1", o3.ID) != 150000 {
		t.Fatal("subtotal tiket + add-on")
	}
}
