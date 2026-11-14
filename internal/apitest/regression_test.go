package apitest

import (
	"context"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"tixgo/backend/internal/payment"
)

func TestSeatHoldOppositeOrderNoDeadlock(t *testing.T) { // T02
	e := NewEnv(t)
	_, _, slug, tier := e.publishedSeated(8, 10)
	var seats struct {
		Seats []struct{ ID string }
	}
	e.Anon().GET(api + "/events/" + slug + "/seatmap").Expect(200).Data(&seats)
	if len(seats.Seats) < 40 {
		t.Fatalf("seatmap: %d", len(seats.Seats))
	}
	_ = tier
	a, b := e.Verified("Pemburu A"), e.Verified("Pemburu B")
	var fiveXX atomic.Int64
	for round := 0; round < 15; round++ {
		s1, s2 := seats.Seats[round*2].ID, seats.Seats[round*2+1].ID
		var wg sync.WaitGroup
		var ok atomic.Int64
		run := func(c *Client, ids []string) {
			defer wg.Done()
			r := c.POST(api+"/events/"+slug+"/holds", M{"seat_ids": ids})
			switch {
			case r.Code == 201:
				ok.Add(1)
			case r.Code >= 500:
				fiveXX.Add(1)
			}
		}
		wg.Add(2)
		go run(a, []string{s1, s2})
		go run(b, []string{s2, s1})
		wg.Wait()
		if ok.Load() != 1 {
			t.Fatalf("ronde %d: tepat 1 sukses, dapat %d", round, ok.Load())
		}
		// bebaskan agar ronde berikutnya tidak terhalang batas satu-hold-aktif
		e.Exec("UPDATE holds SET status='released' WHERE status='active'")
		e.Exec("UPDATE seats SET status='available' WHERE status='held'")
	}
	if fiveXX.Load() != 0 {
		t.Fatalf("deadlock/5xx: %d", fiveXX.Load())
	}
}

func TestHoldExpiryFreesStockWithinWorkerTick(t *testing.T) { // T03
	e := NewEnv(t)
	_, _, slug, tier := e.publishedGA(100000, 5)
	u := e.Verified("Lalai")
	u.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 3}).Expect(201)
	if e.QueryInt("SELECT held FROM tiers WHERE id=$1", tier) != 3 {
		t.Fatal("3 ditahan")
	}
	e.Clock.Advance(11 * time.Minute) // hold_ttl default 600 dtk
	if n, err := e.Svc.Hold.ExpireDue(t.Context()); err != nil || n != 1 {
		t.Fatalf("expire: %d %v", n, err)
	}
	if e.QueryInt("SELECT held FROM tiers WHERE id=$1", tier) != 0 {
		t.Fatal("kuota kembali")
	}
}

func TestPayAfterOrderExpiredIsFulfilledIfStockStillFree(t *testing.T) { // T07
	e := NewEnv(t)
	_, _, slug, tier := e.publishedGA(100000, 5)
	u := e.Verified("Telat Bayar")
	var hold struct{ ID string }
	u.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 1}).Expect(201).Data(&hold)
	var ord struct{ ID string }
	u.IdemPOST(api+"/orders", "late-ord-0001", M{"hold_id": hold.ID}).Expect(201).Data(&ord)
	var pay struct {
		PaymentID string `json:"payment_id"`
	}
	u.IdemPOST(api+"/orders/"+ord.ID+"/pay", "late-pay-0001", nil).Expect(200).Data(&pay)
	e.Clock.Advance(15 * time.Minute)
	if _, err := e.Svc.Order.ExpireDue(t.Context()); err != nil {
		t.Fatal(err)
	}
	if e.QueryStr("SELECT status FROM orders WHERE id=$1", ord.ID) != "expired" {
		t.Fatal("expired")
	}
	ref := e.QueryStr("SELECT provider_ref FROM payments WHERE id=$1", pay.PaymentID)
	e.Anon().Do("POST", "/webhooks/midtrans", e.Gateway.Settle(ref, "gopay")).Expect(200)
	if e.QueryStr("SELECT status FROM orders WHERE id=$1", ord.ID) != "paid" {
		t.Fatal("stok masih bebas → tetap diterbitkan")
	}
	if e.QueryInt("SELECT count(*) FROM recon_exceptions WHERE kind='paid_after_expire_fulfilled'") != 1 {
		t.Fatal("jejak rekonsiliasi")
	}
	if e.QueryInt("SELECT sold FROM tiers WHERE id=$1", tier) != 1 {
		t.Fatal("sold=1")
	}
	e.ledgerBalanced()
}

func TestHoldStillWorksWhenRedisIsDown(t *testing.T) { // T18: DB tetap penjaga stok
	e := NewEnv(t)
	_, _, slug, tier := e.publishedGA(100000, 3)
	users := []*Client{e.Verified("R1"), e.Verified("R2"), e.Verified("R3"), e.Verified("R4")}
	orig := e.Svc.Redis
	dead := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 30 * time.Millisecond, MaxRetries: -1})
	e.Svc.Redis = dead
	t.Cleanup(func() { e.Svc.Redis = orig; _ = dead.Close() })
	var ok int
	for _, u := range users {
		if r := u.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 1}); r.Code == 201 {
			ok++
		} else if r.Code >= 500 {
			t.Fatalf("Redis mati tidak boleh membuat 5xx: %d %s", r.Code, r.Body)
		}
	}
	if ok != 3 {
		t.Fatalf("tepat 3 (kuota) walau Redis mati: %d", ok)
	}
}

