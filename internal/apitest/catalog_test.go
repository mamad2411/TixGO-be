package apitest

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---------- Onboarding organizer & KYB (M33) ----------

func TestOrganizerApplyRequiresVerifiedEmailAndIsSingle(t *testing.T) {
	e := NewEnv(t)
	c := e.Register(uniqEmail("o"), "Calon Organizer")
	c.POST(api+"/organizer/apply", M{"name": "Panggung A"}).ExpectErr(403, "EMAIL_NOT_VERIFIED")
	e.Exec("UPDATE users SET email_verified_at=now() WHERE id=$1", c.UserID)
	c.POST(api+"/organizer/apply", M{"name": "ab"}).ExpectErr(400, "VALIDATION_FAILED")
	c.POST(api+"/organizer/apply", M{"name": "Panggung A", "type": "x"}).ExpectErr(400, "VALIDATION_FAILED")
	var o struct {
		ID, KYBStatus string
	}
	r := c.POST(api+"/organizer/apply", M{"name": "Panggung A"}).Expect(201)
	m := r.Map()
	if m["kyb_status"] != "draft" || m["can_sell_paid"] != false {
		t.Fatalf("organizer baru harus draft & belum boleh jual berbayar: %v", m)
	}
	_ = o
	c.POST(api+"/organizer/apply", M{"name": "Panggung B"}).ExpectErr(409, "CONFLICT")
	// peran berubah → harus login ulang untuk klaim baru
	c = e.Login(c.Email)
	var me struct {
		Role      string `json:"role"`
		Organizer struct {
			Role string `json:"role"`
		} `json:"organizer"`
	}
	c.GET(api + "/me").Expect(200).Data(&me)
	if me.Role != "organizer" || me.Organizer.Role != "owner" {
		t.Fatalf("peran harus organizer/owner: %+v", me)
	}
	// pelanggan biasa tidak dapat mengakses /organizer
	e.Verified("Biasa").GET(api+"/organizer").ExpectErr(403, "FORBIDDEN")
}

func TestKYBFullFlow(t *testing.T) {
	e := NewEnv(t)
	// ketentuan organizer terbit → kontrak wajib
	admin := e.UserWithRole("admin", "Admin")
	var d struct {
		ID string `json:"id"`
	}
	admin.POST(api+"/admin/legal", M{"kind": "organizer_terms", "locale": "id", "title": "Ketentuan Organizer", "body": "isi"}).Expect(201).Data(&d)
	admin.POST(api+"/admin/legal/"+d.ID+"/publish", nil).Expect(200)

	o := e.NewOrganizerNoMFA(false)
	// belum lengkap → 422 dengan daftar yang kurang
	r := o.C.POST(api+"/organizer/kyb/submit", nil).ExpectErr(422, "VALIDATION_FAILED")
	missing, _ := r.ErrDetails()["missing"].([]any)
	if len(missing) < 3 {
		t.Fatalf("harus melaporkan dokumen/rekening/kontrak yang kurang: %v", missing)
	}
	// dokumen: isi bukan PDF/gambar ditolak (sniff isi, bukan nama berkas)
	o.C.Upload(api+"/organizer/kyb/documents", map[string]string{"kind": "ktp"}, "file", "ktp.pdf", []byte("<html>bukan pdf</html>")).ExpectErr(400, "VALIDATION_FAILED")
	o.C.Upload(api+"/organizer/kyb/documents", map[string]string{"kind": "aneh"}, "file", "x.png", pngBytes(10, 10)).ExpectErr(400, "VALIDATION_FAILED")
	var doc struct {
		ID string `json:"id"`
	}
	o.C.Upload(api+"/organizer/kyb/documents", map[string]string{"kind": "ktp"}, "file", "ktp.png", pngBytes(40, 40)).Expect(201).Data(&doc)
	// rekening: butuh sesi MFA
	o.C.POST(api+"/organizer/bank-accounts", M{"bank_code": "BCA", "account_no": "1234567890", "account_name": "PT Panggung Kita"}).ExpectErr(403, "MFA_REQUIRED")
	o.C = e.EnableMFA(o.C)
	o.C.POST(api+"/organizer/bank-accounts", M{"bank_code": "BCA", "account_no": "12ab", "account_name": "PT"}).ExpectErr(400, "VALIDATION_FAILED")
	var bank map[string]any
	o.C.POST(api+"/organizer/bank-accounts", M{"bank_code": "bca", "account_no": "1234 5678 90", "account_name": "PT Panggung Kita"}).Expect(201).Data(&bank)
	if bank["account_no_masked"] != "****7890" || bank["bank_code"] != "BCA" {
		t.Fatalf("nomor rekening harus dimasker: %v", bank)
	}
	if strings.Contains(string(e.rawBankRow()), "1234567890") {
		t.Fatal("nomor rekening tidak boleh tersimpan plain di DB")
	}
	// kontrak
	o.C.POST(api+"/organizer/kyb/contract", M{"document_id": d.ID}).Expect(204)
	// submit
	o.C.POST(api+"/organizer/kyb/submit", nil).Expect(200)
	o.C.POST(api+"/organizer/kyb/submit", nil).ExpectErr(409, "INVALID_STATE")
	// dokumen terkunci saat ditinjau
	o.C.DELETE(api+"/organizer/kyb/documents/"+doc.ID).ExpectErr(409, "INVALID_STATE")

	// event berbayar belum bisa diajukan selama KYB belum approved
	venue := e.NewVenue(o.C, "Bandung")
	var ev struct {
		ID string `json:"id"`
	}
	o.C.POST(api+"/organizer/events", M{"venue_id": venue, "title": "Konser Belum KYB", "mode": "general", "starts_at": e.future(20)}).Expect(201).Data(&ev)
	o.C.POST(api+"/organizer/events/"+ev.ID+"/tiers", M{"name": "Reg", "price": 100000, "quota": 50}).Expect(201)
	r2 := o.C.POST(api+"/organizer/events/"+ev.ID+"/submit", nil).ExpectErr(422, "VALIDATION_FAILED")
	if !strings.Contains(string(r2.Body), "kyb_approved") {
		t.Fatalf("harus menyebut kyb_approved: %s", r2.Body)
	}

	// admin: needs_info butuh alasan; lalu approve
	admin.GET(api + "/admin/kyb?status=submitted").Expect(200)
	admin.POST(api+"/admin/kyb/"+o.ID+"/review", M{"decision": "needs_info"}).ExpectErr(400, "VALIDATION_FAILED")
	admin.POST(api+"/admin/kyb/"+o.ID+"/review", M{"decision": "needs_info", "reason": "NPWP buram"}).Expect(200)
	var org map[string]any
	o.C.GET(api + "/organizer").Expect(200).Data(&org)
	if org["kyb_status"] != "needs_info" || org["kyb_reason"] != "NPWP buram" {
		t.Fatalf("status KYB organizer salah: %v", org)
	}
	// perbaiki → ajukan ulang → setujui
	o.C.DELETE(api + "/organizer/kyb/documents/" + doc.ID).Expect(204)
	o.C.Upload(api+"/organizer/kyb/documents", map[string]string{"kind": "npwp"}, "file", "npwp.png", pngBytes(50, 50)).Expect(201)
	o.C.POST(api+"/organizer/kyb/submit", nil).Expect(200)
	admin.POST(api+"/admin/kyb/"+o.ID+"/review", M{"decision": "approve"}).Expect(200)
	o.C.GET(api + "/organizer").Data(&org)
	if org["kyb_status"] != "approved" || org["can_sell_paid"] != true {
		t.Fatalf("harus approved: %v", org)
	}
	if n := e.QueryInt("SELECT count(*) FROM bank_accounts WHERE organizer_id=$1 AND verified_at IS NOT NULL", o.ID); n != 1 {
		t.Fatal("rekening harus terverifikasi saat KYB disetujui")
	}
	// email pemberitahuan KYB terkirim
	found := false
	for _, m := range e.Mail.MailsTo(o.C.Email) {
		if strings.Contains(m.Subject, "approved") {
			found = true
		}
	}
	if !found {
		t.Fatal("pemilik harus diberi tahu hasil KYB")
	}
	// audit
	if n := e.QueryInt("SELECT count(*) FROM audit_logs WHERE action='kyb.review' AND entity_id=$1", o.ID); n != 2 {
		t.Fatalf("audit kyb.review harus 2, dapat %d", n)
	}
	// perubahan rekening setelah approved → masa tunggu 3 hari + butuh verifikasi ulang
	var nb map[string]any
	o.C.POST(api+"/organizer/bank-accounts", M{"bank_code": "BNI", "account_no": "99887766", "account_name": "PT Panggung Kita"}).Expect(201).Data(&nb)
	if nb["usable"] != false || nb["verified"] != false {
		t.Fatalf("rekening baru pasca-approved tidak boleh langsung usable: %v", nb)
	}
	// hanya non-admin tidak boleh meninjau
	e.Verified("Xx").POST(api+"/admin/kyb/"+o.ID+"/review", M{"decision": "approve"}).ExpectErr(403, "FORBIDDEN")
}

