package apitest

import (
	"testing"
	"time"
)

func squadOrderPay(t *testing.T, e *Env, u *Client, squadID string) string {
	t.Helper()
	var res struct {
		Order struct{ ID string }
	}
	u.IdemPOST(api+"/squads/"+squadID+"/pay", "sq-pay-"+newID(), M{}).Expect(201).Data(&res)
	var pay struct {
		PaymentID string `json:"payment_id"`
	}
	u.IdemPOST(api+"/orders/"+res.Order.ID+"/pay", "sq-op-"+newID(), nil).Expect(200).Data(&pay)
	e.Anon().Do("POST", "/webhooks/midtrans", e.Gateway.Settle(e.QueryStr("SELECT provider_ref FROM payments WHERE id=$1", pay.PaymentID), "gopay")).Expect(200)
	return res.Order.ID
}

func TestSquadSeatedAllPayCompletes(t *testing.T) {
	e := NewEnv(t)
	_, evID, slug, tier := e.publishedSeated(3, 6)
	// squad 3 kursi berdekatan; ketua tidak dibatasi per-akun (max_per_account=4 default) untuk seluruh ukuran squad
	leader := e.Verified("Ketua Squad")
	m1 := e.Verified("Anggota Satu")
	m2 := e.Verified("Anggota Dua")
	var sg struct {
		SeatIDs []string `json:"seat_ids"`
	}
	e.Anon().GET(api + "/events/" + slug + "/seats/suggest?tier_id=" + tier + "&qty=3").Expect(200).Data(&sg)
	var sq struct {
		ID, Code string
		Open     int `json:"open_slots"`
	}
	leader.POST(api+"/events/"+slug+"/squads", M{"size": 3, "policy": "release_unpaid", "seat_ids": sg.SeatIDs[:2]}).ExpectErr(400, "VALIDATION_FAILED")
	leader.POST(api+"/events/"+slug+"/squads", M{"size": 3, "policy": "release_unpaid", "seat_ids": sg.SeatIDs}).Expect(201).Data(&sq)
	if sq.Open != 2 {
		t.Fatalf("slot terbuka: %d", sq.Open)
	}
	// orang luar tak bisa melihat; kode salah 404
	m1.GET(api+"/squads/"+sq.ID).ExpectErr(404, "NOT_FOUND")
	m1.POST(api+"/squads/join", M{"code": "SALAHKODE"}).ExpectErr(404, "NOT_FOUND")
	m1.POST(api+"/squads/join", M{"code": sq.Code}).Expect(200)
	m1.POST(api+"/squads/join", M{"code": sq.Code}).Expect(200) // idempoten
	m2.POST(api+"/squads/join", M{"code": sq.Code}).Expect(200)
	extra := e.Verified("Anggota Lebih")
	extra.POST(api+"/squads/join", M{"code": sq.Code}).ExpectErr(409, "LIMIT_EXCEEDED")

	if n := e.QueryInt("SELECT count(*) FROM seats WHERE event_id=$1 AND status='held'", evID); n != 3 {
		t.Fatalf("3 kursi ditahan: %d", n)
	}
	squadOrderPay(t, e, m1, sq.ID)
	var v struct {
		Status string
		Paid   int
	}
	leader.GET(api + "/squads/" + sq.ID).Expect(200).Data(&v)
	if v.Status != "holding" || v.Paid != 1 {
		t.Fatalf("1 dari 3 lunas: %+v", v)
	}
	squadOrderPay(t, e, m2, sq.ID)
	squadOrderPay(t, e, leader, sq.ID)
	leader.GET(api + "/squads/" + sq.ID).Expect(200).Data(&v)
	if v.Status != "completed" || v.Paid != 3 {
		t.Fatalf("semua lunas → completed: %+v", v)
	}
	// tiap anggota memegang tiketnya sendiri atas kursi yang ditetapkan
	for _, u := range []*Client{leader, m1, m2} {
		var tk []map[string]any
		u.GET(api + "/tickets?status=valid").Expect(200).Data(&tk)
		if len(tk) != 1 {
			t.Fatalf("%s: tiket %d", u.Email, len(tk))
		}
	}
	if n := e.QueryInt("SELECT count(*) FROM seats WHERE event_id=$1 AND status='sold'", evID); n != 3 {
		t.Fatalf("3 kursi terjual: %d", n)
	}
	e.ledgerBalanced()
	// sudah bayar → tidak bisa bayar lagi
	m1.IdemPOST(api+"/squads/"+sq.ID+"/pay", "sq-again-0001", M{}).ExpectErr(409, "INVALID_STATE")
}

