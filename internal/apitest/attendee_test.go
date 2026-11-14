package apitest

import (
	"strings"
	"testing"
)

func TestCompTicketsExistingUserAndNewcomerClaim(t *testing.T) {
	e := NewEnv(t)
	o, evID, _, tier := e.publishedGA(100000, 10)
	e.Exec("UPDATE tiers SET comp_quota=2 WHERE id=$1", tier)
	friend := e.Verified("Teman Lama")
	var c1 struct {
		TicketID string `json:"ticket_id"`
		Claim    string `json:"claim_link"`
	}
	o.C.POST(api+"/organizer/events/"+evID+"/comps", M{"tier_id": tier, "name": "Teman Lama", "email": friend.Email, "reason": "tamu VIP"}).Expect(201).Data(&c1)
	if c1.Claim != "" || len(ticketIDs(t, friend)) != 1 {
		t.Fatal("penerima beraccount langsung menerima tiket")
	}
	var c2 struct {
		TicketID string `json:"ticket_id"`
		Claim    string `json:"claim_link"`
	}
	o.C.POST(api+"/organizer/events/"+evID+"/comps", M{"tier_id": tier, "name": "Orang Baru", "email": "baru@example.com", "reason": "sponsor"}).Expect(201).Data(&c2)
	if c2.Claim == "" {
		t.Fatal("penerima baru mendapat tautan klaim")
	}
	// kuota undangan habis; kuota penjualan tidak berkurang
	o.C.POST(api+"/organizer/events/"+evID+"/comps", M{"tier_id": tier, "name": "Lagi", "email": "x@example.com", "reason": "tambah"}).ExpectErr(409, "LIMIT_EXCEEDED")
	if e.QueryInt("SELECT sold FROM tiers WHERE id=$1", tier) != 0 {
		t.Fatal("comp tidak boleh mengurangi kuota jual")
	}
	// penerima baru mendaftar lalu klaim, walau transfer dimatikan
	e.Exec("UPDATE events SET transfer_enabled=false WHERE id=$1", evID)
	nb := e.Register("baru@example.com", "Orang Baru")
	e.Exec("UPDATE users SET email_verified_at=now() WHERE id=$1", nb.UserID)
	nb = e.Login("baru@example.com")
	nb.POST(api+"/claims/"+claimToken(c2.Claim)+"/accept", nil).Expect(200)
	if len(ticketIDs(t, nb)) != 1 {
		t.Fatal("tiket comp berpindah ke penerima")
	}
	if e.QueryStr("SELECT origin FROM tickets WHERE id=$1", c2.TicketID) != "comp" {
		t.Fatal("asal tiket tetap comp")
	}
	// bukan organizer lain
	o2 := e.NewOrganizer(true)
	o2.C.POST(api+"/organizer/events/"+evID+"/comps", M{"tier_id": tier, "name": "Curang", "email": "c@example.com", "reason": "x y z"}).ExpectErr(404, "NOT_FOUND")
}

func TestAttendeeListSearchExportAndImport(t *testing.T) {
	e := NewEnv(t)
	o, evID, slug, tier := e.publishedGA(100000, 30)
	u := e.Verified("Budi Santoso")
	e.buyPaid(u, slug, tier, 2, "gopay")
	e.Exec("UPDATE tickets SET attendee_name='=HYPERLINK(\"http://evil\")' WHERE id=(SELECT id FROM tickets LIMIT 1)")
	var l []map[string]any
	o.C.GET(api + "/organizer/events/" + evID + "/attendees").Expect(200).Data(&l)
	if len(l) != 2 {
		t.Fatalf("2 peserta: %d", len(l))
	}
	var one []map[string]any
	o.C.GET(api + "/organizer/events/" + evID + "/attendees?q=" + strings.ToLower(u.Email[:6])).Expect(200).Data(&one)
	if len(one) != 2 {
		t.Fatalf("cari email: %d", len(one))
	}
	o.C.GET(api + "/organizer/events/" + evID + "/attendees?status=used").Expect(200).Data(&one)
	if len(one) != 0 {
		t.Fatal("filter status")
	}
	r := o.C.GET(api + "/organizer/events/" + evID + "/attendees.csv").Expect(200)
	if !strings.Contains(string(r.Body), "'=HYPERLINK") || strings.Contains(string(r.Body), ",=HYPERLINK") {
		t.Fatalf("formula injection harus dinetralkan: %s", r.Body)
	}
	if e.QueryInt("SELECT count(*) FROM audit_logs WHERE action='attendees.export'") != 1 {
		t.Fatal("ekspor harus teraudit")
	}
	// impor CSV dengan baris salah
	e.Exec("UPDATE tiers SET comp_quota=5 WHERE id=$1", tier)
	csvData := "name,email\nAyu Lestari,ayu@example.com\nX,bukan-email\nDewi Sartika,dewi@example.com\n"
	var res struct {
		Issued int
		Errors []struct {
			Row   int
			Error string
		}
	}
	o.C.Upload(api+"/organizer/events/"+evID+"/comps/import", map[string]string{"tier_id": tier, "reason": "undangan sponsor"}, "file", "g.csv", []byte(csvData)).Expect(200).Data(&res)
	if res.Issued != 2 || len(res.Errors) != 1 || res.Errors[0].Row != 3 {
		t.Fatalf("impor: %+v", res)
	}
	o.C.Upload(api+"/organizer/events/"+evID+"/comps/import", map[string]string{"tier_id": tier, "reason": "x y z"}, "file", "g.csv", []byte("a,b\n1,2\n")).ExpectErr(400, "VALIDATION_FAILED")
}

