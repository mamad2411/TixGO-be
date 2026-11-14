package apitest

import "testing"

func attend(t *testing.T, e *Env, o *Org, evID string, u *Client) string {
	t.Helper()
	tid := ticketIDs(t, u)[0]
	var q qr
	u.GET(api + "/tickets/" + tid + "/qr").Expect(200).Data(&q)
	o.C.POST(api+"/gate/scan", M{"payload": q.Payload, "event_id": evID}).Expect(200)
	return tid
}

func TestReviewsOnlyAfterAttendanceModerationAggregateAndReply(t *testing.T) {
	e := NewEnv(t)
	o, evID, slug, tier := e.publishedGA(100000, 30)
	var users []*Client
	var tickets []string
	for i := 0; i < 6; i++ {
		u := e.Verified("Penonton")
		e.buyPaid(u, slug, tier, 1, "gopay")
		users = append(users, u)
	}
	// belum hadir → tidak boleh mengulas
	tid0 := ticketIDs(t, users[0])[0]
	users[0].POST(api+"/tickets/"+tid0+"/review", M{"rating": 5, "body": "Seru banget"}).ExpectErr(422, "VALIDATION_FAILED")
	for _, u := range users {
		tickets = append(tickets, attend(t, e, o, evID, u))
	}
	users[0].POST(api+"/tickets/"+tickets[0]+"/review", M{"rating": 6}).ExpectErr(400, "VALIDATION_FAILED")
	var rv struct{ ID, Status string }
	users[0].POST(api+"/tickets/"+tickets[0]+"/review", M{"rating": 5, "body": "Sound system mantap"}).Expect(201).Data(&rv)
	if rv.Status != "published" {
		t.Fatal("lolos filter → terbit")
	}
	users[0].POST(api+"/tickets/"+tickets[0]+"/review", M{"rating": 4}).ExpectErr(409, "CONFLICT")
	// ulasan berisi kata terlarang/URL → antrean moderasi, tidak tampil
	var bad struct{ ID, Status string }
	users[1].POST(api+"/tickets/"+tickets[1]+"/review", M{"rating": 1, "body": "Penyelenggara tolol, kunjungi http://spam.example"}).Expect(201).Data(&bad)
	if bad.Status != "pending" {
		t.Fatalf("harus pending: %s", bad.Status)
	}
	var pub struct {
		Count   int
		Average *float64
		Reviews []map[string]any
	}
	e.Anon().GET(api + "/events/" + slug + "/reviews").Expect(200).Data(&pub)
	if pub.Count != 1 || pub.Average != nil || len(pub.Reviews) != 1 {
		t.Fatalf("agregat disembunyikan di bawah 5 ulasan: %+v", pub)
	}
	if pub.Reviews[0]["author"] != "Penonton" {
		t.Fatal("hanya nama depan")
	}
	for i := 2; i < 6; i++ {
		users[i].POST(api+"/tickets/"+tickets[i]+"/review", M{"rating": 4, "body": "Oke"}).Expect(201)
	}
	e.Anon().GET(api + "/events/" + slug + "/reviews").Expect(200).Data(&pub)
	if pub.Count != 5 || pub.Average == nil || *pub.Average < 4.1 || *pub.Average > 4.3 {
		t.Fatalf("5 ulasan: rata-rata 4.2 tampil: %+v", pub)
	}
	// moderasi manual
	admin := e.UserWithRole("admin", "Admin Mod")
	var q []map[string]any
	admin.GET(api + "/admin/moderation/reviews").Expect(200).Data(&q)
	if len(q) != 1 {
		t.Fatalf("antrean: %d", len(q))
	}
	admin.POST(api+"/admin/moderation/reviews/"+bad.ID+"/reject", nil).Expect(204)
	admin.POST(api+"/admin/moderation/reviews/"+bad.ID+"/bogus", nil).ExpectErr(400, "VALIDATION_FAILED")
	// laporan: 3 pelapor menyembunyikan ulasan
	users[0].POST(api+"/reviews/"+rv.ID+"/report", M{"reason": "tes"}).ExpectErr(400, "VALIDATION_FAILED")
	users[0].POST(api+"/reviews/"+rv.ID+"/report", M{"reason": "mengandung klaim palsu"}).ExpectErr(409, "CONFLICT")
	for i := 1; i <= 3; i++ {
		users[i+1].POST(api+"/reviews/"+rv.ID+"/report", M{"reason": "ulasan menyesatkan"}).Expect(204)
		if i == 1 {
			users[i+1].POST(api+"/reviews/"+rv.ID+"/report", M{"reason": "ulasan menyesatkan"}).Expect(204) // idempoten per pelapor
		}
	}
	if e.QueryStr("SELECT status FROM reviews WHERE id=$1", rv.ID) != "pending" {
		t.Fatal("≥3 laporan → disembunyikan")
	}
	admin.POST(api+"/admin/moderation/reviews/"+rv.ID+"/approve", nil).Expect(204)
	// balasan organizer
	o.C.POST(api+"/organizer/events/"+evID+"/reviews/"+rv.ID+"/reply", M{"reply": "Terima kasih sudah hadir!"}).Expect(204)
	e.Anon().GET(api + "/events/" + slug + "/reviews").Expect(200).Data(&pub)
	found := false
	for _, r := range pub.Reviews {
		if r["organizer_reply"] == "Terima kasih sudah hadir!" {
			found = true
		}
	}
	if !found {
		t.Fatal("balasan tampil")
	}
	o2 := e.NewOrganizer(true)
	o2.C.POST(api+"/organizer/events/"+evID+"/reviews/"+rv.ID+"/reply", M{"reply": "bukan event saya"}).ExpectErr(404, "NOT_FOUND")
}

func TestPhotoGalleryRightsAndModeration(t *testing.T) {
	e := NewEnv(t)
	o, evID, slug, tier := e.publishedGA(100000, 30)
	fan := e.Verified("Penonton Foto")
	outsider := e.Verified("Orang Luar")
	e.buyPaid(fan, slug, tier, 1, "gopay")
	attend(t, e, o, evID, fan)
	var up struct{ ID string }
	fan.Upload(api+"/media", map[string]string{"kind": "photo"}, "file", "f.png", pngBytes(500, 500)).Expect(201).Data(&up)
	body := M{"asset_id": up.ID, "rights_confirmed": false}
	fan.POST(api+"/events/"+slug+"/photos", body).ExpectErr(400, "VALIDATION_FAILED")
	var out struct{ ID string }
	outsider.POST(api+"/events/"+slug+"/photos", M{"asset_id": up.ID, "rights_confirmed": true}).ExpectErr(403, "FORBIDDEN")
	fan.POST(api+"/events/"+slug+"/photos", M{"asset_id": up.ID, "rights_confirmed": true}).Expect(201).Data(&out)
	// belum dimoderasi → tidak tampil & berkas tidak tersaji
	var gal []map[string]any
	e.Anon().GET(api + "/events/" + slug + "/photos").Expect(200).Data(&gal)
	if len(gal) != 0 {
		t.Fatal("pending tidak tampil")
	}
	e.Anon().GET(api + "/media/" + up.ID + "/400.jpg").Expect(404)
	admin := e.UserWithRole("support", "Agen Mod")
	admin.POST(api+"/admin/moderation/photos/"+out.ID+"/approve", nil).Expect(204)
	e.Anon().GET(api + "/events/" + slug + "/photos").Expect(200).Data(&gal)
	if len(gal) != 1 {
		t.Fatal("setelah disetujui tampil")
	}
	e.Anon().GET(api + "/media/" + up.ID + "/400.jpg").Expect(200)
}