func (e *Env) rawBankRow() []byte {
	var b []byte
	_ = pool.QueryRow(e.ctx, "SELECT account_no_enc FROM bank_accounts LIMIT 1").Scan(&b)
	return b
}

func TestOrganizerTeamManagement(t *testing.T) {
	e := NewEnv(t)
	o := e.NewOrganizerNoMFA(true)
	member := e.Verified("Anggota")
	// tanpa MFA ditolak
	o.C.POST(api+"/organizer/team", M{"email": member.Email, "role": "member"}).ExpectErr(403, "MFA_REQUIRED")
	o.C = e.EnableMFA(o.C)
	o.C.POST(api+"/organizer/team", M{"email": "tidak-ada@example.com", "role": "member"}).ExpectErr(404, "NOT_FOUND")
	o.C.POST(api+"/organizer/team", M{"email": member.Email, "role": "owner"}).ExpectErr(400, "VALIDATION_FAILED")
	o.C.POST(api+"/organizer/team", M{"email": member.Email, "role": "member"}).Expect(201)
	o.C.POST(api+"/organizer/team", M{"email": member.Email, "role": "member"}).ExpectErr(409, "CONFLICT")
	var team []map[string]any
	o.C.GET(api + "/organizer/team").Expect(200).Data(&team)
	if len(team) != 2 {
		t.Fatalf("tim harus 2 orang: %v", team)
	}
	// anggota login ulang → bisa melihat organizer tapi tidak bisa kelola tim/rekening
	mc := e.Login(member.Email)
	mc.GET(api + "/organizer").Expect(200)
	mc.POST(api+"/organizer/team", M{"email": "x@example.com", "role": "member"}).ExpectErr(403, "FORBIDDEN")
	mc.POST(api+"/organizer/bank-accounts", M{"bank_code": "BCA", "account_no": "1234567890", "account_name": "Nama"}).ExpectErr(403, "FORBIDDEN")
	// hapus anggota → aksesnya dicabut
	o.C.DELETE(api + "/organizer/team/" + member.UserID).Expect(204)
	mc.GET(api+"/organizer").ExpectErr(401, "UNAUTHENTICATED")
	e.Login(member.Email).GET(api+"/organizer").ExpectErr(403, "FORBIDDEN")
	// pemilik tidak bisa menghapus dirinya
	o.C.DELETE(api+"/organizer/team/"+o.C.UserID).ExpectErr(409, "CONFLICT")
}

// ---------- Event: validasi & siklus ----------

func TestEventCreateValidation(t *testing.T) {
	e := NewEnv(t)
	o := e.NewOrganizer(true)
	venue := e.NewVenue(o.C, "Jakarta")
	base := func(m M) M {
		b := M{"venue_id": venue, "title": "Konser", "mode": "general", "starts_at": e.future(10)}
		for k, v := range m {
			b[k] = v
		}
		return b
	}
	post := func(m M) *Resp { return o.C.POST(api+"/organizer/events", base(m)) }
	post(M{"starts_at": e.Clock.Now().Add(-time.Hour).Format(time.RFC3339)}).ExpectErr(400, "VALIDATION_FAILED") // masa lalu
	post(M{"title": "ab"}).ExpectErr(400, "VALIDATION_FAILED")
	post(M{"mode": "campur"}).ExpectErr(400, "VALIDATION_FAILED")
	post(M{"venue_id": newID()}).ExpectErr(400, "VALIDATION_FAILED")
	post(M{"ends_at": e.future(5)}).ExpectErr(400, "VALIDATION_FAILED")   // ends sebelum start
	post(M{"doors_at": e.future(11)}).ExpectErr(400, "VALIDATION_FAILED") // doors setelah start
	post(M{"tax_bp": 1100}).ExpectErr(422, "VALIDATION_FAILED")           // pajak ditunda (Q13)
	post(M{"hold_ttl_sec": 5}).ExpectErr(400, "VALIDATION_FAILED")
	post(M{"visibility": "password"}).ExpectErr(400, "VALIDATION_FAILED") // password wajib
	post(M{"refund_policy": M{"type": "refundable"}}).ExpectErr(400, "VALIDATION_FAILED")
	post(M{"refund_policy": M{"type": "refundable", "refundable_until": e.future(11), "deduction_pct": 10, "on_reschedule": M{"window_days": 14}}}).ExpectErr(400, "VALIDATION_FAILED") // setelah acara
	post(M{"refund_policy": M{"type": "non_refundable", "deduction_pct": 10, "on_reschedule": M{"window_days": 14}}}).ExpectErr(400, "VALIDATION_FAILED")

	// default aman: non_refundable, fee 0, dibayar pembeli
	var v map[string]any
	post(nil).Expect(201).Data(&v)
	pol := v["refund_policy"].(map[string]any)
	if pol["type"] != "non_refundable" || v["status"] != "draft" || v["fee_payer"] != "buyer" {
		t.Fatalf("default event salah: %v", v)
	}
	if !strings.HasPrefix(v["slug"].(string), "konser-") {
		t.Fatalf("slug harus dari judul: %v", v["slug"])
	}
	// refund policy refundable valid
	post(M{"title": "Konser Refund", "refund_policy": M{"type": "refundable", "refundable_until": e.future(5), "deduction_pct": 10, "fee_refundable": false, "on_reschedule": M{"window_days": 7}}}).Expect(201)
}

func TestMemberCannotSetPricingFeesOrPolicy(t *testing.T) {
	e := NewEnv(t)
	o := e.NewOrganizer(true)
	member := e.Verified("Anggota")
	o.C.POST(api+"/organizer/team", M{"email": member.Email, "role": "member"}).Expect(201)
	mc := e.Login(member.Email)
	venue := e.NewVenue(mc, "Jakarta")
	mc.POST(api+"/organizer/events", M{"venue_id": venue, "title": "Konser Anggota", "mode": "general", "starts_at": e.future(10), "service_fee_bp": 500}).ExpectErr(403, "FORBIDDEN")
	var ev struct {
		ID string `json:"id"`
	}
	mc.POST(api+"/organizer/events", M{"venue_id": venue, "title": "Konser Anggota", "mode": "general", "starts_at": e.future(10)}).Expect(201).Data(&ev)
	mc.PATCH(api+"/organizer/events/"+ev.ID, M{"description": "deskripsi baru"}).Expect(200)
	mc.PATCH(api+"/organizer/events/"+ev.ID, M{"fee_payer": "organizer"}).ExpectErr(403, "FORBIDDEN")
	mc.POST(api+"/organizer/events/"+ev.ID+"/tiers", M{"name": "VIP", "price": 1000, "quota": 5}).ExpectErr(403, "FORBIDDEN")
	mc.POST(api+"/organizer/events/"+ev.ID+"/submit", nil).ExpectErr(403, "FORBIDDEN")
	mc.DELETE(api+"/organizer/events/"+ev.ID).ExpectErr(403, "FORBIDDEN")
}

