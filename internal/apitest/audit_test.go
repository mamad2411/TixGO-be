package apitest

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var audParam = regexp.MustCompile(`:[A-Za-z_]+|\*[A-Za-z_]+`)

func audURL(path string) string { return audParam.ReplaceAllString(path, "01ARZ3NDEKTSV4RRFFQ69G5FAV") }

// Audit 1 — batas peran: pembeli biasa TIDAK boleh sukses (2xx) di rute organizer/admin/agent/partner/gate-staff, dan tidak boleh 5xx.
func TestAuditCustomerCannotReachPrivilegedRoutes(t *testing.T) {
	e := NewEnv(t)
	c := e.Verified("Pembeli Biasa")
	var bad []string
	for _, r := range e.Engine.Routes() {
		p := r.Path
		if !strings.Contains(p, "/admin/") && !strings.Contains(p, "/organizer/") && !strings.Contains(p, "/agent/") {
			continue
		}
		var body any
		if r.Method != "GET" && r.Method != "DELETE" {
			body = M{}
		}
		resp := c.Do(r.Method, audURL(p), body)
		if resp.Code/100 == 2 || resp.Code >= 500 {
			bad = append(bad, fmt.Sprintf("%s %s → %d", r.Method, p, resp.Code))
		}
	}
	if len(bad) > 0 {
		t.Fatalf("rute istimewa terbuka/pecah untuk pembeli biasa:\n%s", strings.Join(bad, "\n"))
	}
}

// Audit 2 — ketahanan input: tak ada 5xx untuk body kosong/rusak & ID ngawur pada SEMUA rute (login & anonim).
func TestAuditNoServerErrorsOnMalformedInput(t *testing.T) {
	e := NewEnv(t)
	c := e.Verified("Penguji Input")
	skip := map[string]bool{"/api/v1/ws": true, "/webhooks/midtrans": false}
	var bad []string
	for _, r := range e.Engine.Routes() {
		if skip[r.Path] || strings.HasPrefix(r.Path, "/metrics") {
			continue
		}
		for _, body := range []any{nil, "{", `{"x":`, M{}, M{"id": 1, "qty": -5, "amount": "abc"}, strings.Repeat("A", 4000)} {
			if (r.Method == "GET" || r.Method == "DELETE") && body != nil {
				continue
			}
			for _, cl := range []*Client{c, e.Anon()} {
				resp := cl.Do(r.Method, audURL(r.Path), body)
				if resp.Code >= 500 && resp.Code != 503 {
					bad = append(bad, fmt.Sprintf("%s %s body=%.20v → %d %.80s", r.Method, r.Path, fmt.Sprint(body), resp.Code, resp.Body))
				}
			}
		}
	}
	if len(bad) > 0 {
		t.Fatalf("%d respons 5xx:\n%s", len(bad), strings.Join(dedupe(bad), "\n"))
	}
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		k := s
		if i := strings.Index(s, " body="); i > 0 {
			k = s[:i]
		}
		if !seen[k] {
			seen[k] = true
			out = append(out, s)
		}
	}
	return out
}

// Audit 3 — POST uang wajib Idempotency-Key.
func TestAuditMoneyPostsRequireIdempotencyKey(t *testing.T) {
	e := NewEnv(t)
	c := e.Verified("Penguji Kunci")
	for _, p := range []string{"/orders", "/orders/01ARZ3NDEKTSV4RRFFQ69G5FAV/pay", "/orders/01ARZ3NDEKTSV4RRFFQ69G5FAV/refunds", "/resale/listings/01ARZ3NDEKTSV4RRFFQ69G5FAV/buy", "/squads/01ARZ3NDEKTSV4RRFFQ69G5FAV/pay"} {
		r := c.POST(api+p, M{})
		if r.Code != 400 || !strings.Contains(string(r.Body), "Idempotency-Key") {
			t.Errorf("POST %s tanpa Idempotency-Key harus 400, dapat %d %.100s", p, r.Code, r.Body)
		}
	}
}

