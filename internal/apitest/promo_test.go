package apitest

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestPromoDiscountAndReport(t *testing.T) {
	e := NewEnv(t)
	o, evID, slug, tier := e.publishedGA(100000, 50)
	var pr struct{ ID string }
	o.C.POST(api+"/organizer/events/"+evID+"/promos", M{"code": "hemat10", "kind": "percent", "value": 10, "max_per_account": 1}).Expect(201).Data(&pr)
	o.C.POST(api+"/organizer/events/"+evID+"/promos", M{"code": "hemat10", "kind": "percent", "value": 10}).ExpectErr(409, "CONFLICT")
	o.C.POST(api+"/organizer/events/"+evID+"/promos", M{"code": "x", "kind": "percent", "value": 10}).ExpectErr(400, "VALIDATION_FAILED")
	o.C.POST(api+"/organizer/events/"+evID+"/promos", M{"code": "GEDE", "kind": "percent", "value": 150}).ExpectErr(400, "VALIDATION_FAILED")

	u := e.Verified("Promo")
	var hold struct{ ID string }
	u.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 2}).Expect(201).Data(&hold)
	var q struct {
		Discount int64 `json:"discount"`
		Subtotal int64 `json:"subtotal"`
	}
	u.POST(api+"/orders/quote", M{"hold_id": hold.ID, "promo_code": "HEMAT10"}).Expect(200).Data(&q)
	if q.Discount != 20000 || q.Subtotal != 200000 {
		t.Fatalf("diskon 10%% dari 200000: %+v", q)
	}
	u.POST(api+"/orders/quote", M{"hold_id": hold.ID, "promo_code": "SALAH"}).ExpectErr(400, "PROMO_INVALID")
	var ord struct{ ID string }
	u.IdemPOST(api+"/orders", "promo-order-01", M{"hold_id": hold.ID, "promo_code": "HEMAT10"}).Expect(201).Data(&ord)
	var pay struct {
		PaymentID string `json:"payment_id"`
	}
	u.IdemPOST(api+"/orders/"+ord.ID+"/pay", "promo-pay-0001", nil).Expect(200).Data(&pay)
	e.Anon().Do("POST", "/webhooks/midtrans", e.Gateway.Settle(e.QueryStr("SELECT provider_ref FROM payments WHERE id=$1", pay.PaymentID), "gopay")).Expect(200)
	// satu akun sekali pakai
	var h2 struct{ ID string }
	u.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 1}).Expect(201).Data(&h2)
	u.IdemPOST(api+"/orders", "promo-order-02", M{"hold_id": h2.ID, "promo_code": "HEMAT10"}).ExpectErr(409, "PROMO_EXHAUSTED")
	var rep struct {
		Redemptions   int64
		DiscountTotal int64 `json:"discount_total"`
	}
	o.C.GET(api + "/organizer/events/" + evID + "/promos/" + pr.ID + "/report").Expect(200).Data(&rep)
	if rep.Redemptions != 1 || rep.DiscountTotal != 20000 {
		t.Fatalf("laporan: %+v", rep)
	}
}

func TestPromoQuotaNeverExceededUnderConcurrency(t *testing.T) { // T-promo
	e := NewEnv(t)
	o, evID, slug, tier := e.publishedGA(100000, 100)
	o.C.POST(api+"/organizer/events/"+evID+"/promos", M{"code": "TERBATAS", "kind": "fixed", "value": 5000, "max_uses": 5}).Expect(201)
	const N = 20
	var ok atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		u := e.Verified("Rebut")
		var hold struct{ ID string }
		u.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 1}).Expect(201).Data(&hold)
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := u.IdemPOST(api+"/orders", "conc-promo-"+newID(), M{"hold_id": hold.ID, "promo_code": "TERBATAS"})
			if r.Code == 201 {
				ok.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if ok.Load() != 5 {
		t.Fatalf("hanya 5 order yang boleh memakai promo: %d", ok.Load())
	}
	if n := e.QueryInt("SELECT used FROM promo_codes WHERE code='TERBATAS'"); n != 5 {
		t.Fatalf("used=%d", n)
	}
}

func TestVoucherWallet(t *testing.T) {
	e := NewEnv(t)
	o, evID, slug, tier := e.publishedGA(100000, 50)
	o.C.POST(api+"/organizer/events/"+evID+"/promos", M{"code": "dompet15", "kind": "percent", "value": 15, "max_per_account": 1}).Expect(201)
	u := e.Verified("Dompet")
	var got []struct {
		Code, Status string
		EventSlug    string `json:"event_slug"`
		PromoID      string `json:"promo_id"`
	}
	u.POST(api+"/me/vouchers", M{"code": "tidak-ada"}).ExpectErr(400, "PROMO_INVALID")
	u.POST(api+"/me/vouchers", M{"code": "!!"}).ExpectErr(400, "PROMO_INVALID")
	u.POST(api+"/me/vouchers", M{"code": " dompet15 "}).Expect(200).Data(&got)
	if len(got) != 1 || got[0].Code != "DOMPET15" || got[0].Status != "available" || got[0].EventSlug != slug {
		t.Fatalf("tukar: %+v", got)
	}
	u.POST(api+"/me/vouchers", M{"code": "DOMPET15"}).Expect(200) // idempoten
	u.GET(api + "/me/vouchers").Expect(200).Data(&got)
	if len(got) != 1 {
		t.Fatalf("daftar harus 1: %+v", got)
	}
	// pakai di order -> status 'used'
	var hold, ord struct{ ID string }
	u.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 1}).Expect(201).Data(&hold)
	u.IdemPOST(api+"/orders", "vch-order-01", M{"hold_id": hold.ID, "promo_code": "DOMPET15"}).Expect(201).Data(&ord)
	u.GET(api + "/me/vouchers").Expect(200).Data(&got)
	if got[0].Status != "used" {
		t.Fatalf("setelah dipakai harus used: %+v", got[0])
	}
	// akun lain tidak melihatnya; hapus
	e.Verified("Lain").GET(api + "/me/vouchers").Expect(200).Data(&got)
	if len(got) != 0 {
		t.Fatalf("isolasi akun: %+v", got)
	}
	u.DELETE(api+"/me/vouchers/"+newID()).ExpectErr(404, "NOT_FOUND")
	e.Anon().Do("GET", api+"/me/vouchers", nil).Expect(401)
}