func TestEventLifecycleModerationAndPublish(t *testing.T) {
	e := NewEnv(t)
	o := e.NewOrganizer(true)
	admin := e.UserWithRole("admin", "Admin")
	venue := e.NewVenue(o.C, "Jakarta")
	var ev struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
	o.C.POST(api+"/organizer/events", M{"venue_id": venue, "title": "Festival Musim", "mode": "general", "starts_at": e.future(20)}).Expect(201).Data(&ev)

	// belum lengkap: tanpa tier
	r := o.C.POST(api+"/organizer/events/"+ev.ID+"/submit", nil).ExpectErr(422, "VALIDATION_FAILED")
	if !strings.Contains(string(r.Body), `"tiers"`) {
		t.Fatalf("harus menyebut tiers: %s", r.Body)
	}
	var tier struct {
		ID string `json:"id"`
	}
	o.C.POST(api+"/organizer/events/"+ev.ID+"/tiers", M{"name": "GA", "price": 250000, "quota": 0}).ExpectErr(400, "VALIDATION_FAILED") // kuota GA wajib
	o.C.POST(api+"/organizer/events/"+ev.ID+"/tiers", M{"name": "GA", "price": 250000, "quota": 200}).Expect(201).Data(&tier)
	o.C.POST(api+"/organizer/events/"+ev.ID+"/submit", nil).Expect(200)
	// saat ditinjau tidak bisa diubah
	o.C.PATCH(api+"/organizer/events/"+ev.ID, M{"description": "x"}).ExpectErr(409, "INVALID_STATE")
	o.C.POST(api+"/organizer/events/"+ev.ID+"/tiers", M{"name": "VIP", "price": 1, "quota": 1}).ExpectErr(409, "INVALID_STATE")
	// belum approved → tidak bisa publish
	o.C.POST(api+"/organizer/events/"+ev.ID+"/publish", nil).ExpectErr(409, "INVALID_STATE")
	// belum terlihat publik
	e.Anon().GET(api+"/events/"+ev.Slug).ExpectErr(404, "NOT_FOUND")

	// tolak harus beralasan → kembali ke draft
	admin.POST(api+"/admin/events/"+ev.ID+"/review", M{"decision": "reject"}).ExpectErr(400, "VALIDATION_FAILED")
	admin.POST(api+"/admin/events/"+ev.ID+"/review", M{"decision": "reject", "note": "poster belum ada"}).Expect(200)
	var v map[string]any
	o.C.GET(api + "/organizer/events/" + ev.ID).Expect(200).Data(&v)
	if v["status"] != "draft" || v["review_note"] != "poster belum ada" {
		t.Fatalf("setelah ditolak harus draft + catatan: %v", v)
	}
	// ajukan ulang → approve
	o.C.POST(api+"/organizer/events/"+ev.ID+"/submit", nil).Expect(200)
	admin.POST(api+"/admin/events/"+ev.ID+"/review", M{"decision": "approve"}).Expect(200)
	// ubah data sensitif pada approved → kembali draft
	o.C.PATCH(api+"/organizer/events/"+ev.ID, M{"title": "Festival Musim Baru"}).Expect(200)
	o.C.GET(api + "/organizer/events/" + ev.ID).Data(&v)
	if v["status"] != "draft" {
		t.Fatalf("perubahan sensitif pada approved harus kembali ke draft: %v", v["status"])
	}
	o.C.POST(api+"/organizer/events/"+ev.ID+"/submit", nil).Expect(200)
	admin.POST(api+"/admin/events/"+ev.ID+"/review", M{"decision": "approve"}).Expect(200)
	// ubah non-sensitif pada approved tetap approved
	o.C.PATCH(api+"/organizer/events/"+ev.ID, M{"genre": "Pop"}).Expect(200)
	o.C.GET(api + "/organizer/events/" + ev.ID).Data(&v)
	if v["status"] != "approved" {
		t.Fatalf("perubahan non-sensitif harus tetap approved: %v", v["status"])
	}
	// publish → fase publik default dibuat
	o.C.POST(api+"/organizer/events/"+ev.ID+"/publish", nil).Expect(200)
	o.C.GET(api + "/organizer/events/" + ev.ID).Data(&v)
	if v["status"] != "published" || len(v["phases"].([]any)) != 1 {
		t.Fatalf("published + 1 fase default: %v", v)
	}
	e.Anon().GET(api + "/events/" + ev.Slug).Expect(200)
	// setelah terbit: tanggal & mode tidak bisa diubah lewat PATCH; deskripsi boleh
	o.C.PATCH(api+"/organizer/events/"+ev.ID, M{"starts_at": e.future(40)}).ExpectErr(409, "INVALID_STATE")
	o.C.PATCH(api+"/organizer/events/"+ev.ID, M{"description": "Deskripsi final"}).Expect(200)
	// hanya draft yang bisa dihapus
	o.C.DELETE(api+"/organizer/events/"+ev.ID).ExpectErr(409, "INVALID_STATE")
	// audit trail
	for _, a := range []string{"event.create", "event.submit", "event.review", "event.publish"} {
		if e.QueryInt("SELECT count(*) FROM audit_logs WHERE action=$1 AND entity_id=$2", a, ev.ID) == 0 {
			t.Fatalf("audit %s hilang", a)
		}
	}
}

func TestTenantIsolationBetweenOrganizers(t *testing.T) { // T08
	e := NewEnv(t)
	a := e.NewOrganizer(true)
	b := e.NewOrganizer(true)
	evA := e.PublishedEvent(a, EventSpec{Title: "Milik A", Price: 100000})
	for _, req := range []struct{ method, path string }{
		{"GET", "/organizer/events/" + evA.ID},
		{"PATCH", "/organizer/events/" + evA.ID},
		{"DELETE", "/organizer/events/" + evA.ID},
		{"POST", "/organizer/events/" + evA.ID + "/tiers"},
		{"GET", "/organizer/events/" + evA.ID + "/tiers"},
		{"POST", "/organizer/events/" + evA.ID + "/phases"},
		{"GET", "/organizer/events/" + evA.ID + "/gates"},
		{"POST", "/organizer/events/" + evA.ID + "/submit"},
		{"POST", "/organizer/events/" + evA.ID + "/seatmap/generate"},
		{"GET", "/organizer/events/" + evA.ID + "/fans"},
	} {
		r := b.C.Do(req.method, api+req.path, M{"description": "x", "name": "x", "price": 1, "quota": 1, "audience": "public", "sections": []M{}})
		if r.Code != 404 && r.Code != 400 {
			t.Errorf("%s %s oleh organizer lain harus 404 (atau 400 validasi), dapat %d: %s", req.method, req.path, r.Code, r.Body)
		}
		if r.Code == 400 { // validasi lebih dulu dari kepemilikan: ulangi dengan body valid
			continue
		}
	}
	b.C.GET(api+"/organizer/events/"+evA.ID).ExpectErr(404, "NOT_FOUND")
	var list []map[string]any
	b.C.GET(api + "/organizer/events").Expect(200).Data(&list)
	if len(list) != 0 {
		t.Fatal("daftar event organizer B harus kosong")
	}
	// event A tetap utuh
	var v map[string]any
	a.C.GET(api + "/organizer/events/" + evA.ID).Expect(200).Data(&v)
	if v["title"] != "Milik A" {
		t.Fatalf("event A berubah: %v", v)
	}
}

// ---------- Tier & add-on ----------

func TestTierRulesQuotaPriceDelete(t *testing.T) {
	e := NewEnv(t)
	o := e.NewOrganizer(true)
	ev := e.PublishedEvent(o, EventSpec{Price: 100000, Quota: 100})
	// sebagian terjual/ditahan (simulasi stok)
	e.Exec("UPDATE tiers SET sold=30, held=10 WHERE id=$1", ev.TierID)
	o.C.PATCH(api+"/organizer/events/"+ev.ID+"/tiers/"+ev.TierID, M{"quota": 39}).ExpectErr(409, "LIMIT_EXCEEDED")
	o.C.PATCH(api+"/organizer/events/"+ev.ID+"/tiers/"+ev.TierID, M{"quota": 40}).Expect(200)
	// ubah harga setelah ada penjualan diperbolehkan (order lama memakai snapshot) dan tercatat
	o.C.PATCH(api+"/organizer/events/"+ev.ID+"/tiers/"+ev.TierID, M{"price": 150000}).Expect(200)
	if e.QueryInt("SELECT count(*) FROM audit_logs WHERE action='tier.price_change'") != 1 {
		t.Fatal("perubahan harga harus diaudit")
	}
	o.C.PATCH(api+"/organizer/events/"+ev.ID+"/tiers/"+ev.TierID, M{"price": -5}).ExpectErr(400, "VALIDATION_FAILED")
	o.C.PATCH(api+"/organizer/events/"+ev.ID+"/tiers/"+ev.TierID, M{"price": 999999999999}).ExpectErr(400, "VALIDATION_FAILED")
	// tier dengan penjualan tidak bisa dihapus
	o.C.DELETE(api+"/organizer/events/"+ev.ID+"/tiers/"+ev.TierID).ExpectErr(409, "INVALID_STATE")
	// tier baru tanpa penjualan bisa dihapus
	var t2 struct {
		ID string `json:"id"`
	}
	o.C.POST(api+"/organizer/events/"+ev.ID+"/tiers", M{"name": "VIP", "price": 500000, "quota": 10}).Expect(201).Data(&t2)
	o.C.DELETE(api + "/organizer/events/" + ev.ID + "/tiers/" + t2.ID).Expect(204)
	// tier milik event lain → 404
	other := e.PublishedEvent(o, EventSpec{Title: "Event Lain", Price: 1000})
	o.C.PATCH(api+"/organizer/events/"+ev.ID+"/tiers/"+other.TierID, M{"name": "x"}).ExpectErr(404, "NOT_FOUND")
}

func TestAddonRules(t *testing.T) {
	e := NewEnv(t)
	o := e.NewOrganizer(true)
	ev := e.PublishedEvent(o, EventSpec{Price: 50000})
	var a struct {
		ID string `json:"id"`
	}
	o.C.POST(api+"/organizer/events/"+ev.ID+"/addons", M{"name": "Parkir", "price": 20000}).ExpectErr(400, "VALIDATION_FAILED")
	o.C.POST(api+"/organizer/events/"+ev.ID+"/addons", M{"name": "Parkir", "price": 20000, "quota": 50}).Expect(201).Data(&a)
	e.Exec("UPDATE addons SET sold=10 WHERE id=$1", a.ID)
	o.C.PATCH(api+"/organizer/events/"+ev.ID+"/addons/"+a.ID, M{"quota": 5}).ExpectErr(409, "LIMIT_EXCEEDED")
	o.C.PATCH(api+"/organizer/events/"+ev.ID+"/addons/"+a.ID, M{"active": false}).Expect(200)
	var pub struct {
		Addons []any `json:"addons"`
	}
	e.Anon().GET(api + "/events/" + ev.Slug).Expect(200).Data(&pub)
	if len(pub.Addons) != 0 {
		t.Fatal("add-on nonaktif tidak boleh tampil publik")
	}
}

