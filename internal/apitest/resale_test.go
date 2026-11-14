package apitest

import (
	"testing"
	"time"
)

func resaleEvent(t *testing.T, e *Env, capPct int) (o *Org, evID, slug, tier string) {
	t.Helper()
	o, evID, slug, tier = e.publishedGA(100000, 20)
	o.C.PUT(api+"/organizer/events/"+evID+"/ticket-policy", M{"transfer_enabled": true, "resale_cap_pct": capPct}).Expect(200)
	return
}

func TestResaleCapListingRulesAndPublicView(t *testing.T) {
	e := NewEnv(t)
	o, evID, slug, tier := resaleEvent(t, e, 120)
	seller := e.Verified("Penjual")
	e.buyPaid(seller, slug, tier, 1, "gopay")
	tid := ticketIDs(t, seller)[0]
	seller.POST(api+"/tickets/"+tid+"/resale", M{"price": 130000}).ExpectErr(422, "VALIDATION_FAILED") // di atas plafon 120%
	seller.POST(api+"/tickets/"+tid+"/resale", M{"price": 500}).ExpectErr(400, "VALIDATION_FAILED")
	var l struct {
		ID, Status string
		BuyerPays  int64 `json:"buyer_pays"`
		BuyerFee   int64 `json:"buyer_fee"`
	}
	seller.POST(api+"/tickets/"+tid+"/resale", M{"price": 110000}).Expect(201).Data(&l)
	if l.BuyerFee != 5500 || l.BuyerPays != 115500 {
		t.Fatalf("biaya jelas 5%%: %+v", l)
	}
	seller.POST(api+"/tickets/"+tid+"/resale", M{"price": 100000}).ExpectErr(409, "CONFLICT")
	// tiket yang sedang dijual tidak bisa dikirim lewat tautan & tidak bisa direfund
	seller.POST(api+"/tickets/"+tid+"/transfer", M{}).ExpectErr(409, "CONFLICT")
	// publik melihat listing tanpa identitas penjual
	var pub struct {
		CapPct   int `json:"cap_pct"`
		Listings []map[string]any
	}
	e.Anon().GET(api + "/events/" + slug + "/resale").Expect(200).Data(&pub)
	if pub.CapPct != 120 || len(pub.Listings) != 1 || pub.Listings[0]["seller_id"] != nil {
		t.Fatalf("listing publik: %+v", pub)
	}
	// event tanpa resale
	_, _, slug2, tier2 := e.publishedGA(100000, 10)
	other := e.Verified("Penjual Dua")
	e.buyPaid(other, slug2, tier2, 1, "gopay")
	other.POST(api+"/tickets/"+ticketIDs(t, other)[0]+"/resale", M{"price": 100000}).ExpectErr(422, "NOT_ALLOWED")
	// batal listing
	seller.DELETE(api + "/resale/listings/" + l.ID).Expect(204)
	seller.DELETE(api+"/resale/listings/"+l.ID).ExpectErr(409, "INVALID_STATE")
	// pembeli lain tak bisa menjual tiket orang
	stranger := e.Verified("Orang Asing")
	stranger.POST(api+"/tickets/"+tid+"/resale", M{"price": 100000}).ExpectErr(404, "NOT_FOUND")
	_ = o
	_ = evID
}