func TestInvoiceNumbersAreUniqueAndGaplessUnderParallelPayments(t *testing.T) { // T27
	e := NewEnv(t)
	_, _, slug, tier := e.publishedGA(100000, 60)
	const N = 30
	type po struct{ oid, ref string }
	var pending []po
	for i := 0; i < N; i++ {
		u := e.Verified("Bayar Paralel")
		var h struct{ ID string }
		u.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 1}).Expect(201).Data(&h)
		var o struct{ ID string }
		u.IdemPOST(api+"/orders", "par-ord-"+strconv.Itoa(i)+"-"+newID(), M{"hold_id": h.ID}).Expect(201).Data(&o)
		var p struct {
			PaymentID string `json:"payment_id"`
		}
		u.IdemPOST(api+"/orders/"+o.ID+"/pay", "par-pay-"+strconv.Itoa(i)+"-"+newID(), nil).Expect(200).Data(&p)
		pending = append(pending, po{o.ID, e.QueryStr("SELECT provider_ref FROM payments WHERE id=$1", p.PaymentID)})
	}
	var wg sync.WaitGroup
	for _, p := range pending {
		wg.Add(1)
		go func(p po) {
			defer wg.Done()
			e.Anon().Do("POST", "/webhooks/midtrans", e.Gateway.Settle(p.ref, "gopay")).Expect(200)
		}(p)
	}
	wg.Wait()
	rows, err := pool.Query(context.Background(), "SELECT seq FROM invoices ORDER BY seq")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var seqs []int
	for rows.Next() {
		var s int
		_ = rows.Scan(&s)
		seqs = append(seqs, s)
	}
	sort.Ints(seqs)
	if len(seqs) != N {
		t.Fatalf("invoice: %d, harus %d", len(seqs), N)
	}
	for i, s := range seqs {
		if s != i+1 {
			t.Fatalf("nomor harus 1..%d tanpa celah: %v", N, seqs)
		}
	}
	e.ledgerBalanced()
}

func TestParallelPayoutRequestsNeverExceedBalance(t *testing.T) { // T30
	e := NewEnv(t)
	o, evID, slug, tier := e.publishedGA(100000, 30)
	e.seedBank(o.ID)
	for i := 0; i < 5; i++ {
		e.buyPaid(e.Verified("Pembeli"), slug, tier, 1, "gopay")
	}
	e.Exec("UPDATE events SET status='finished', starts_at=now()-interval '10 days', ends_at=now()-interval '9 days' WHERE id=$1", evID)
	_, _ = e.Svc.Finance.ReleaseSettlements(context.Background())
	var bal struct{ Available int64 }
	o.C.GET(api + "/organizer/finance/balance").Data(&bal)
	each := bal.Available * 6 / 10
	var okN atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := o.C.POST("/api/v1/organizer/finance/payouts", M{"amount": each}, "Idempotency-Key", "par-payout-"+strconv.Itoa(i)+"-"+newID())
			if r.Code == 201 {
				okN.Add(1)
			} else if r.Code != 422 {
				t.Errorf("status tak terduga %d: %s", r.Code, r.Body)
			}
		}(i)
	}
	wg.Wait()
	if okN.Load() != 1 {
		t.Fatalf("tepat satu pencairan 60%% yang lolos: %d", okN.Load())
	}
	e.ledgerBalanced()
}

func TestAttendeeExportDeniedForOtherOrganizer(t *testing.T) { // T34
	e := NewEnv(t)
	_, evID, slug, tier := e.publishedGA(100000, 5)
	e.buyPaid(e.Verified("Peserta"), slug, tier, 1, "gopay")
	o2 := e.NewOrganizer(true)
	o2.C.GET(api+"/organizer/events/"+evID+"/attendees.csv").ExpectErr(404, "NOT_FOUND")
	o2.C.GET(api+"/organizer/events/"+evID+"/attendees").ExpectErr(404, "NOT_FOUND")
	if e.QueryInt("SELECT count(*) FROM audit_logs WHERE action='attendees.export'") != 0 {
		t.Fatal("tidak ada ekspor yang berhasil")
	}
}