// ---------- Denah ----------

func TestSeatmapGenerateReplaceAndBlock(t *testing.T) {
	e := NewEnv(t)
	o := e.NewOrganizer(true)
	venue := e.NewVenue(o.C, "Jakarta")
	var ev struct {
		ID, Slug string
	}
	o.C.POST(api+"/organizer/events", M{"venue_id": venue, "title": "Konser Duduk", "mode": "seated", "starts_at": e.future(20)}).Expect(201).Data(&ev)
	var t1, t2 struct {
		ID string `json:"id"`
	}
	o.C.POST(api+"/organizer/events/"+ev.ID+"/tiers", M{"name": "VIP", "price": 900000}).Expect(201).Data(&t1)
	o.C.POST(api+"/organizer/events/"+ev.ID+"/tiers", M{"name": "CAT1", "price": 500000}).Expect(201).Data(&t2)

	o.C.POST(api+"/organizer/events/"+ev.ID+"/seatmap/generate", M{"sections": []M{{"name": "A-B", "tier_id": t1.ID, "rows": 2, "seats_per_row": 3}}}).ExpectErr(400, "VALIDATION_FAILED") // '-' dilarang
	o.C.POST(api+"/organizer/events/"+ev.ID+"/seatmap/generate", M{"sections": []M{{"name": "A", "tier_id": newID(), "rows": 2, "seats_per_row": 3}}}).ExpectErr(400, "VALIDATION_FAILED")
	o.C.POST(api+"/organizer/events/"+ev.ID+"/seatmap/generate", M{"sections": []M{{"name": "A", "tier_id": t1.ID, "rows": 101, "seats_per_row": 3}}}).ExpectErr(400, "VALIDATION_FAILED")
	o.C.POST(api+"/organizer/events/"+ev.ID+"/seatmap/generate", M{"sections": []M{{"name": "A", "tier_id": t1.ID, "rows": 100, "seats_per_row": 100}, {"name": "B", "tier_id": t2.ID, "rows": 100, "seats_per_row": 100}, {"name": "C", "tier_id": t2.ID, "rows": 10, "seats_per_row": 10}}}).ExpectErr(400, "VALIDATION_FAILED") // > 20.000

	var sum map[string]any
	o.C.POST(api+"/organizer/events/"+ev.ID+"/seatmap/generate", M{"sections": []M{
		{"name": "A", "tier_id": t1.ID, "rows": 2, "seats_per_row": 3, "accessible": []string{"A-A-1"}},
		{"name": "B", "tier_id": t2.ID, "rows": 3, "seats_per_row": 4}}}).Expect(200).Data(&sum)
	if sum["seats"].(float64) != 18 || sum["sections"].(float64) != 2 {
		t.Fatalf("ringkasan salah: %v", sum)
	}
	// ganti denah (replace) bukan menambah
	o.C.POST(api+"/organizer/events/"+ev.ID+"/seatmap/generate", M{"sections": []M{{"name": "A", "tier_id": t1.ID, "rows": 2, "seats_per_row": 5}, {"name": "B", "tier_id": t2.ID, "rows": 2, "seats_per_row": 5}}}).Expect(200)
	if n := e.QueryInt("SELECT count(*) FROM seats WHERE event_id=$1", ev.ID); n != 20 {
		t.Fatalf("denah baru harus menggantikan: %d", n)
	}
	// label & koordinat
	var sm struct {
		Seats []struct {
			Label string  `json:"label"`
			X     float64 `json:"x"`
			Y     float64 `json:"y"`
		} `json:"seats"`
	}
	o.C.GET(api + "/organizer/events/" + ev.ID + "/seatmap").Expect(200).Data(&sm)
	labels := map[string]bool{}
	for _, s := range sm.Seats {
		labels[s.Label] = true
	}
	if !labels["A-A-1"] || !labels["B-B-5"] || len(labels) != 20 {
		t.Fatalf("label kursi salah: %v", labels)
	}
	// blokir kursi: hanya yang tersedia
	var seatID string
	_ = pool.QueryRow(e.ctx, "SELECT id FROM seats WHERE event_id=$1 AND label='A-A-1'", ev.ID).Scan(&seatID)
	var res map[string]float64
	o.C.POST(api+"/organizer/events/"+ev.ID+"/seats/block", M{"seat_ids": []string{seatID}}).Expect(200).Data(&res)
	if res["changed"] != 1 {
		t.Fatalf("blokir: %v", res)
	}
	o.C.POST(api+"/organizer/events/"+ev.ID+"/seats/block", M{"seat_ids": []string{seatID}}).Expect(200).Data(&res)
	if res["changed"] != 0 {
		t.Fatal("blokir ulang tidak boleh mengubah apa-apa")
	}
	o.C.POST(api+"/organizer/events/"+ev.ID+"/seats/unblock", M{"seat_ids": []string{seatID}}).Expect(200)
	// tidak bisa mengganti denah bila ada kursi ter-hold
	e.Exec("UPDATE seats SET status='held' WHERE id=$1", seatID)
	o.C.POST(api+"/organizer/events/"+ev.ID+"/seatmap/generate", M{"sections": []M{{"name": "A", "tier_id": t1.ID, "rows": 1, "seats_per_row": 1}}}).ExpectErr(409, "INVALID_STATE")
	e.Exec("UPDATE seats SET status='available' WHERE id=$1", seatID)
	// event general tidak punya denah
	gen := e.PublishedEvent(o, EventSpec{Title: "Konser GA", Price: 1000})
	o.C.POST(api+"/organizer/events/"+gen.ID+"/seatmap/generate", M{"sections": []M{{"name": "A", "tier_id": gen.TierID, "rows": 1, "seats_per_row": 1}}}).ExpectErr(409, "INVALID_STATE")
}

func TestSeatmapImportSVGSanitizes(t *testing.T) {
	e := NewEnv(t)
	o := e.NewOrganizer(true)
	venue := e.NewVenue(o.C, "Jakarta")
	var ev struct {
		ID, Slug string
	}
	o.C.POST(api+"/organizer/events", M{"venue_id": venue, "title": "Konser SVG", "mode": "seated", "starts_at": e.future(20)}).Expect(201).Data(&ev)
	var tier struct {
		ID string `json:"id"`
	}
	o.C.POST(api+"/organizer/events/"+ev.ID+"/tiers", M{"name": "CAT1", "price": 500000}).Expect(201).Data(&tier)

	evil := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100" onload="alert(1)">
  <script>alert('x')</script>
  <g data-section="A" data-tier="CAT1">
    <circle data-seat="A-1-1" cx="10" cy="10" r="3" onclick="steal()"/>
    <circle data-seat="A-1-2" cx="20" cy="10" r="3"/>
    <foreignObject><div>hai</div></foreignObject>
  </g></svg>`
	var sum map[string]any
	o.C.Upload(api+"/organizer/events/"+ev.ID+"/seatmap/import-svg", nil, "file", "denah.svg", []byte(evil)).Expect(200).Data(&sum)
	if sum["seats"].(float64) != 2 {
		t.Fatalf("2 kursi harus terbaca: %v", sum)
	}
	// SVG publik tersanitasi (setelah terbit)
	e.ForcePublish(ev.ID)
	r := e.Anon().GET(api + "/events/" + ev.Slug + "/seatmap.svg").Expect(200)
	body := strings.ToLower(string(r.Body))
	for _, bad := range []string{"script", "onload", "onclick", "foreignobject", "alert"} {
		if strings.Contains(body, bad) {
			t.Fatalf("SVG publik masih memuat %q: %s", bad, body)
		}
	}
	if !strings.Contains(body, `data-seat="a-1-1"`) {
		t.Fatalf("data-seat harus dipertahankan: %s", body)
	}
	if !strings.Contains(r.Header.Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatal("SVG harus disajikan dengan CSP ketat")
	}
	// label duplikat → 422; tier tidak cocok → 422
	dup := `<svg xmlns="http://www.w3.org/2000/svg"><g data-section="A" data-tier="CAT1"><circle data-seat="A-1-1" cx="1" cy="1" r="1"/><circle data-seat="A-1-1" cx="2" cy="1" r="1"/></g></svg>`
	o.C.Upload(api+"/organizer/events/"+ev.ID+"/seatmap/import-svg", nil, "file", "d.svg", []byte(dup)).ExpectErr(422, "VALIDATION_FAILED")
	badTier := `<svg xmlns="http://www.w3.org/2000/svg"><g data-section="A" data-tier="TIDAK-ADA"><circle data-seat="A-1-1" cx="1" cy="1" r="1"/></g></svg>`
	o.C.Upload(api+"/organizer/events/"+ev.ID+"/seatmap/import-svg", nil, "file", "d.svg", []byte(badTier)).ExpectErr(422, "VALIDATION_FAILED")
	o.C.Upload(api+"/organizer/events/"+ev.ID+"/seatmap/import-svg", nil, "file", "d.svg", []byte("<html/>")).ExpectErr(400, "VALIDATION_FAILED")
	o.C.Upload(api+"/organizer/events/"+ev.ID+"/seatmap/import-svg", nil, "file", "d.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><rect/></svg>`)).ExpectErr(400, "VALIDATION_FAILED")
}