func TestSquadReleaseUnpaidVsCancelAllOnTimeout(t *testing.T) {
	for _, policy := range []string{"release_unpaid", "cancel_all"} {
		policy := policy
		t.Run(policy, func(t *testing.T) {
			e := NewEnv(t)
			_, evID, slug, tier := e.publishedGA(100000, 20)
			leader := e.Verified("Ketua GA")
			m1 := e.Verified("Anggota GA")
			var sq struct{ ID, Code string }
			leader.POST(api+"/events/"+slug+"/squads", M{"size": 3, "policy": policy, "tier_id": tier}).Expect(201).Data(&sq)
			if e.QueryInt("SELECT held FROM tiers WHERE id=$1", tier) != 3 {
				t.Fatal("3 kuota ditahan")
			}
			m1.POST(api+"/squads/join", M{"code": sq.Code}).Expect(200)
			squadOrderPay(t, e, m1, sq.ID) // hanya 1 yang bayar
			e.Clock.Advance(20 * time.Minute)
			if _, err := e.Svc.Hold.ExpireDue(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, err := e.Svc.Order.ExpireDue(t.Context()); err != nil {
				t.Fatal(err)
			}
			e.Clock.Advance(2 * time.Minute)
			if n, err := e.Svc.Squad.Sweep(t.Context()); err != nil || n != 1 {
				t.Fatalf("sweep: %d %v", n, err)
			}
			switch policy {
			case "release_unpaid":
				if e.QueryStr("SELECT status FROM squads") != "expired" {
					t.Fatal("expired")
				}
				if e.QueryInt("SELECT held FROM tiers WHERE id=$1", tier) != 0 || e.QueryInt("SELECT sold FROM tiers WHERE id=$1", tier) != 1 {
					t.Fatalf("bagian yang belum bayar dilepas; yang bayar tetap: held/sold salah")
				}
				var tk []map[string]any
				m1 = e.Login(m1.Email)
				m1.GET(api + "/tickets?status=valid").Expect(200).Data(&tk)
				if len(tk) != 1 {
					t.Fatal("anggota yang bayar tetap punya tiket")
				}
			case "cancel_all":
				if e.QueryStr("SELECT status FROM squads") != "cancelled" {
					t.Fatal("cancelled")
				}
				if e.QueryInt("SELECT count(*) FROM refunds WHERE initiated_by='admin' AND scope='order'") != 1 {
					t.Fatal("yang sudah bayar di-refund otomatis")
				}
				if e.QueryStr("SELECT status FROM orders WHERE status IN ('refunded','paid') LIMIT 1") != "refunded" {
					t.Fatal("order refunded")
				}
			}
			e.ledgerBalanced()
			_ = evID
		})
	}
}

func TestSquadLeaderCancelAndExtendOnce(t *testing.T) {
	e := NewEnv(t)
	_, _, slug, tier := e.publishedGA(100000, 20)
	leader := e.Verified("Ketua Batal")
	var sq struct{ ID string }
	leader.POST(api+"/events/"+slug+"/squads", M{"size": 4, "policy": "release_unpaid", "tier_id": tier}).Expect(201).Data(&sq)
	leader.POST(api+"/squads/"+sq.ID+"/extend", nil).Expect(200)
	leader.POST(api+"/squads/"+sq.ID+"/extend", nil).ExpectErr(409, "INVALID_STATE")
	leader.POST(api+"/squads/"+sq.ID+"/invite", M{"email": "teman@example.com"}).Expect(204)
	if m, ok := e.Mail.LastMailTo("teman@example.com"); !ok || m.Subject == "" {
		t.Fatal("email undangan")
	}
	leader.DELETE(api + "/squads/" + sq.ID).Expect(204)
	if e.QueryInt("SELECT held FROM tiers WHERE id=$1", tier) != 0 {
		t.Fatal("kuota kembali setelah dibatalkan")
	}
	leader.DELETE(api+"/squads/"+sq.ID).ExpectErr(409, "INVALID_STATE")
}
