package apitest

import "testing"

func TestPassportPointsBadgesRedeemAndDelete(t *testing.T) {
	e := NewEnv(t)
	o, evID, slug, tier := e.publishedGA(100000, 10)
	e.Exec("UPDATE events SET genre='Rock' WHERE id=$1", evID)
	u := e.Verified("Penonton Setia")
	e.buyPaid(u, slug, tier, 1, "gopay")
	var pp struct {
		Points int
		Badges []string
		Stamps []map[string]any
	}
	u.GET(api + "/passport").Expect(200).Data(&pp)
	if pp.Points != 0 || len(pp.Stamps) != 0 {
		t.Fatalf("belum hadir: %+v", pp)
	}
	tid := ticketIDs(t, u)[0]
	var q qr
	u.GET(api + "/tickets/" + tid + "/qr").Expect(200).Data(&q)
	o.C.POST(api+"/gate/scan", M{"payload": q.Payload, "event_id": evID}).Expect(200)
	u.GET(api + "/passport").Expect(200).Data(&pp)
	if pp.Points != 200 { // 100 hadir + 50 kota baru + 50 genre baru
		t.Fatalf("poin pertama: %d", pp.Points)
	}
	if len(pp.Stamps) != 1 || len(pp.Badges) == 0 || pp.Badges[0] != "first_show" {
		t.Fatalf("stempel & badge: %+v", pp)
	}
	// scan ulang (duplikat) tidak menambah poin
	o.C.POST(api+"/gate/scan", M{"payload": q.Payload, "event_id": evID}).Expect(200)
	u.GET(api + "/passport").Expect(200).Data(&pp)
	if pp.Points != 200 {
		t.Fatalf("idempoten: %d", pp.Points)
	}
	// tukar: butuh 500, event tanpa fase fan → ditolak
	_, ev2, slug2, _ := e.publishedGA(100000, 10)
	u.POST(api+"/passport/redeem", M{"event": slug2}).ExpectErr(422, "VALIDATION_FAILED")
	e.Exec("INSERT INTO sale_phases(id,event_id,name,audience,starts_at,ends_at) VALUES ($1,$2,'Fan','fan',now()+interval '1 day',now()+interval '2 days')", newID(), ev2)
	u.POST(api+"/passport/redeem", M{"event": slug2}).ExpectErr(422, "VALIDATION_FAILED") // poin kurang
	e.Exec("INSERT INTO passport_points(user_id, points, reason) VALUES ($1, 400, 'bonus-tes')", u.UserID)
	u.POST(api+"/passport/redeem", M{"event": slug2}).Expect(204)
	if e.QueryInt("SELECT count(*) FROM phase_access WHERE user_id=$1 AND via='passport'", u.UserID) != 1 {
		t.Fatal("akses fase fan diberikan")
	}
	u.POST(api+"/passport/redeem", M{"event": slug2}).ExpectErr(409, "CONFLICT")
	u.GET(api + "/passport").Expect(200).Data(&pp)
	if pp.Points != 100 {
		t.Fatalf("poin setelah tukar: %d", pp.Points)
	}
	// hapus data: bersih + opt-out (scan berikutnya tidak memberi poin)
	u.DELETE(api + "/passport").Expect(204)
	if e.QueryInt("SELECT count(*) FROM passport_points WHERE user_id=$1", u.UserID) != 0 {
		t.Fatal("poin terhapus")
	}
	u.GET(api + "/passport").Expect(200).Data(&pp)
	if len(pp.Stamps) != 0 || pp.Points != 0 {
		t.Fatalf("profil kosong setelah hapus: %+v", pp)
	}
	// profil tidak publik
	e.Anon().GET(api + "/passport").Expect(401)
}