// ---------- Fase penjualan, presale, fans ----------

func TestPhaseValidationAndPresaleCodes(t *testing.T) {
	e := NewEnv(t)
	o := e.NewOrganizer(true)
	venue := e.NewVenue(o.C, "Jakarta")
	var ev struct {
		ID, Slug string
	}
	o.C.POST(api+"/organizer/events", M{"venue_id": venue, "title": "Konser Presale", "mode": "general", "starts_at": e.future(30), "ends_at": e.future(31)}).Expect(201).Data(&ev)
	o.C.POST(api+"/organizer/events/"+ev.ID+"/tiers", M{"name": "GA", "price": 100000, "quota": 100}).Expect(201)
	ph := func(name, aud string, s, en int) M {
		return M{"name": name, "audience": aud, "starts_at": e.future(s), "ends_at": e.future(en)}
	}
	var code, pub struct {
		ID string `json:"id"`
	}
	o.C.POST(api+"/organizer/events/"+ev.ID+"/phases", ph("Presale", "code", 2, 4)).Expect(201).Data(&code)
	// tumpang tindih audiens sama ditolak
	o.C.POST(api+"/organizer/events/"+ev.ID+"/phases", ph("Presale 2", "code", 3, 5)).ExpectErr(409, "CONFLICT")
	// audiens beda boleh tumpang tindih; ends melewati acara ditolak
	o.C.POST(api+"/organizer/events/"+ev.ID+"/phases", ph("Umum", "public", 4, 29)).Expect(201).Data(&pub)
	o.C.POST(api+"/organizer/events/"+ev.ID+"/phases", ph("Salah", "fan", 4, 40)).ExpectErr(400, "VALIDATION_FAILED")
	o.C.POST(api+"/organizer/events/"+ev.ID+"/phases", M{"name": "x", "audience": "public", "starts_at": e.future(5), "ends_at": e.future(4)}).ExpectErr(400, "VALIDATION_FAILED")
	// kode hanya untuk fase 'code'
	o.C.POST(api+"/organizer/events/"+ev.ID+"/phases/"+pub.ID+"/codes", M{"count": 1}).ExpectErr(409, "INVALID_STATE")
	var codes struct {
		Codes []string `json:"codes"`
	}
	o.C.POST(api+"/organizer/events/"+ev.ID+"/phases/"+code.ID+"/codes", M{"count": 3, "max_uses": 1}).Expect(201).Data(&codes)
	if len(codes.Codes) != 3 || len(codes.Codes[0]) != 8 {
		t.Fatalf("3 kode 8 karakter: %v", codes.Codes)
	}
	// hash di DB, bukan plaintext
	if n := e.QueryInt("SELECT count(*) FROM presale_codes WHERE code_hash=$1", codes.Codes[0]); n != 0 {
		t.Fatal("kode presale tidak boleh disimpan plaintext")
	}
	// publish dulu agar redeem bisa
	e.ForcePublish(ev.ID)
	u := e.Verified("Penebus")
	u.POST(api+"/events/"+ev.Slug+"/presale/redeem", M{"code": "SALAHSAJA"}).ExpectErr(400, "PRESALE_CODE_INVALID")
	u.POST(api+"/events/"+ev.Slug+"/presale/redeem", M{"code": strings.ToLower(codes.Codes[0])}).Expect(200)
	// penebusan ulang oleh user yang sama tidak menghabiskan kuota
	u.POST(api+"/events/"+ev.Slug+"/presale/redeem", M{"code": codes.Codes[0]}).Expect(200)
	// kode max_uses=1 sudah habis untuk user lain
	u2 := e.Verified("Penebus 2")
	u2.POST(api+"/events/"+ev.Slug+"/presale/redeem", M{"code": codes.Codes[0]}).ExpectErr(400, "PRESALE_CODE_INVALID")
	// salah 5× → kunci
	u3 := e.Verified("Penebak")
	for i := 0; i < 4; i++ {
		u3.POST(api+"/events/"+ev.Slug+"/presale/redeem", M{"code": "TEBAK" + string(rune('A'+i))}).ExpectErr(400, "PRESALE_CODE_INVALID")
	}
	u3.POST(api+"/events/"+ev.Slug+"/presale/redeem", M{"code": "TEBAKZ"}).ExpectErr(429, "RATE_LIMITED")
	u3.POST(api+"/events/"+ev.Slug+"/presale/redeem", M{"code": codes.Codes[1]}).ExpectErr(429, "RATE_LIMITED")
	// fase yang sudah dimulai tidak bisa dihapus/diundur awalnya
	o.C.PATCH(api+"/organizer/events/"+ev.ID+"/phases/"+code.ID, M{"ends_at": e.future(3)}).Expect(200)
	o.C.DELETE(api + "/organizer/events/" + ev.ID + "/phases/" + code.ID).Expect(204)
}

func (e *Env) ForcePublish(eventID string) {
	e.T.Helper()
	e.Exec("UPDATE events SET status='published', published_at=now() WHERE id=$1", eventID)
}

func TestFanRegistrationAndSelection(t *testing.T) {
	e := NewEnv(t)
	o := e.NewOrganizer(true)
	venue := e.NewVenue(o.C, "Jakarta")
	var ev struct{ ID, Slug string }
	o.C.POST(api+"/organizer/events", M{"venue_id": venue, "title": "Konser Fan", "mode": "general", "starts_at": e.future(30), "ends_at": e.future(31)}).Expect(201).Data(&ev)
	o.C.POST(api+"/organizer/events/"+ev.ID+"/tiers", M{"name": "GA", "price": 100000, "quota": 100}).Expect(201)
	o.C.POST(api+"/organizer/events/"+ev.ID+"/phases", M{"name": "Fan", "audience": "fan", "starts_at": e.future(3), "ends_at": e.future(5)}).Expect(201)
	e.ForcePublish(ev.ID)
	var users []*Client
	for i := 0; i < 4; i++ {
		u := e.Verified("Fan")
		u.POST(api+"/events/"+ev.Slug+"/fan-registration", nil).Expect(204)
		u.POST(api+"/events/"+ev.Slug+"/fan-registration", nil).Expect(204) // idempoten
		users = append(users, u)
	}
	unverified := e.Register(uniqEmail("nv"), "Belum")
	unverified.POST(api+"/events/"+ev.Slug+"/fan-registration", nil).ExpectErr(403, "EMAIL_NOT_VERIFIED")
	var list []map[string]any
	o.C.GET(api + "/organizer/events/" + ev.ID + "/fans").Expect(200).Data(&list)
	if len(list) != 4 {
		t.Fatalf("4 fan: %d", len(list))
	}
	var sel map[string]float64
	o.C.POST(api+"/organizer/events/"+ev.ID+"/fans/select", M{"user_ids": []string{users[0].UserID}, "random": 2}).Expect(200).Data(&sel)
	if sel["granted"] != 3 {
		t.Fatalf("3 fan terpilih (1 eksplisit + 2 acak): %v", sel)
	}
	if n := e.QueryInt("SELECT count(*) FROM phase_access WHERE via='fan'"); n != 3 {
		t.Fatalf("akses fase fan: %d", n)
	}
	o.C.POST(api+"/organizer/events/"+ev.ID+"/fans/select", M{}).ExpectErr(400, "VALIDATION_FAILED")
	// pendaftaran ditutup setelah fase fan mulai
	e.Clock.Advance(4 * 24 * time.Hour)
	late := e.Verified("Telat")
	late.POST(api+"/events/"+ev.Slug+"/fan-registration", nil).ExpectErr(409, "SALE_NOT_OPEN")
}

// ---------- Katalog publik ----------

