package apitest

import (
	"strings"
	"testing"
	"time"
)

func TestCancelEventRefundsEveryPaidOrderAndNotifies(t *testing.T) { // T-lifecycle
	e := NewEnv(t)
	o, evID, slug, tier := e.publishedGA(150000, 20)
	var users []*Client
	var orders []string
	for i := 0; i < 3; i++ {
		u := e.Verified("Pembeli")
		users = append(users, u)
		method := "gopay"
		if i == 2 {
			method = "bank_transfer" // jalur manual
		}
		orders = append(orders, e.buyPaid(u, slug, tier, 1, method))
	}
	before := e.QueryInt("SELECT count(*) FROM ledger_entries")
	_ = before
	o.C.POST(api+"/organizer/events/"+evID+"/cancel", M{"reason": "x"}).ExpectErr(400, "VALIDATION_FAILED")
	o.C.POST(api+"/organizer/events/"+evID+"/cancel", M{"reason": "Artis berhalangan hadir"}).Expect(200)
	// idempoten
	o.C.POST(api+"/organizer/events/"+evID+"/cancel", M{"reason": "Artis berhalangan hadir"}).ExpectErr(409, "INVALID_STATE")

	if n := e.QueryInt("SELECT count(*) FROM refunds WHERE initiated_by='organizer_cancel'"); n != 3 {
		t.Fatalf("3 refund otomatis: %d", n)
	}
	// 2 gateway selesai, 1 manual menunggu rekening
	if n := e.QueryInt("SELECT count(*) FROM refunds WHERE status='refunded'"); n != 2 {
		t.Fatalf("refund gateway selesai: %d", n)
	}
	if n := e.QueryInt("SELECT count(*) FROM refunds WHERE status='awaiting_account'"); n != 1 {
		t.Fatalf("refund manual menunggu rekening: %d", n)
	}
	// event batal: stok TIDAK dikembalikan untuk dijual
	if s := e.QueryInt("SELECT sold FROM tiers WHERE id=$1", tier); s != 3 {
		t.Fatalf("stok tidak boleh kembali pada event batal: %d", s)
	}
	// tiket tidak bisa dibeli lagi
	late := e.Verified("Telat")
	late.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 1}).Expect(409)
	// notifikasi terkirim ke semua pembeli (refund_update hanya 2 yang selesai; event_cancelled 3)
	for _, u := range users {
		found := false
		for _, m := range e.Mail.MailsTo(u.Email) {
			if m.Subject != "" && strings.Contains(m.Subject, "dibatalkan") {
				found = true
			}
		}
		if !found {
			for _, m := range e.Mail.MailsTo(u.Email) {
				t.Logf("mail: %q", m.Subject)
			}
			t.Fatalf("email pembatalan ke %s tidak ada", u.Email)
		}
	}
	// sweep idempoten: tidak membuat refund ganda
	if _, err := e.Svc.Lifecycle.SweepCancelled(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := e.QueryInt("SELECT count(*) FROM refunds"); n != 3 {
		t.Fatalf("tidak boleh ada refund ganda: %d", n)
	}
	e.ledgerBalanced()
	_ = orders
}

func TestRescheduleKeepsTicketsAndOpensRefundWindow(t *testing.T) {
	e := NewEnv(t)
	o, evID, slug, tier := e.publishedGA(100000, 10)
	u := e.Verified("Jadwal")
	oid := e.buyPaid(u, slug, tier, 1, "gopay")
	u.GET(api + "/orders/" + oid + "/refund-eligibility").Expect(200)
	var el struct{ Eligible bool }
	u.GET(api + "/orders/" + oid + "/refund-eligibility").Data(&el)
	if el.Eligible {
		t.Fatal("sebelum jadwal berubah, non-refundable")
	}
	o.C.POST(api+"/organizer/events/"+evID+"/reschedule", M{"starts_at": e.future(60), "ends_at": e.future(61)}).Expect(200)
	var v struct {
		Eligible bool   `json:"eligible"`
		Amount   int64  `json:"amount"`
		Basis    string `json:"basis"`
	}
	u.GET(api + "/orders/" + oid + "/refund-eligibility").Expect(200).Data(&v)
	if !v.Eligible || v.Basis != "reschedule_window" || v.Amount <= 0 {
		t.Fatalf("setelah jadwal ulang harus eligible penuh: %+v", v)
	}
	if e.QueryStr("SELECT status FROM tickets LIMIT 1") != "valid" {
		t.Fatal("tiket tetap valid")
	}
	// lewat jendela 14 hari → tidak eligible lagi
	e.Clock.Advance(15 * 24 * time.Hour)
	u = e.Login(u.Email)
	u.GET(api + "/orders/" + oid + "/refund-eligibility").Expect(200).Data(&v)
	if v.Eligible {
		t.Fatal("jendela refund sudah habis")
	}
}

func TestDuplicateEventCopiesTiersButNotDatesOrSales(t *testing.T) {
	e := NewEnv(t)
	o, evID, slug, tier := e.publishedGA(100000, 10)
	u := e.Verified("Salin")
	e.buyPaid(u, slug, tier, 1, "gopay")
	var cp struct {
		ID, Status string
	}
	o.C.POST(api+"/organizer/events/"+evID+"/duplicate", M{"starts_at": e.future(90)}).Expect(201).Data(&cp)
	if cp.Status != "draft" || cp.ID == evID {
		t.Fatalf("salinan harus draft baru: %+v", cp)
	}
	if n := e.QueryInt("SELECT count(*) FROM tiers WHERE event_id=$1", cp.ID); n != 1 {
		t.Fatalf("tier tersalin: %d", n)
	}
	if n := e.QueryInt("SELECT sold FROM tiers WHERE event_id=$1", cp.ID); n != 0 {
		t.Fatalf("sold tidak disalin: %d", n)
	}
	if n := e.QueryInt("SELECT count(*) FROM sale_phases WHERE event_id=$1", cp.ID); n != 0 {
		t.Fatalf("fase tidak disalin: %d", n)
	}
	o.C.POST(api+"/organizer/events/"+evID+"/duplicate", M{"starts_at": e.future(-1)}).ExpectErr(400, "VALIDATION_FAILED")
}