func TestReissueRenameVoidRotateSecret(t *testing.T) {
	e := NewEnv(t)
	o, evID, slug, tier := e.publishedGA(100000, 10)
	u := e.Verified("Pemilik")
	e.buyPaid(u, slug, tier, 2, "gopay")
	ids := ticketIDs(t, u)
	var q1 qr
	u.GET(api + "/tickets/" + ids[0] + "/qr").Expect(200).Data(&q1)
	base := api + "/organizer/events/" + evID + "/tickets/"
	o.C.POST(base+ids[0]+"/reissue", nil).Expect(204)
	var res struct{ Result string }
	o.C.POST(api+"/gate/scan", M{"payload": q1.Payload, "event_id": evID}).Expect(200).Data(&res)
	if res.Result != "EXPIRED_CODE" {
		t.Fatalf("QR lama mati setelah reissue: %s", res.Result)
	}
	o.C.POST(base+ids[0]+"/rename", M{"name": "A"}).ExpectErr(400, "VALIDATION_FAILED")
	o.C.POST(base+ids[0]+"/rename", M{"name": "Nama Baru"}).Expect(204)
	if e.QueryStr("SELECT attendee_name FROM tickets WHERE id=$1", ids[0]) != "Nama Baru" {
		t.Fatal("nama berubah")
	}
	o.C.POST(base+ids[1]+"/void", nil).Expect(204)
	o.C.POST(base+ids[1]+"/void", nil).ExpectErr(409, "TICKET_INVALID")
	if e.QueryInt("SELECT count(*) FROM audit_logs WHERE action IN ('ticket.reissue','ticket.rename','ticket.void')") != 3 {
		t.Fatal("semua aksi teraudit")
	}
	// event mulai → rename ditolak
	e.Exec("UPDATE events SET starts_at=now()-interval '1 hour' WHERE id=$1", evID)
	o.C.POST(base+ids[0]+"/rename", M{"name": "Terlambat"}).ExpectErr(409, "INVALID_STATE")
}

func TestBroadcastToAttendeesWithDailyLimit(t *testing.T) {
	e := NewEnv(t)
	o, evID, slug, tier := e.publishedGA(100000, 10)
	a := e.Verified("Satu")
	b := e.Verified("Dua")
	e.buyPaid(a, slug, tier, 1, "gopay")
	e.buyPaid(b, slug, tier, 1, "gopay")
	base := api + "/organizer/events/" + evID + "/broadcasts"
	o.C.POST(base, M{"subject": "x", "body": "pendek"}).ExpectErr(400, "VALIDATION_FAILED")
	var bc struct {
		Recipients int
		Status     string
	}
	o.C.POST(base, M{"subject": "Info pintu masuk", "body": "Pintu A dibuka pukul 17.00, bawa e-tiket."}).Expect(201).Data(&bc)
	if bc.Recipients != 2 {
		t.Fatalf("penerima: %d", bc.Recipients)
	}
	for _, u := range []*Client{a, b} {
		found := false
		for _, m := range e.Mail.MailsTo(u.Email) {
			if strings.Contains(m.Subject, "Info pintu masuk") {
				found = true
			}
		}
		if !found {
			t.Fatalf("siaran belum sampai ke %s", u.Email)
		}
	}
	if e.QueryStr("SELECT status FROM broadcasts") != "sent" {
		t.Fatal("status sent")
	}
	for i := 0; i < 2; i++ {
		o.C.POST(base, M{"subject": "Info lagi", "body": "Pengumuman tambahan untuk semua."}).Expect(201)
	}
	o.C.POST(base, M{"subject": "Terlalu sering", "body": "Ini ke-4 dalam sehari."}).ExpectErr(429, "RATE_LIMITED")
}
