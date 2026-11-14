package apitest

import (
	"testing"
	"time"
)

func TestWaitlistFIFOOfferAndPassOnTimeout(t *testing.T) { // M15
	e := NewEnv(t)
	_, evID, slug, tier := e.publishedGA(100000, 2)
	e.Exec("UPDATE events SET refund_policy=$2::jsonb WHERE id=$1", evID, `{"type":"refundable","refundable_until":"`+e.Clock.Now().Add(240*time.Hour).Format(time.RFC3339)+`","deduction_pct":0,"fee_refundable":true,"on_reschedule":{"window_days":14}}`)
	buyer := e.Verified("Pembeli Awal")
	oid := e.buyPaid(buyer, slug, tier, 2, "gopay") // habis
	first := e.Verified("Antrean Satu")
	second := e.Verified("Antrean Dua")
	late := e.Verified("Beli Langsung")

	body := M{"tier_id": tier, "qty": 1}
	// masih ada stok → disuruh beli langsung (setelah habis diuji di bawah)
	first.POST(api+"/events/"+slug+"/waitlist", body).Expect(201)
	first.POST(api+"/events/"+slug+"/waitlist", body).Expect(201) // idempoten (satu entri)
	second.POST(api+"/events/"+slug+"/waitlist", body).Expect(201)
	if n := e.QueryInt("SELECT count(*) FROM waitlists WHERE status='waiting'"); n != 2 {
		t.Fatalf("2 entri menunggu: %d", n)
	}
	// stok kembali (refund) → penawaran FIFO: hanya yang pertama mendapat hold; yang kedua belum
	buyer.IdemPOST(api+"/orders/"+oid+"/refunds", "wl-refund-0001", M{}).Expect(201)
	var wl []struct {
		Status string
		HoldID *string `json:"hold_id"`
	}
	first.GET(api + "/waitlists").Expect(200).Data(&wl)
	if len(wl) != 1 || wl[0].Status != "offered" || wl[0].HoldID == nil {
		t.Fatalf("yang pertama ditawari: %+v", wl)
	}
	// 2 kursi kembali → dua orang pertama keduanya mendapat 1 tiket masing-masing
	second.GET(api + "/waitlists").Expect(200).Data(&wl)
	if wl[0].Status != "offered" {
		t.Fatalf("stok 2 cukup untuk dua entri: %+v", wl)
	}
	// stok yang ditawarkan tidak bisa direbut orang lain
	late.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 1}).ExpectErr(409, "SOLD_OUT")
	// penerima menyelesaikan pembelian dari hold penawaran
	first.GET(api + "/waitlists").Data(&wl)
	var ord struct{ ID string }
	first.IdemPOST(api+"/orders", "wl-order-0001", M{"hold_id": *wl[0].HoldID}).Expect(201).Data(&ord)
	if e.QueryStr("SELECT status FROM waitlists WHERE user_id=$1", first.UserID) != "accepted" {
		t.Fatal("penawaran diterima setelah order dibuat")
	}
	// yang kedua mengabaikan → hold habis → entri kedaluwarsa & stok kembali
	e.Clock.Advance(30 * time.Minute)
	if _, err := e.Svc.Hold.ExpireDue(t.Context()); err != nil {
		t.Fatal(err)
	}
	if e.QueryStr("SELECT status FROM waitlists WHERE user_id=$1", second.UserID) == "offered" {
		t.Fatal("penawaran harus berakhir setelah jendela habis")
	}
	late = e.Login(late.Email)
	var lh struct{ ID string }
	late.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 1}).Expect(201).Data(&lh) // stok bebas lagi
	late.DELETE(api + "/holds/" + lh.ID).Expect(204)
	// bergabung saat stok tersedia ditolak
	other := e.Verified("Terlambat Gabung")
	other.POST(api+"/events/"+slug+"/waitlist", body).ExpectErr(409, "INVALID_STATE")
}