func TestResalePurchaseSettlesTransfersTicketAndPaysSellerManually(t *testing.T) {
	e := NewEnv(t)
	o, evID, slug, tier := resaleEvent(t, e, 120)
	seller := e.Verified("Penjual Tiket")
	buyer := e.Verified("Pembeli Resale")
	rival := e.Verified("Pembeli Lain")
	e.buyPaid(seller, slug, tier, 1, "gopay")
	oldTicket := ticketIDs(t, seller)[0]
	var oldQR qr
	seller.GET(api + "/tickets/" + oldTicket + "/qr").Expect(200).Data(&oldQR)
	var l struct{ ID string }
	seller.POST(api+"/tickets/"+oldTicket+"/resale", M{"price": 110000}).Expect(201).Data(&l)

	var ord struct {
		ID    string
		Quote struct{ Total int64 }
	}
	buyer.IdemPOST(api+"/resale/listings/"+l.ID+"/buy", "resale-buy-0001", nil).Expect(201).Data(&ord)
	if ord.Quote.Total != 115500 {
		t.Fatalf("total pembeli: %d", ord.Quote.Total)
	}
	rival.IdemPOST(api+"/resale/listings/"+l.ID+"/buy", "resale-buy-0002", nil).ExpectErr(409, "SEAT_TAKEN")
	seller.IdemPOST(api+"/resale/listings/"+l.ID+"/buy", "resale-buy-0003", nil).ExpectErr(409, "SEAT_TAKEN") // sudah dipesan
	seller.DELETE(api+"/resale/listings/"+l.ID).ExpectErr(409, "INVALID_STATE")

	var pay struct {
		PaymentID string `json:"payment_id"`
	}
	buyer.IdemPOST(api+"/orders/"+ord.ID+"/pay", "resale-pay-0001", nil).Expect(200).Data(&pay)
	ref := e.QueryStr("SELECT provider_ref FROM payments WHERE id=$1", pay.PaymentID)
	e.Anon().Do("POST", "/webhooks/midtrans", e.Gateway.Settle(ref, "gopay")).Expect(200)
	e.Anon().Do("POST", "/webhooks/midtrans", e.Gateway.Settle(ref, "gopay")).Expect(200) // ganda

	if e.QueryStr("SELECT status FROM orders WHERE id=$1", ord.ID) != "paid" {
		t.Fatal("order resale lunas")
	}
	var vs, vb []map[string]any
	seller.GET(api + "/tickets?status=valid").Expect(200).Data(&vs)
	buyer.GET(api + "/tickets?status=valid").Expect(200).Data(&vb)
	if len(vs) != 0 || len(vb) != 1 {
		t.Fatalf("kepemilikan pindah ke pembeli: seller=%d buyer=%d", len(vs), len(vb))
	}
	if e.QueryStr("SELECT status FROM tickets WHERE id=$1", oldTicket) != "transferred" {
		t.Fatal("tiket lama transferred")
	}
	if e.QueryInt("SELECT count(*) FROM tickets WHERE event_id=$1", evID) != 2 {
		t.Fatal("tiket baru diterbitkan")
	}
	// QR lama tidak sah; QR baru sah
	var r struct{ Result string }
	o.C.POST(api+"/gate/scan", M{"payload": oldQR.Payload, "event_id": evID}).Expect(200).Data(&r)
	if r.Result == "OK" {
		t.Fatalf("QR penjual tidak boleh lolos: %s", r.Result)
	}
	newT := vb[0]["id"].(string)
	// pembeli tidak bisa menaikkan harga berantai: plafon dihitung dari harga asli 100000 (bukan 110000)
	buyer.POST(api+"/tickets/"+newT+"/resale", M{"price": 125000}).ExpectErr(422, "VALIDATION_FAILED")
	var l2 struct{ ID string }
	buyer.POST(api+"/tickets/"+newT+"/resale", M{"price": 120000}).Expect(201).Data(&l2)
	buyer.DELETE(api + "/resale/listings/" + l2.ID).Expect(204)
	var nq qr
	buyer.GET(api + "/tickets/" + newT + "/qr").Expect(200).Data(&nq)
	o.C.POST(api+"/gate/scan", M{"payload": nq.Payload, "event_id": evID}).Expect(200).Data(&r)
	if r.Result != "OK" {
		t.Fatalf("QR pembeli: %s", r.Result)
	}
	e.ledgerBalanced()
	// organizer tidak menerima apa pun dari resale (saldo pending tetap dari penjualan primer saja)
	if e.QueryInt("SELECT coalesce(sum(credit-debit),0) FROM ledger_entries le JOIN ledger_accounts la ON la.id=le.account_id WHERE la.kind='resale_payable'") != 110000 {
		t.Fatal("utang ke penjual = harga listing")
	}
	// pembayaran ke penjual: isi rekening → finance bayar dengan bukti
	var pos []struct {
		ID     string
		Status string
	}
	seller.GET(api + "/me/resale/payouts").Expect(200).Data(&pos)
	if len(pos) != 1 || pos[0].Status != "awaiting_account" {
		t.Fatalf("payout penjual: %+v", pos)
	}
	fin := e.UserWithRole("finance", "Finance Resale")
	fin.Upload(api+"/admin/finance/resale-payouts/"+pos[0].ID+"/mark-paid", nil, "proof", "b.png", pngBytes(8, 8)).ExpectErr(409, "INVALID_STATE") // rekening belum diisi
	seller.PUT(api+"/resale/payouts/"+pos[0].ID+"/account", M{"bank_code": "bca", "account_name": "Penjual", "account_no": "12345678"}).Expect(204)
	buyer.PUT(api+"/resale/payouts/"+pos[0].ID+"/account", M{"bank_code": "bca", "account_name": "Xavier", "account_no": "12345678"}).ExpectErr(404, "NOT_FOUND")
	var fl []map[string]any
	fin.GET(api + "/admin/finance/resale-payouts?status=ready").Expect(200).Data(&fl)
	if len(fl) != 1 || fl[0]["account_no"] != "12345678" {
		t.Fatalf("finance melihat rekening: %v", fl)
	}
	fin.Upload(api+"/admin/finance/resale-payouts/"+pos[0].ID+"/mark-paid", nil, "proof", "b.png", pngBytes(8, 8)).Expect(204)
	if e.QueryInt("SELECT coalesce(sum(credit-debit),0) FROM ledger_entries le JOIN ledger_accounts la ON la.id=le.account_id WHERE la.kind='resale_payable'") != 0 {
		t.Fatal("utang lunas")
	}
	e.ledgerBalanced()
}