func TestAdminOrderToolsSearchResyncRefund(t *testing.T) {
	e := NewEnv(t)
	o, evID, slug, tier := e.publishedGA(100000, 10)
	u := e.Verified("Pelanggan Bantuan")
	fin := e.UserWithRole("finance", "Finance Ops")
	// webhook hilang: sisi gateway sudah lunas, TixGo belum tahu
	var hold struct{ ID string }
	u.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 1}).Expect(201).Data(&hold)
	var ord struct{ ID string }
	u.IdemPOST(api+"/orders", "ops-order-0001", M{"hold_id": hold.ID}).Expect(201).Data(&ord)
	var pay struct {
		PaymentID string `json:"payment_id"`
	}
	u.IdemPOST(api+"/orders/"+ord.ID+"/pay", "ops-pay-00001", nil).Expect(200).Data(&pay)
	_ = e.Gateway.Settle(e.QueryStr("SELECT provider_ref FROM payments WHERE id=$1", pay.PaymentID), "gopay") // tanpa dikirim ke webhook
	if e.QueryStr("SELECT status FROM orders WHERE id=$1", ord.ID) != "pending" {
		t.Fatal("belum terlunasi")
	}
	var rows []struct {
		ID     string
		Status string
	}
	fin.GET(api + "/admin/finance/orders?q=" + u.Email).Expect(200).Data(&rows)
	if len(rows) != 1 || rows[0].ID != ord.ID {
		t.Fatalf("cari lewat email: %+v", rows)
	}
	u.GET(api+"/admin/finance/orders").ExpectErr(403, "FORBIDDEN")
	var rs struct {
		OrderStatus string `json:"order_status"`
	}
	fin.POST(api+"/admin/finance/orders/"+ord.ID+"/resync", nil).Expect(200).Data(&rs)
	if rs.OrderStatus != "paid" || e.QueryInt("SELECT count(*) FROM tickets") != 1 {
		t.Fatalf("resync melunasi & menerbitkan tiket: %+v", rs)
	}
	fin.POST(api+"/admin/finance/orders/"+ord.ID+"/resync", nil).Expect(200) // idempoten
	if e.QueryInt("SELECT count(*) FROM tickets") != 1 {
		t.Fatal("tidak boleh tiket ganda")
	}
	var det struct {
		Payments, Refunds, Tickets []map[string]any
	}
	fin.GET(api + "/admin/finance/orders/" + ord.ID).Expect(200).Data(&det)
	if len(det.Payments) != 1 || len(det.Tickets) != 1 {
		t.Fatalf("detail: %+v", det)
	}
	// refund oleh finance: alasan wajib; tiket terpakai ditolak; normal → selesai (jalur gateway) + ledger seimbang
	fin.POST(api+"/admin/finance/orders/"+ord.ID+"/refund", M{"reason": "x"}).ExpectErr(400, "VALIDATION_FAILED")
	e.Exec("UPDATE tickets SET status='used'")
	fin.POST(api+"/admin/finance/orders/"+ord.ID+"/refund", M{"reason": "kompensasi gangguan"}).ExpectErr(422, "REFUND_NOT_ALLOWED")
	e.Exec("UPDATE tickets SET status='valid'")
	fin.POST(api+"/admin/finance/orders/"+ord.ID+"/refund", M{"reason": "kompensasi gangguan"}).Expect(201)
	if e.QueryStr("SELECT status FROM orders WHERE id=$1", ord.ID) != "refunded" || e.QueryStr("SELECT status FROM refunds") != "refunded" {
		t.Fatal("order & refund selesai")
	}
	fin.POST(api+"/admin/finance/orders/"+ord.ID+"/refund", M{"reason": "dobel"}).ExpectErr(422, "REFUND_NOT_ALLOWED")
	e.ledgerBalanced()
	_, _ = o, evID
}