func TestRefundIssuesCreditNoteAndSnapshotPolicyIsNotRetroactive(t *testing.T) { // T44, T48
	e := NewEnv(t)
	_, evID, slug, tier := e.publishedGA(100000, 5)
	u := e.Verified("Snapshot")
	oid := e.buyPaid(u, slug, tier, 1, "gopay") // dibeli saat non-refundable
	// organizer mengubah kebijakan menjadi refundable SETELAH penjualan
	e.Exec(`UPDATE events SET refund_policy=$2::jsonb WHERE id=$1`, evID, `{"type":"refundable","refundable_until":"`+e.Clock.Now().Add(240*time.Hour).Format(time.RFC3339)+`","deduction_pct":0,"fee_refundable":true,"on_reschedule":{"window_days":14}}`)
	var el struct{ Eligible bool }
	u.GET(api + "/orders/" + oid + "/refund-eligibility").Expect(200).Data(&el)
	if el.Eligible {
		t.Fatal("order lama tetap memakai snapshot non-refundable (T48)")
	}
	// order baru memakai kebijakan baru → refund menerbitkan credit note + ledger seimbang (T44)
	oid2 := e.buyPaid(u, slug, tier, 1, "gopay")
	u.IdemPOST(api+"/orders/"+oid2+"/refunds", "snap-refund-01", M{}).Expect(201)
	if e.QueryInt("SELECT count(*) FROM credit_notes") != 1 {
		t.Fatal("credit note terbit")
	}
	e.ledgerBalanced()
}

func TestRefundGatewayRejectedFallsBackToManualExplicitly(t *testing.T) { // T49
	e := NewEnv(t)
	_, evID, slug, tier := e.publishedGA(100000, 5)
	e.Exec(`UPDATE events SET refund_policy=$2::jsonb WHERE id=$1`, evID, `{"type":"refundable","refundable_until":"`+e.Clock.Now().Add(240*time.Hour).Format(time.RFC3339)+`","deduction_pct":0,"fee_refundable":true,"on_reschedule":{"window_days":14}}`)
	u := e.Verified("Gagal Gateway")
	oid := e.buyPaid(u, slug, tier, 1, "gopay")
	e.Gateway.FailNext.Refund = payment.ErrRefundRejected
	var rf struct {
		ID           string
		Status       string
		NeedsAccount bool `json:"needs_account"`
	}
	u.IdemPOST(api+"/orders/"+oid+"/refunds", "gw-fail-0001", M{}).Expect(201).Data(&rf)
	if e.QueryStr("SELECT status FROM refunds WHERE id=$1", rf.ID) != "awaiting_account" || e.QueryStr("SELECT method FROM refunds WHERE id=$1", rf.ID) != "manual" {
		t.Fatalf("ditolak gateway → manual, tercatat: %s/%s", e.QueryStr("SELECT status FROM refunds WHERE id=$1", rf.ID), e.QueryStr("SELECT method FROM refunds WHERE id=$1", rf.ID))
	}
	if e.QueryInt("SELECT count(*) FROM audit_logs WHERE action='refund.fallback_manual'") != 1 {
		t.Fatal("pengalihan tercatat di audit")
	}
	if len(e.Mail.MailsTo(u.Email)) < 2 {
		t.Fatal("pembeli diberi tahu untuk mengisi rekening")
	}
}

func TestPerAccountLimitRejectsWith422(t *testing.T) { // T15
	e := NewEnv(t)
	_, _, slug, tier := e.publishedGA(50000, 20) // max_per_account default 4, max_per_order 6
	u := e.Verified("Borong")
	u.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 7}).ExpectErr(422, "LIMIT_EXCEEDED") // > per pesanan
	e.buyPaid(u, slug, tier, 3, "gopay")
	u.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 2}).ExpectErr(422, "LIMIT_EXCEEDED") // 3 + 2 > 4
	u.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 1}).Expect(201)                      // 3 + 1 = 4 masih boleh
}

func TestManualRefundOverdueEscalatesOnceAndMoneyIsNotLost(t *testing.T) { // M17 AC6
	e := NewEnv(t)
	_, evID, slug, tier := e.publishedGA(100000, 5)
	e.Exec(`UPDATE events SET refund_policy=$2::jsonb WHERE id=$1`, evID, `{"type":"refundable","refundable_until":"`+e.Clock.Now().Add(240*time.Hour).Format(time.RFC3339)+`","deduction_pct":0,"fee_refundable":true,"on_reschedule":{"window_days":14}}`)
	u := e.Verified("Lupa Rekening")
	oid := e.buyPaid(u, slug, tier, 1, "bank_transfer")
	var rf struct{ ID string }
	u.IdemPOST(api+"/orders/"+oid+"/refunds", "overdue-0001", M{}).Expect(201).Data(&rf)
	if n, _ := e.Svc.Refund.EscalateOverdue(t.Context()); n != 0 {
		t.Fatal("belum lewat tenggat")
	}
	e.Clock.Advance(31 * 24 * time.Hour)
	if n, err := e.Svc.Refund.EscalateOverdue(t.Context()); err != nil || n != 1 {
		t.Fatalf("eskalasi: %d %v", n, err)
	}
	if n, _ := e.Svc.Refund.EscalateOverdue(t.Context()); n != 0 {
		t.Fatal("idempoten: tidak eskalasi dua kali")
	}
	if e.QueryInt("SELECT count(*) FROM recon_exceptions WHERE kind='refund_account_overdue' AND status<>'resolved'") != 1 {
		t.Fatal("pengecualian terbuka untuk finance")
	}
	if e.QueryStr("SELECT status FROM refunds WHERE id=$1", rf.ID) != "awaiting_account" {
		t.Fatal("refund tetap menunggu — dana tidak dibatalkan")
	}
}
