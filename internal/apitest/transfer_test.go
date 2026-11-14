package apitest

import (
	"strings"
	"testing"
	"time"
)

func claimToken(link string) string { return link[strings.LastIndex(link, "/claim/")+len("/claim/"):] }

func TestTicketTransferClaimRotatesSecretAndBlocksOldQR(t *testing.T) {
	e := NewEnv(t)
	o, evID, slug, tier := e.publishedGA(100000, 10)
	a := e.Verified("Pengirim")
	b := e.Verified("Penerima")
	oid := e.buyPaid(a, slug, tier, 1, "gopay")
	tid := ticketIDs(t, a)[0]
	var oldQR qr
	a.GET(api + "/tickets/" + tid + "/qr").Expect(200).Data(&oldQR)

	var link struct {
		Link string `json:"link"`
	}
	a.POST(api+"/tickets/"+tid+"/transfer", M{"email": b.Email}).Expect(201).Data(&link)
	tok := claimToken(link.Link)
	if m, ok := e.Mail.LastMailTo(b.Email); !ok || !strings.Contains(m.Text, tok) {
		t.Fatal("email klaim harus terkirim berisi tautan")
	}
	// pengirim sendiri tak bisa klaim; email lain ditolak
	a.POST(api+"/claims/"+tok+"/accept", nil).ExpectErr(409, "CONFLICT")
	other := e.Verified("Lain")
	other.POST(api+"/claims/"+tok+"/accept", nil).ExpectErr(403, "FORBIDDEN")
	// penerima klaim
	b.POST(api+"/claims/"+tok+"/accept", nil).Expect(200)
	// sekali pakai
	b.POST(api+"/claims/"+tok+"/accept", nil).ExpectErr(409, "INVALID_STATE")
	if len(ticketIDs(t, a)) != 0 || len(ticketIDs(t, b)) != 1 {
		t.Fatal("kepemilikan harus pindah")
	}
	// QR lama tidak valid; QR baru valid
	var r struct{ Result string }
	o.C.POST(api+"/gate/scan", M{"payload": oldQR.Payload, "event_id": evID}).Expect(200).Data(&r)
	if r.Result != "EXPIRED_CODE" {
		t.Fatalf("QR lama harus gagal: %s", r.Result)
	}
	var nq qr
	b.GET(api + "/tickets/" + tid + "/qr").Expect(200).Data(&nq)
	o.C.POST(api+"/gate/scan", M{"payload": nq.Payload, "event_id": evID}).Expect(200).Data(&r)
	if r.Result != "OK" {
		t.Fatalf("QR baru: %s", r.Result)
	}
	// pengirim awal tak bisa lagi refund order (tiket sudah berpindah)
	a.GET(api + "/orders/" + oid + "/refund-eligibility").Expect(200)
	if e.QueryInt("SELECT count(*) FROM ticket_transfers WHERE ticket_id=$1", tid) != 1 {
		t.Fatal("rantai kepemilikan harus tercatat")
	}
}

func TestTransferRulesPolicyRevokeExpiry(t *testing.T) {
	e := NewEnv(t)
	o, evID, slug, tier := e.publishedGA(100000, 10)
	a := e.Verified("Aldo")
	b := e.Verified("Bimo")
	e.buyPaid(a, slug, tier, 2, "gopay")
	ids := ticketIDs(t, a)
	// bukan pemilik → 404
	b.POST(api+"/tickets/"+ids[0]+"/transfer", M{}).ExpectErr(404, "NOT_FOUND")
	// organizer mematikan transfer
	o.C.PUT(api+"/organizer/events/"+evID+"/ticket-policy", M{"transfer_enabled": false, "resale_cap_pct": 0}).Expect(200)
	a.POST(api+"/tickets/"+ids[1]+"/transfer", M{}).ExpectErr(422, "NOT_ALLOWED")
	o.C.PUT(api+"/organizer/events/"+evID+"/ticket-policy", M{"transfer_enabled": true, "resale_cap_pct": 0}).Expect(200)
	// tiket terpakai tidak bisa dikirim
	e.Exec("UPDATE tickets SET status='used' WHERE id=$1", ids[1])
	a.POST(api+"/tickets/"+ids[1]+"/transfer", M{}).ExpectErr(409, "TICKET_INVALID")
	// dicabut
	var l1 struct{ Link string }
	a.POST(api+"/tickets/"+ids[0]+"/transfer", M{}).Expect(201).Data(&l1)
	a.DELETE(api + "/tickets/" + ids[0] + "/transfer").Expect(204)
	b.POST(api+"/claims/"+claimToken(l1.Link)+"/accept", nil).ExpectErr(409, "INVALID_STATE")
	// kedaluwarsa
	var l2 struct{ Link string }
	a.POST(api+"/tickets/"+ids[0]+"/transfer", M{}).Expect(201).Data(&l2)
	e.Clock.Advance(73 * time.Hour)
	b = e.Login(b.Email)
	b.POST(api+"/claims/"+claimToken(l2.Link)+"/accept", nil).ExpectErr(409, "INVALID_STATE")
}