func TestEventDayReminderSentOnceAndRespectsPreference(t *testing.T) {
	e := NewEnv(t)
	_, evID, slug, tier := e.publishedGA(100000, 10)
	a, b := e.Verified("Ingat Satu"), e.Verified("Ingat Dua")
	e.buyPaid(a, slug, tier, 1, "gopay")
	e.buyPaid(b, slug, tier, 1, "gopay")
	b.PUT(api+"/me/notification-prefs", M{"category": "reminders", "channel": "email", "enabled": false}).Expect(204)
	e.Exec("UPDATE events SET starts_at=now()+interval '24 hours', ends_at=now()+interval '30 hours', doors_at=now()+interval '22 hours' WHERE id=$1", evID)
	if n, err := e.Svc.Follow.RunEventReminders(t.Context()); err != nil || n != 2 {
		t.Fatalf("2 pemegang tiket diproses: %d %v", n, err)
	}
	if n, _ := e.Svc.Follow.RunEventReminders(t.Context()); n != 0 {
		t.Fatal("tidak boleh kirim ulang")
	}
	has := func(u *Client) bool {
		for _, m := range e.Mail.MailsTo(u.Email) {
			if strings.Contains(m.Subject, "Pengingat") {
				return true
			}
		}
		return false
	}
	if !has(a) || has(b) {
		t.Fatalf("pengingat: a=%v b=%v (b mematikan kategori reminders)", has(a), has(b))
	}
}

func TestPayoutAndPrivacyEmailsUseProperTemplates(t *testing.T) {
	e := NewEnv(t)
	o, evID, slug, tier := e.publishedGA(300000, 10)
	e.seedBank(o.ID)
	e.buyPaid(e.Verified("Pembeli"), slug, tier, 1, "gopay")
	e.Exec("UPDATE events SET status='finished', starts_at=now()-interval '10 days', ends_at=now()-interval '9 days' WHERE id=$1", evID)
	_, _ = e.Svc.Finance.ReleaseSettlements(t.Context())
	var bal struct{ Available int64 }
	o.C.GET(api + "/organizer/finance/balance").Data(&bal)
	var po struct{ ID string }
	o.C.POST("/api/v1/organizer/finance/payouts", M{"amount": bal.Available}, "Idempotency-Key", "mail-payout-01").Expect(201).Data(&po)
	fin := e.UserWithRole("finance", "Finance Mail")
	fin.POST(api+"/admin/finance/payouts/"+po.ID+"/approve", nil).Expect(200)
	subjects := ""
	for _, m := range e.Mail.MailsTo(o.C.Email) {
		subjects += m.Subject + "|"
	}
	if !strings.Contains(subjects, "Pencairan") || !strings.Contains(subjects, "disetujui") {
		t.Fatalf("pemilik organizer harus diberi tahu status pencairan: %s", subjects)
	}
	// ekspor data memakai templat "export_ready"
	u := e.Verified("Ekspor Mail")
	u.POST(api+"/me/privacy/exports", nil).Expect(202)
	found := false
	for _, m := range e.Mail.MailsTo(u.Email) {
		if strings.Contains(m.Subject, "Ekspor data kamu siap") {
			found = true
		}
	}
	if !found {
		t.Fatal("email ekspor siap")
	}
}

func TestConcurrentPayCallsForSameOrderNeverError5xxOrDuplicateCharge(t *testing.T) {
	e := NewEnv(t)
	_, _, slug, tier := e.publishedGA(100000, 5)
	u := e.Verified("Klik Dua Kali")
	var h struct{ ID string }
	u.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 1}).Expect(201).Data(&h)
	var ord struct{ ID string }
	u.IdemPOST(api+"/orders", "dbl-order-0001", M{"hold_id": h.ID}).Expect(201).Data(&ord)
	var wg sync.WaitGroup
	codes := make([]int, 8)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = u.IdemPOST(api+"/orders/"+ord.ID+"/pay", "dbl-pay-"+strconv.Itoa(i)+"-"+newID(), nil).Code
		}(i)
	}
	wg.Wait()
	for _, c := range codes {
		if c >= 500 {
			t.Fatalf("pay paralel tidak boleh 5xx: %v", codes)
		}
	}
	if n := e.QueryInt("SELECT count(*) FROM payments WHERE order_id=$1 AND status IN ('created','pending')", ord.ID); n != 1 {
		t.Fatalf("hanya satu transaksi aktif per order, dapat %d (kode %v)", n, codes)
	}
}