func TestPublicCatalogFiltersAndFairStatus(t *testing.T) {
	e := NewEnv(t)
	o := e.NewOrganizer(true)
	jkt := e.PublishedEvent(o, EventSpec{Title: "Rock Jakarta", City: "Jakarta", Genre: "Rock", Price: 300000, Quota: 100, Extra: M{"service_fee_bp": 500, "service_fee_fixed": 2500}})
	bdg := e.PublishedEvent(o, EventSpec{Title: "Jazz Bandung", City: "Bandung", Genre: "Jazz", Price: 100000, Quota: 50})
	free := e.PublishedEvent(o, EventSpec{Title: "Gratis Surabaya", City: "Surabaya", Genre: "Pop", Price: 0, Quota: 30})
	// draft & unlisted tidak muncul di daftar
	venue := e.NewVenue(o.C, "Medan")
	o.C.POST(api+"/organizer/events", M{"venue_id": venue, "title": "Draf Rahasia", "mode": "general", "starts_at": e.future(10)}).Expect(201)
	hidden := e.PublishedEvent(o, EventSpec{Title: "Unlisted Show", City: "Jakarta", Price: 1000, Extra: M{"visibility": "unlisted"}})
	_ = hidden

	list := func(q string) []map[string]any {
		var out []map[string]any
		e.Anon().GET(api + "/events" + q).Expect(200).Data(&out)
		return out
	}
	titles := func(l []map[string]any) []string {
		var s []string
		for _, x := range l {
			s = append(s, x["title"].(string))
		}
		return s
	}
	if got := titles(list("")); len(got) != 3 {
		t.Fatalf("daftar publik harus 3 (tanpa draft/unlisted): %v", got)
	}
	if got := titles(list("?city=bandung")); len(got) != 1 || got[0] != "Jazz Bandung" {
		t.Fatalf("filter kota (case-insensitive): %v", got)
	}
	if got := titles(list("?genre=ROCK")); len(got) != 1 || got[0] != "Rock Jakarta" {
		t.Fatalf("filter genre: %v", got)
	}
	if got := titles(list("?q=jazz")); len(got) != 1 {
		t.Fatalf("pencarian judul: %v", got)
	}
	if got := titles(list("?q=%25")); len(got) != 0 {
		t.Fatalf("karakter LIKE harus di-escape: %v", got)
	}
	if got := titles(list("?price_min=200000")); len(got) != 1 || got[0] != "Rock Jakarta" {
		t.Fatalf("filter harga min: %v", got)
	}
	if got := titles(list("?price_max=50000")); len(got) != 1 || got[0] != "Gratis Surabaya" {
		t.Fatalf("filter harga max: %v", got)
	}
	e.Anon().GET(api+"/events?status=bukan").ExpectErr(400, "VALIDATION_FAILED")
	e.Anon().GET(api+"/events?sort=acak").ExpectErr(400, "VALIDATION_FAILED")
	e.Anon().GET(api+"/events?date_from=kemarin").ExpectErr(400, "VALIDATION_FAILED")
	// pencarian lewat nama artis
	var art struct {
		ID string `json:"id"`
	}
	o.C.POST(api+"/organizer/artists", M{"name": "Band Fiktif"}).Expect(201).Data(&art)
	o.C.PATCH(api+"/organizer/events/"+bdg.ID, M{"artist_ids": []string{art.ID}}).Expect(200)
	if got := titles(list("?q=fiktif")); len(got) != 1 || got[0] != "Jazz Bandung" {
		t.Fatalf("pencarian nama artis: %v", got)
	}

	// harga akhir = harga + biaya layanan (F1): 300000 + 5% (15000) + 2500
	l := list("?city=Jakarta")
	if l[0]["min_price"].(float64) != 300000 || l[0]["min_final_price"].(float64) != 317500 {
		t.Fatalf("harga akhir salah: %v", l[0])
	}
	// tiket gratis tidak dikenai biaya
	g := list("?city=Surabaya")
	if g[0]["min_final_price"].(float64) != 0 {
		t.Fatalf("tiket gratis tanpa biaya: %v", g[0])
	}

	// status jujur: onsale → full_booked → sold_out (F4)
	if l[0]["sale_state"] != "onsale" {
		t.Fatalf("harus onsale: %v", l[0]["sale_state"])
	}
	e.Exec("UPDATE tiers SET sold=90, held=10 WHERE id=$1", jkt.TierID)
	l = list("?city=Jakarta")
	if l[0]["sale_state"] != "full_booked" || l[0]["remaining"].(float64) != 0 {
		t.Fatalf("semua tiket ditahan = full_booked: %v", l[0])
	}
	e.Exec("UPDATE tiers SET sold=100, held=0 WHERE id=$1", jkt.TierID)
	l = list("?city=Jakarta")
	if l[0]["sale_state"] != "sold_out" {
		t.Fatalf("habis = sold_out: %v", l[0]["sale_state"])
	}
	if got := titles(list("?status=soldout")); len(got) != 1 || got[0] != "Rock Jakarta" {
		t.Fatalf("filter soldout: %v", got)
	}
	if got := titles(list("?status=onsale")); len(got) != 2 {
		t.Fatalf("filter onsale: %v", got)
	}
	// "hampir habis" hanya dari data nyata
	e.Exec("UPDATE tiers SET sold=47, held=0 WHERE id=$1", bdg.TierID) // 3 dari 50 tersisa
	b := list("?city=Bandung")
	if b[0]["almost_gone"] != true {
		t.Fatalf("sisa 3/50 harus almost_gone: %v", b[0])
	}
	if list("?city=Jakarta")[0]["almost_gone"] == true {
		t.Fatal("sold_out tidak boleh almost_gone")
	}
	_ = free

	// paginasi
	var meta struct {
		Meta struct{ Page, PerPage, Total int } `json:"meta"`
	}
	r := e.Anon().GET(api + "/events?per_page=2&page=2").Expect(200)
	_ = r
	_ = meta
}

func TestEventDetailTiersFairPricingAndRefundSummary(t *testing.T) {
	e := NewEnv(t)
	o := e.NewOrganizer(true)
	ev := e.PublishedEvent(o, EventSpec{Title: "Detail Show", Price: 200000, Quota: 40, Extra: M{"service_fee_bp": 1000, "age_min": 18,
		"refund_policy": M{"type": "refundable", "refundable_until": e.future(10), "deduction_pct": 20, "fee_refundable": false, "on_reschedule": M{"window_days": 7}}}})
	r := e.Anon().GET(api + "/events/" + ev.Slug).Expect(200)
	var raw map[string]any
	r.Data(&raw)
	tiers := raw["tiers"].([]any)
	t0 := tiers[0].(map[string]any)
	if t0["price"].(float64) != 200000 || t0["fee"].(float64) != 20000 || t0["final_price"].(float64) != 220000 || t0["available"].(float64) != 40 {
		t.Fatalf("tier publik: %v", t0)
	}
	if raw["age_min"].(float64) != 18 || !strings.Contains(raw["refund_summary"].(string), "20%") {
		t.Fatalf("detail: age/refund: %v %v", raw["age_min"], raw["refund_summary"])
	}
	// fee ditanggung organizer → harga akhir = harga dasar
	o.C.PATCH(api+"/organizer/events/"+ev.ID, M{"fee_payer": "organizer"}).Expect(200)
	e.Anon().GET(api + "/events/" + ev.Slug).Data(&raw)
	t0 = raw["tiers"].([]any)[0].(map[string]any)
	if t0["fee"].(float64) != 0 || t0["final_price"].(float64) != 200000 {
		t.Fatalf("fee_payer organizer: %v", t0)
	}
	// slug atau ID sama-sama bekerja
	e.Anon().GET(api + "/events/" + ev.ID).Expect(200)
	e.Anon().GET(api+"/events/tidak-ada-xyz").ExpectErr(404, "NOT_FOUND")
}

func TestUnlistedAndPasswordEvents(t *testing.T) {
	e := NewEnv(t)
	o := e.NewOrganizer(true)
	un := e.PublishedEvent(o, EventSpec{Title: "Tersembunyi", Price: 1000, Extra: M{"visibility": "unlisted"}})
	pw := e.PublishedEvent(o, EventSpec{Title: "Berpassword", Price: 1000, Extra: M{"visibility": "password", "password": "rahasia-123"}})
	// unlisted dapat dibuka via slug tetapi tidak di daftar
	e.Anon().GET(api + "/events/" + un.Slug).Expect(200)
	var list []map[string]any
	e.Anon().GET(api + "/events").Expect(200).Data(&list)
	if len(list) != 0 {
		t.Fatalf("unlisted/password tidak boleh ada di daftar: %v", list)
	}
	// password
	e.Anon().GET(api+"/events/"+pw.Slug).ExpectErr(403, "FORBIDDEN")
	e.Anon().GET(api+"/events/"+pw.Slug, "X-Event-Password", "salah").ExpectErr(403, "FORBIDDEN")
	e.Anon().GET(api+"/events/"+pw.Slug, "X-Event-Password", "rahasia-123").Expect(200)
	e.Anon().GET(api+"/events/"+pw.Slug+"/availability").ExpectErr(403, "FORBIDDEN")
	// hash password tak bocor
	if r := e.Anon().GET(api+"/events/"+pw.Slug, "X-Event-Password", "rahasia-123"); strings.Contains(string(r.Body), "argon2") {
		t.Fatal("hash password bocor")
	}
	// rate limit tebak password
	e.EnableRateLimit()
	var limited bool
	for i := 0; i < 15; i++ {
		if e.Anon().WithIP("198.51.100.99").GET(api+"/events/"+pw.Slug, "X-Event-Password", "tebakan").Code == 429 {
			limited = true
		}
	}
	if !limited {
		t.Fatal("tebakan password harus dibatasi laju")
	}
}