func TestResaleReservationExpiryReleasesListingAndLatePaymentRefunds(t *testing.T) {
	e := NewEnv(t)
	_, _, slug, tier := resaleEvent(t, e, 120)
	seller := e.Verified("Penjual Cepat")
	buyer := e.Verified("Pembeli Lambat")
	e.buyPaid(seller, slug, tier, 1, "gopay")
	var l struct{ ID string }
	seller.POST(api+"/tickets/"+ticketIDs(t, seller)[0]+"/resale", M{"price": 100000}).Expect(201).Data(&l)
	var ord struct{ ID string }
	buyer.IdemPOST(api+"/resale/listings/"+l.ID+"/buy", "resale-buy-exp1", nil).Expect(201).Data(&ord)
	var pay struct {
		PaymentID string `json:"payment_id"`
	}
	buyer.IdemPOST(api+"/orders/"+ord.ID+"/pay", "resale-pay-exp1", nil).Expect(200).Data(&pay)
	e.Clock.Advance(15 * time.Minute)
	if _, err := e.Svc.Order.ExpireDue(t.Context()); err != nil {
		t.Fatal(err)
	}
	if e.QueryStr("SELECT status FROM resale_listings WHERE id=$1", l.ID) != "active" {
		t.Fatal("listing kembali aktif setelah order kedaluwarsa")
	}
	buyer = e.Login(buyer.Email)
	// pembeli lain mengambil listing; pembeli lambat membayar belakangan → refund otomatis (tiket tidak boleh dobel)
	rival := e.Verified("Pembeli Cepat")
	var ord2 struct{ ID string }
	rival.IdemPOST(api+"/resale/listings/"+l.ID+"/buy", "resale-buy-exp2", nil).Expect(201).Data(&ord2)
	ref := e.QueryStr("SELECT provider_ref FROM payments WHERE id=$1", pay.PaymentID)
	e.Anon().Do("POST", "/webhooks/midtrans", e.Gateway.Settle(ref, "gopay")).Expect(200)
	if e.QueryStr("SELECT status FROM orders WHERE id=$1", ord.ID) == "paid" {
		t.Fatal("pembayaran terlambat tidak boleh meluluskan order yang listing-nya sudah dipesan orang lain")
	}
	if e.QueryInt("SELECT count(*) FROM refunds WHERE order_id=$1", ord.ID) != 1 {
		t.Fatal("refund otomatis untuk pembayaran terlambat")
	}
	var vb []map[string]any
	buyer.GET(api + "/tickets?status=valid").Expect(200).Data(&vb)
	if len(vb) != 0 {
		t.Fatal("pembeli lambat tidak menerima tiket")
	}
}