func TestSuspendedOrganizerStopsSalesButKeepsSoldTicketsValid(t *testing.T) {
	e := NewEnv(t)
	o, _, slug, tier := e.publishedGA(100000, 10)
	admin := e.UserWithRole("admin", "Admin Suspend")
	u := e.Verified("Pembeli Awal")
	oid := e.buyPaid(u, slug, tier, 1, "gopay")
	admin.POST(api+"/admin/organizers/"+o.ID+"/suspend", M{"reason": "dugaan penipuan"}).Expect(204)
	late := e.Verified("Pembeli Telat")
	late.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 1}).ExpectErr(409, "SALE_PAUSED")
	var list []map[string]any
	e.Anon().GET(api + "/events").Expect(200).Data(&list)
	for _, x := range list {
		if x["slug"] == slug {
			t.Fatal("event organizer yang ditangguhkan tidak boleh muncul di katalog")
		}
	}
	// tiket yang sudah terjual tetap sah
	var tk []map[string]any
	u.GET(api + "/tickets?status=valid").Expect(200).Data(&tk)
	if len(tk) != 1 {
		t.Fatal("tiket terjual tetap valid")
	}
	_ = oid
}

func TestAdminCanCancelEventLikeOrganizer(t *testing.T) {
	e := NewEnv(t)
	_, evID, slug, tier := e.publishedGA(100000, 5)
	u := e.Verified("Pembeli Batal Admin")
	e.buyPaid(u, slug, tier, 1, "gopay")
	admin := e.UserWithRole("admin", "Admin Batal")
	admin.POST(api+"/admin/ops/events/"+evID+"/cancel", M{"reason": "x"}).ExpectErr(400, "VALIDATION_FAILED")
	u.POST(api+"/admin/ops/events/"+evID+"/cancel", M{"reason": "tidak berhak"}).ExpectErr(403, "FORBIDDEN")
	admin.POST(api+"/admin/ops/events/"+evID+"/cancel", M{"reason": "melanggar kebijakan platform"}).Expect(200)
	if e.QueryStr("SELECT status FROM events WHERE id=$1", evID) != "cancelled" || e.QueryInt("SELECT count(*) FROM refunds WHERE initiated_by='organizer_cancel'") != 1 {
		t.Fatal("event batal + refund otomatis")
	}
	e.ledgerBalanced()
}

func TestSameIdempotencyKeyInParallelCreatesOneOrder(t *testing.T) { // T04 versi paralel
	e := NewEnv(t)
	_, _, slug, tier := e.publishedGA(100000, 5)
	u := e.Verified("Klik Kembar")
	var h struct{ ID string }
	u.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 1}).Expect(201).Data(&h)
	var wg sync.WaitGroup
	codes := make([]int, 8)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = u.IdemPOST(api+"/orders", "kembar-0001", M{"hold_id": h.ID}).Code
		}(i)
	}
	wg.Wait()
	if n := e.QueryInt("SELECT count(*) FROM orders"); n != 1 {
		t.Fatalf("satu kunci paralel harus satu order, dapat %d (kode %v)", n, codes)
	}
	for _, c := range codes {
		if c >= 500 {
			t.Fatalf("5xx: %v", codes)
		}
	}
}