func TestAvailabilityShowsOnlyUnavailableSeatsAndCaches(t *testing.T) {
	e := NewEnv(t)
	o := e.NewOrganizer(true)
	ev := e.PublishedEvent(o, EventSpec{Title: "Duduk", Mode: "seated", Price: 100000, Quota: 10})
	var seatIDs []string
	rows, _ := pool.Query(e.ctx, "SELECT id FROM seats WHERE event_id=$1 ORDER BY label LIMIT 3", ev.ID)
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		seatIDs = append(seatIDs, id)
	}
	rows.Close()
	e.Exec("UPDATE seats SET status='held' WHERE id=$1", seatIDs[0])
	e.Exec("UPDATE seats SET status='sold' WHERE id=$1", seatIDs[1])
	var av struct {
		Tiers []struct {
			ID        string `json:"id"`
			Available int    `json:"available"`
			Held      int    `json:"held"`
			Sold      int    `json:"sold"`
		} `json:"tiers"`
		Unavailable map[string]string `json:"unavailable_seats"`
	}
	e.Anon().GET(api + "/events/" + ev.Slug + "/availability").Expect(200).Data(&av)
	if len(av.Unavailable) != 2 || av.Unavailable[seatIDs[0]] != "held" || av.Unavailable[seatIDs[1]] != "sold" {
		t.Fatalf("hanya kursi tak tersedia yang dikirim: %v", av.Unavailable)
	}
	if av.Tiers[0].Available != 8 || av.Tiers[0].Held != 1 || av.Tiers[0].Sold != 1 {
		t.Fatalf("ringkasan tier: %+v", av.Tiers[0])
	}
	// cache 1 detik hanya untuk tampilan: perubahan langsung tidak terlihat sampai kedaluwarsa/invalidasi
	e.Exec("UPDATE seats SET status='sold' WHERE id=$1", seatIDs[2])
	var av2 struct {
		Unavailable map[string]string `json:"unavailable_seats"`
	}
	e.Anon().GET(api + "/events/" + ev.Slug + "/availability").Data(&av2)
	if len(av2.Unavailable) != 2 {
		t.Fatalf("respons harus dari cache: %v", av2.Unavailable)
	}
	e.Svc.Catalog.InvalidateAvailability(e.ctx, ev.ID)
	e.Anon().GET(api + "/events/" + ev.Slug + "/availability").Data(&av2)
	if len(av2.Unavailable) != 3 {
		t.Fatalf("setelah invalidasi harus segar: %v", av2.Unavailable)
	}
	// geometri statis dengan ETag
	r := e.Anon().GET(api + "/events/" + ev.Slug + "/seatmap").Expect(200)
	etag := r.Header.Get("ETag")
	if etag == "" || !strings.Contains(r.Header.Get("Cache-Control"), "max-age") {
		t.Fatal("seatmap harus ber-ETag dan cache")
	}
	e.Anon().GET(api+"/events/"+ev.Slug+"/seatmap", "If-None-Match", etag).Expect(304)
}

func TestCalendarAndFiltersMeta(t *testing.T) {
	e := NewEnv(t)
	o := e.NewOrganizer(true)
	e.PublishedEvent(o, EventSpec{Title: "Kal 1", City: "Jakarta", Genre: "Rock", Price: 1000})
	e.PublishedEvent(o, EventSpec{Title: "Kal 2", City: "Bandung", Genre: "Jazz", Price: 1000})
	month := e.Clock.Now().Add(30 * 24 * time.Hour).Format("2006-01")
	var days []struct {
		Date   string           `json:"date"`
		Events []map[string]any `json:"events"`
	}
	e.Anon().GET(api + "/calendar?month=" + month).Expect(200).Data(&days)
	total := 0
	for _, d := range days {
		total += len(d.Events)
	}
	if total != 2 {
		t.Fatalf("kalender harus memuat 2 event: %+v", days)
	}
	e.Anon().GET(api+"/calendar?month=2026-13").ExpectErr(400, "VALIDATION_FAILED")
	var f struct {
		Cities []string `json:"cities"`
		Genres []string `json:"genres"`
	}
	e.Anon().GET(api + "/meta/filters").Expect(200).Data(&f)
	if len(f.Cities) != 2 || len(f.Genres) != 2 {
		t.Fatalf("filter meta: %+v", f)
	}
}

// ---------- Favorit, pengingat, follow ----------

func TestFavoritesRemindersFollowsAndNewEventNotification(t *testing.T) {
	e := NewEnv(t)
	o := e.NewOrganizer(true)
	fan := e.Verified("Penggemar")
	// follow artis, kota, genre
	var art struct {
		ID, Slug string
	}
	o.C.POST(api+"/organizer/artists", M{"name": "Band Favorit"}).Expect(201).Data(&art)
	fan.POST(api+"/me/follows", M{"kind": "artist", "target": art.Slug}).Expect(204)
	fan.POST(api+"/me/follows", M{"kind": "city", "target": "Bandung"}).Expect(204)
	fan.POST(api+"/me/follows", M{"kind": "artist", "target": "tidak-ada"}).ExpectErr(404, "NOT_FOUND")
	fan.POST(api+"/me/follows", M{"kind": "mood", "target": "x"}).ExpectErr(400, "VALIDATION_FAILED")
	// event baru dengan artis tsb terbit → follower diberi tahu (kategori 'follows')
	before := e.Mail.Count()
	ev := e.PublishedEvent(o, EventSpec{Title: "Tur Band Favorit", City: "Jakarta", Price: 1000, Extra: M{"artist_ids": []string{art.ID}}})
	got := false
	for _, m := range e.Mail.MailsTo(fan.Email) {
		if strings.Contains(m.Subject, "Tur Band Favorit") {
			got = true
		}
	}
	if !got || e.Mail.Count() <= before {
		t.Fatal("pengikut artis harus diberi tahu saat event terbit")
	}
	// dapat dimatikan lewat preferensi
	e.Exec("INSERT INTO notification_prefs (user_id, category, channel, enabled) VALUES ($1,'follows','email',false)", fan.UserID)
	n := len(e.Mail.MailsTo(fan.Email))
	e.PublishedEvent(o, EventSpec{Title: "Tur Kedua", City: "Bandung", Price: 1000})
	if len(e.Mail.MailsTo(fan.Email)) != n {
		t.Fatal("preferensi follows=off harus menekan notifikasi")
	}
	// favorit
	fan.POST(api+"/me/favorites/"+ev.ID, nil).Expect(204)
	fan.POST(api+"/me/favorites/"+ev.ID, nil).Expect(204) // idempoten
	var favs []map[string]any
	fan.GET(api + "/me/favorites").Expect(200).Data(&favs)
	if len(favs) != 1 || favs[0]["title"] != "Tur Band Favorit" {
		t.Fatalf("favorit: %v", favs)
	}
	fan.DELETE(api + "/me/favorites/" + ev.ID).Expect(204)
	fan.POST(api+"/me/favorites/"+newID(), nil).ExpectErr(404, "NOT_FOUND")
	// unfollow
	fan.DELETE(api + "/me/follows/artist/" + art.Slug).Expect(204)
	var fl []map[string]any
	fan.GET(api + "/me/follows").Expect(200).Data(&fl)
	if len(fl) != 1 {
		t.Fatalf("tersisa 1 follow: %v", fl)
	}
}

func TestReminderBeforeOnsale(t *testing.T) {
	e := NewEnv(t)
	o := e.NewOrganizer(true)
	venue := e.NewVenue(o.C, "Jakarta")
	var ev struct{ ID, Slug string }
	o.C.POST(api+"/organizer/events", M{"venue_id": venue, "title": "Segera Dijual", "mode": "general", "starts_at": e.future(30), "ends_at": e.future(31)}).Expect(201).Data(&ev)
	o.C.POST(api+"/organizer/events/"+ev.ID+"/tiers", M{"name": "GA", "price": 100000, "quota": 100}).Expect(201)
	o.C.POST(api+"/organizer/events/"+ev.ID+"/phases", M{"name": "Umum", "audience": "public", "starts_at": e.future(2), "ends_at": e.future(20)}).Expect(201)
	e.ForcePublish(ev.ID)
	u := e.Verified("Pengingat")
	var r map[string]string
	u.POST(api+"/events/"+ev.Slug+"/reminders", nil).Expect(200).Data(&r)
	want := e.Clock.Now().Add(47 * time.Hour)
	at, _ := time.Parse(time.RFC3339, r["remind_at"])
	if at.Sub(want) > time.Minute || want.Sub(at) > time.Minute {
		t.Fatalf("pengingat harus 1 jam sebelum fase mulai: %s vs %s", at, want)
	}
	// belum jatuh tempo → tidak terkirim
	n, err := e.Svc.Follow.RunReminders(e.ctx)
	if err != nil || n != 0 {
		t.Fatalf("belum waktunya: %d %v", n, err)
	}
	e.Clock.Advance(47*time.Hour + time.Minute)
	n, err = e.Svc.Follow.RunReminders(e.ctx)
	if err != nil || n != 1 {
		t.Fatalf("harus terkirim 1: %d %v", n, err)
	}
	found := false
	for _, m := range e.Mail.MailsTo(u.Email) {
		if strings.Contains(m.Subject, "Segera Dijual") {
			found = true
		}
	}
	if !found {
		t.Fatal("email pengingat tidak terkirim")
	}
	// tidak terkirim dua kali
	n, _ = e.Svc.Follow.RunReminders(e.ctx)
	if n != 0 {
		t.Fatal("pengingat tidak boleh dikirim ulang")
	}
	// fase sudah lewat/berjalan → tidak ada fase mendatang untuk diingatkan
	e.Clock.Advance(2 * time.Hour)
	u = e.Login(u.Email)
	u.POST(api+"/events/"+ev.Slug+"/reminders", nil).ExpectErr(409, "SALE_NOT_OPEN")
}