func TestRefreshCookieAttributes(t *testing.T) {
	e := NewEnv(t)
	e.Register("cookie@example.com", "Cookie Uji")
	e.Exec("UPDATE users SET email_verified_at=now() WHERE email='cookie@example.com'")
	r := e.Anon().POST(api+"/auth/login", M{"email": "cookie@example.com", "password": testPassword}).Expect(200)
	var found bool
	for _, c := range r.Header.Values("Set-Cookie") {
		if strings.HasPrefix(c, "tixgo_rt=") {
			found = true
			l := strings.ToLower(c)
			if !strings.Contains(l, "httponly") || (!strings.Contains(l, "samesite=strict") && !strings.Contains(l, "samesite=lax")) || !strings.Contains(l, "path=") {
				t.Fatalf("cookie refresh harus HttpOnly + SameSite + Path: %s", c)
			}
		}
	}
	if !found {
		t.Fatal("tidak ada cookie refresh")
	}
}

// Invarian lintas-tabel: saldo akun "payment_clearing" di ledger == Σ total order lunas/direfund − Σ refund selesai (uang yang tertahan di kliring gateway).
func TestLedgerClearingAgreesWithOrdersAndRefunds(t *testing.T) {
	e := NewEnv(t)
	o, evID, slug, tier := resaleEvent(t, e, 120)
	until := e.Clock.Now().Add(240 * time.Hour).Format(time.RFC3339)
	e.Exec(`UPDATE events SET refund_policy=$2::jsonb WHERE id=$1`, evID, `{"type":"refundable","refundable_until":"`+until+`","deduction_pct":10,"fee_refundable":false,"on_reschedule":{"window_days":14}}`)
	var ad struct{ ID string }
	o.C.POST(api+"/organizer/events/"+evID+"/addons", M{"name": "Merch", "price": 25000, "quota": 10}).Expect(201).Data(&ad)
	fin := e.UserWithRole("finance", "Finance Invarian")
	var users []*Client
	for i := 0; i < 5; i++ {
		users = append(users, e.Verified("Pembeli Invarian"))
	}
	e.buyPaid(users[0], slug, tier, 2, "gopay")
	o1 := e.buyPaid(users[1], slug, tier, 1, "gopay")
	e.buyPaid(users[2], slug, tier, 3, "bank_transfer")
	users[1].IdemPOST(api+"/orders/"+o1+"/refunds", "inv-refund-01", M{}).Expect(201) // potongan 10%
	o3 := e.buyPaid(users[3], slug, tier, 1, "gopay")
	fin.POST(api+"/admin/finance/orders/"+o3+"/refund", M{"reason": "kompensasi uji invarian"}).Expect(201)
	// resale: users[0] menjual 1 tiket ke users[4]
	tid := ticketIDs(t, users[0])[0]
	var l struct{ ID string }
	users[0].POST(api+"/tickets/"+tid+"/resale", M{"price": 110000}).Expect(201).Data(&l)
	var ro struct{ ID string }
	users[4].IdemPOST(api+"/resale/listings/"+l.ID+"/buy", "inv-resale-01", nil).Expect(201).Data(&ro)
	var pay struct {
		PaymentID string `json:"payment_id"`
	}
	users[4].IdemPOST(api+"/orders/"+ro.ID+"/pay", "inv-resale-pay", nil).Expect(200).Data(&pay)
	e.Anon().Do("POST", "/webhooks/midtrans", e.Gateway.Settle(e.QueryStr("SELECT provider_ref FROM payments WHERE id=$1", pay.PaymentID), "gopay")).Expect(200)

	clearing := e.QueryInt(`SELECT coalesce(sum(le.debit - le.credit),0) FROM ledger_entries le JOIN ledger_accounts la ON la.id=le.account_id WHERE la.kind='payment_clearing'`)
	orders := e.QueryInt(`SELECT coalesce(sum(total),0) FROM orders WHERE status IN ('paid','refunded')`)
	refunded := e.QueryInt(`SELECT coalesce(sum(amount),0) FROM refunds WHERE status='refunded'`)
	if clearing != orders-refunded {
		t.Fatalf("kliring ledger %d ≠ order lunas %d − refund %d", clearing, orders, refunded)
	}
	e.ledgerBalanced()
	// tiap order lunas punya tepat satu transaksi ledger 'order_paid'
	if e.QueryInt(`SELECT count(*) FROM orders o WHERE o.status IN ('paid','refunded') AND o.total > 0 AND NOT EXISTS (SELECT 1 FROM ledger_transactions lt WHERE lt.ref_type='order_paid' AND lt.ref_id=o.id)`) != 0 {
		t.Fatal("ada order lunas tanpa pembukuan")
	}
	_ = ad
}

// Simulasi acak berurutan (seed tetap): beli, tinggalkan hold, refund, kirim tiket, resale, batalkan order, majukan jam.
// Setelah SETIAP langkah invarian inventori & uang harus utuh.
func TestRandomizedFlowsKeepInventoryAndMoneyInvariants(t *testing.T) {
	e := NewEnv(t)
	_, evID, slug, tier := resaleEvent(t, e, 150)
	until := e.Clock.Now().Add(1000 * time.Hour).Format(time.RFC3339)
	e.Exec(`UPDATE events SET refund_policy=$2::jsonb, max_per_account=50, max_per_order=6 WHERE id=$1`, evID, `{"type":"refundable","refundable_until":"`+until+`","deduction_pct":0,"fee_refundable":true,"on_reschedule":{"window_days":14}}`)
	e.Exec(`UPDATE tiers SET quota=14 WHERE id=$1`, tier)
	seed, steps := int64(20261006), 45
	if v, err := strconv.ParseInt(os.Getenv("TIXGO_FUZZ_SEED"), 10, 64); err == nil {
		seed, steps = v, 70
	}
	rng := rand.New(rand.NewSource(seed))
	users := make([]*Client, 6)
	for i := range users {
		users[i] = e.Verified("Acak " + strconv.Itoa(i))
	}
	check := func(step int, op string) {
		t.Helper()
		if e.QueryInt(`SELECT count(*) FROM tiers WHERE sold < 0 OR held < 0 OR (quota > 0 AND sold + held > quota)`) != 0 {
			t.Fatalf("[%d %s] kuota terlampaui/negatif", step, op)
		}
		held := e.QueryInt(`SELECT coalesce(sum(hi.qty),0) FROM hold_items hi JOIN holds h ON h.id=hi.hold_id WHERE h.status='active' AND hi.tier_id=$1`, tier)
		// hold 'converted' yang order-nya masih pending juga menahan stok
		pend := e.QueryInt(`SELECT coalesce(sum(hi.qty),0) FROM hold_items hi JOIN holds h ON h.id=hi.hold_id JOIN orders o ON o.id=h.order_id WHERE h.status='converted' AND o.status='pending' AND hi.tier_id=$1`, tier)
		if h := e.QueryInt(`SELECT held FROM tiers WHERE id=$1`, tier); h != held+pend {
			t.Fatalf("[%d %s] tiers.held=%d ≠ hold aktif %d + order pending %d", step, op, h, held, pend)
		}
		sold := e.QueryInt(`SELECT coalesce(count(*),0) FROM order_items oi JOIN orders o ON o.id=oi.order_id WHERE o.status='paid' AND o.resale_listing_id IS NULL AND oi.tier_id=$1`, tier)
		if s := e.QueryInt(`SELECT sold FROM tiers WHERE id=$1`, tier); s != sold {
			t.Fatalf("[%d %s] tiers.sold=%d ≠ item order lunas=%d", step, op, s, sold)
		}
		e.ledgerBalanced()
		clearing := e.QueryInt(`SELECT coalesce(sum(le.debit-le.credit),0) FROM ledger_entries le JOIN ledger_accounts la ON la.id=le.account_id WHERE la.kind='payment_clearing'`)
		orders := e.QueryInt(`SELECT coalesce(sum(total),0) FROM orders WHERE status IN ('paid','refunded')`)
		refunded := e.QueryInt(`SELECT coalesce(sum(amount),0) FROM refunds WHERE status='refunded'`)
		if clearing != orders-refunded {
			t.Fatalf("[%d %s] kliring %d ≠ %d − %d", step, op, clearing, orders, refunded)
		}
	}
	pickPaid := func() (string, int) { // order lunas non-resale milik salah satu pengguna
		rows, _ := pool.Query(context.Background(), `SELECT id, user_id FROM orders WHERE status='paid' AND resale_listing_id IS NULL AND total>0`)
		defer rows.Close()
		type po struct{ id, uid string }
		var ps []po
		for rows.Next() {
			var p po
			_ = rows.Scan(&p.id, &p.uid)
			ps = append(ps, p)
		}
		if len(ps) == 0 {
			return "", -1
		}
		p := ps[rng.Intn(len(ps))]
		for i, u := range users {
			if u.UserID == p.uid {
				return p.id, i
			}
		}
		return "", -1
	}
	for step := 0; step < steps; step++ {
		u := users[rng.Intn(len(users))]
		op := ""
		switch rng.Intn(7) {
		case 0, 1, 2:
			op = "beli"
			var h struct{ ID string }
			if r := u.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 1 + rng.Intn(3)}); r.Code == 201 {
				r.Data(&h)
				var o struct{ ID string }
				if r := u.IdemPOST(api+"/orders", "rnd-o-"+newID(), M{"hold_id": h.ID}); r.Code == 201 {
					r.Data(&o)
					if rng.Intn(4) > 0 { // 25% ditinggal pending
						var p struct {
							PaymentID string `json:"payment_id"`
						}
						u.IdemPOST(api+"/orders/"+o.ID+"/pay", "rnd-p-"+newID(), nil).Expect(200).Data(&p)
						e.Anon().Do("POST", "/webhooks/midtrans", e.Gateway.Settle(e.QueryStr("SELECT provider_ref FROM payments WHERE id=$1", p.PaymentID), "gopay")).Expect(200)
					}
				}
			}
		case 3:
			op = "tinggal-hold"
			u.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 1})
		case 4:
			op = "refund"
			if id, idx := pickPaid(); idx >= 0 {
				users[idx].IdemPOST(api+"/orders/"+id+"/refunds", "rnd-r-"+newID(), M{})
			}
		case 5:
			op = "resale"
			if id, idx := pickPaid(); idx >= 0 {
				var tk []struct{ ID string }
				users[idx].GET(api + "/tickets?status=valid").Data(&tk)
				if len(tk) > 0 {
					var l struct{ ID string }
					if r := users[idx].POST(api+"/tickets/"+tk[0].ID+"/resale", M{"price": 100000}); r.Code == 201 {
						r.Data(&l)
						buyer := users[(idx+1)%len(users)]
						var ro struct{ ID string }
						if r := buyer.IdemPOST(api+"/resale/listings/"+l.ID+"/buy", "rnd-rb-"+newID(), nil); r.Code == 201 {
							r.Data(&ro)
							var p struct {
								PaymentID string `json:"payment_id"`
							}
							buyer.IdemPOST(api+"/orders/"+ro.ID+"/pay", "rnd-rp-"+newID(), nil).Expect(200).Data(&p)
							e.Anon().Do("POST", "/webhooks/midtrans", e.Gateway.Settle(e.QueryStr("SELECT provider_ref FROM payments WHERE id=$1", p.PaymentID), "gopay")).Expect(200)
						}
						_ = id
					}
				}
			}
		case 6:
			op = "waktu+expire"
			e.Clock.Advance(time.Duration(5+rng.Intn(20)) * time.Minute)
			if _, err := e.Svc.Hold.ExpireDue(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err := e.Svc.Order.ExpireDue(context.Background()); err != nil {
				t.Fatal(err)
			}
			for i := range users { // JWT kedaluwarsa karena jam maju
				users[i] = e.Login(users[i].Email)
			}
		}
		check(step, op)
	}
}