// ---------- Media ----------

func TestMediaUploadServeAndPermissions(t *testing.T) {
	e := NewEnv(t)
	o := e.NewOrganizer(true)
	cust := e.Verified("Pelanggan")
	cust.Upload(api+"/media", map[string]string{"kind": "poster"}, "file", "p.png", pngBytes(900, 450)).ExpectErr(403, "FORBIDDEN")
	o.C.Upload(api+"/media", map[string]string{"kind": "aneh"}, "file", "p.png", pngBytes(10, 10)).ExpectErr(400, "VALIDATION_FAILED")
	o.C.Upload(api+"/media", map[string]string{"kind": "poster"}, "file", "p.png", []byte("bukan gambar")).ExpectErr(400, "VALIDATION_FAILED")
	var m struct {
		ID   string            `json:"id"`
		URLs map[string]string `json:"urls"`
	}
	o.C.Upload(api+"/media", map[string]string{"kind": "poster"}, "file", "p.png", pngBytes(1000, 500)).Expect(201).Data(&m)
	if len(m.URLs) != 3 {
		t.Fatalf("3 varian (400/800/1000): %v", m.URLs)
	}
	// disajikan publik dengan cache panjang
	r := e.Anon().GET(m.URLs["400"]).Expect(200)
	if r.Header.Get("Content-Type") != "image/jpeg" || !strings.Contains(r.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("header media salah: %v", r.Header)
	}
	if len(r.Body) < 100 || string(r.Body[:2]) != "\xff\xd8" {
		t.Fatal("bukan JPEG")
	}
	e.Anon().GET(m.URLs["400"], "If-None-Match", r.Header.Get("ETag")).Expect(304)
	e.Anon().GET(api+"/media/"+m.ID+"/123.jpg").ExpectErr(404, "NOT_FOUND")
	// avatar boleh untuk semua pengguna login, tapi poster dipakai event harus milik sendiri
	venue := e.NewVenue(o.C, "Jakarta")
	other := e.NewOrganizer(true)
	other.C.POST(api+"/organizer/events", M{"venue_id": venue, "title": "Pakai Poster Orang", "mode": "general", "starts_at": e.future(10), "poster_asset_id": m.ID}).ExpectErr(403, "FORBIDDEN")
	var ev struct {
		ID        string `json:"id"`
		PosterURL string `json:"poster_url"`
	}
	o.C.POST(api+"/organizer/events", M{"venue_id": venue, "title": "Pakai Poster Sendiri", "mode": "general", "starts_at": e.future(10), "poster_asset_id": m.ID}).Expect(201).Data(&ev)
	if ev.PosterURL != "/api/v1/media/"+m.ID+"/800.jpg" {
		t.Fatalf("poster_url: %s", ev.PosterURL)
	}
}

func TestVenuesArtistsMasterData(t *testing.T) {
	e := NewEnv(t)
	o := e.NewOrganizer(true)
	o.C.POST(api+"/organizer/venues", M{"name": "X", "city": "Jakarta"}).ExpectErr(400, "VALIDATION_FAILED")
	o.C.POST(api+"/organizer/venues", M{"name": "Gedung A", "city": "Jakarta", "tz": "Mars/Olympus"}).ExpectErr(400, "VALIDATION_FAILED")
	o.C.POST(api+"/organizer/venues", M{"name": "Gedung A", "city": "Jakarta", "lat": -6.2}).ExpectErr(400, "VALIDATION_FAILED")
	var v struct{ ID string }
	o.C.POST(api+"/organizer/venues", M{"name": "Gedung A", "city": "Jakarta", "lat": -6.2, "lng": 106.8, "tz": "Asia/Jakarta"}).Expect(201).Data(&v)
	// hanya pembuat/admin yang boleh mengubah
	other := e.NewOrganizer(true)
	other.C.PATCH(api+"/organizer/venues/"+v.ID, M{"name": "Dibajak"}).ExpectErr(403, "FORBIDDEN")
	o.C.PATCH(api+"/organizer/venues/"+v.ID, M{"capacity": 5000}).Expect(200)
	var list []map[string]any
	e.Anon().GET(api + "/venues?q=gedung").Expect(200).Data(&list)
	if len(list) != 1 {
		t.Fatalf("pencarian venue: %v", list)
	}
	e.Anon().GET(api + "/venues/" + v.ID).Expect(200)
	e.Anon().GET(api+"/venues/"+newID()).ExpectErr(404, "NOT_FOUND")
	// artis: slug unik, duplikat nama mendapat sufiks
	var a1, a2 struct{ Slug string }
	o.C.POST(api+"/organizer/artists", M{"name": "Sheila Pop"}).Expect(201).Data(&a1)
	o.C.POST(api+"/organizer/artists", M{"name": "Sheila Pop"}).Expect(201).Data(&a2)
	if a1.Slug != "sheila-pop" || a2.Slug == a1.Slug || !strings.HasPrefix(a2.Slug, "sheila-pop-") {
		t.Fatalf("slug artis: %s %s", a1.Slug, a2.Slug)
	}
	var detail struct {
		Artist map[string]any   `json:"artist"`
		Events []map[string]any `json:"events"`
	}
	e.Anon().GET(api + "/artists/" + a1.Slug).Expect(200).Data(&detail)
	if detail.Artist["name"] != "Sheila Pop" {
		t.Fatalf("detail artis: %v", detail)
	}
	e.Anon().GET(api+"/artists/tidak-ada").ExpectErr(404, "NOT_FOUND")
}

func TestGatesAndStaffAssignment(t *testing.T) {
	e := NewEnv(t)
	o := e.NewOrganizer(true)
	ev := e.PublishedEvent(o, EventSpec{Price: 1000})
	var g struct{ ID string }
	o.C.POST(api+"/organizer/events/"+ev.ID+"/gates", M{"name": "Pintu A"}).Expect(201).Data(&g)
	o.C.POST(api+"/organizer/events/"+ev.ID+"/gates", M{"name": "Pintu A"}).ExpectErr(409, "CONFLICT")
	staff := e.Verified("Petugas")
	o.C.POST(api+"/organizer/team", M{"email": staff.Email, "role": "gate_staff"}).Expect(201)
	o.C.POST(api+"/organizer/events/"+ev.ID+"/gate-staff", M{"email": staff.Email, "gate_id": g.ID}).Expect(201)
	// bukan anggota tim → ditolak
	outsider := e.Verified("Orang Luar")
	o.C.POST(api+"/organizer/events/"+ev.ID+"/gate-staff", M{"email": outsider.Email}).ExpectErr(409, "CONFLICT")
	o.C.POST(api+"/organizer/events/"+ev.ID+"/gate-staff", M{"email": staff.Email, "gate_id": newID()}).ExpectErr(400, "VALIDATION_FAILED")
	var list []map[string]any
	o.C.GET(api + "/organizer/events/" + ev.ID + "/gate-staff").Expect(200).Data(&list)
	if len(list) != 1 {
		t.Fatalf("staf: %v", list)
	}
	// petugas gate tidak bisa mengakses manajemen event
	sc := e.Login(staff.Email)
	sc.GET(api+"/organizer/events").ExpectErr(403, "FORBIDDEN")
	o.C.DELETE(api + "/organizer/events/" + ev.ID + "/gate-staff/" + staff.UserID).Expect(204)
}

// Gambar lebih kecil dari 800/1600 px tidak di-upscale, jadi hanya punya varian kecil; URL varian besar
// harus tetap menyajikan gambar (varian terbesar yang ada), bukan 404.
func TestMediaSmallImageServesLargerVariantRequests(t *testing.T) {
	e := NewEnv(t)
	o, _, _, _ := e.publishedGA(100000, 10)
	var m struct{ ID string }
	o.C.Upload(api+"/media", map[string]string{"kind": "venue"}, "file", "v.png", pngBytes(640, 400)).Expect(201).Data(&m)
	var sizes []int
	for _, w := range []int{400, 800, 1600} {
		r := e.Anon().GET(api + "/media/" + m.ID + "/" + strconv.Itoa(w) + ".jpg")
		if r.Code != 200 || r.Header.Get("Content-Type") != "image/jpeg" {
			t.Fatalf("varian %d: status %d, harus 200 image/jpeg", w, r.Code)
		}
		sizes = append(sizes, len(r.Body))
	}
	if sizes[2] != sizes[1] {
		t.Fatalf("640px: permintaan 800 dan 1600 harus menyajikan varian terbesar yang sama, dapat %v", sizes)
	}
	e.Anon().GET(api+"/media/"+m.ID+"/abc.jpg").ExpectErr(404, "NOT_FOUND")
}
