package apitest

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestNotificationPrefsDefaultsAndBroadcastRespect(t *testing.T) {
	e := NewEnv(t)
	o, evID, slug, tier := e.publishedGA(100000, 10)
	a := e.Verified("Setuju")
	b := e.Verified("Menolak")
	e.buyPaid(a, slug, tier, 1, "gopay")
	e.buyPaid(b, slug, tier, 1, "gopay")
	var p map[string]map[string]bool
	a.GET(api + "/me/notification-prefs").Expect(200).Data(&p)
	if p["marketing"]["email"] || !p["broadcasts"]["email"] {
		t.Fatalf("default: marketing mati (opt-in), broadcasts hidup: %v", p)
	}
	b.PUT(api+"/me/notification-prefs", M{"category": "broadcasts", "channel": "email", "enabled": false}).Expect(204)
	b.PUT(api+"/me/notification-prefs", M{"category": "bogus", "channel": "email", "enabled": false}).ExpectErr(400, "VALIDATION_FAILED")
	o.C.POST(api+"/organizer/events/"+evID+"/broadcasts", M{"subject": "Info penting", "body": "Gerbang dibuka pukul 17.00 hari ini."}).Expect(201)
	got := func(u *Client) bool {
		for _, m := range e.Mail.MailsTo(u.Email) {
			if strings.Contains(m.Subject, "Info penting") {
				return true
			}
		}
		return false
	}
	if !got(a) || got(b) {
		t.Fatalf("a menerima, b yang menolak tidak: a=%v b=%v", got(a), got(b))
	}
}

func TestPushSubscribeValidationAndDelivery(t *testing.T) {
	e := NewEnv(t)
	u := e.Verified("Push User")
	u.POST(api+"/me/push/subscriptions", M{"endpoint": "http://insecure.example/x", "p256dh": "k", "auth": "a"}).ExpectErr(400, "VALIDATION_FAILED")
	u.POST(api+"/me/push/subscriptions", M{"endpoint": "https://push.example/abc", "p256dh": "BKey", "auth": "auth"}).Expect(204)
	e.Svc.Notifier.SendPush(t.Context(), u.UserID, "reminders", "Tiket terbit", "Cek tiketmu", "/tickets")
	if e.QueryStr("SELECT status FROM notifications WHERE channel='push'") != "sent" {
		t.Fatal("push harus terkirim ke langganan")
	}
}

func TestPrivacyExportContainsOnlyOwnData(t *testing.T) {
	e := NewEnv(t)
	_, _, slug, tier := e.publishedGA(100000, 10)
	a := e.Verified("Pemilik Data")
	b := e.Verified("Orang Lain")
	e.buyPaid(a, slug, tier, 1, "gopay")
	e.buyPaid(b, slug, tier, 1, "gopay")
	var ex struct {
		ID, Status string
	}
	a.POST(api+"/me/privacy/exports", nil).Expect(202).Data(&ex)
	var list []struct {
		Status string `json:"status"`
		URL    string `json:"url"`
	}
	a.GET(api + "/me/privacy/exports").Expect(200).Data(&list)
	if len(list) != 1 || list[0].Status != "done" || list[0].URL == "" {
		t.Fatalf("ekspor selesai dengan URL: %+v", list)
	}
	key := e.QueryStr("SELECT object_key FROM export_jobs WHERE id=$1", ex.ID)
	rc, _, err := e.Store.Get(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(rc)
	s := string(body)
	if !strings.Contains(s, a.Email) || strings.Contains(s, b.Email) {
		t.Fatal("ekspor harus memuat data sendiri saja")
	}
	if !strings.Contains(s, `"orders"`) || !strings.Contains(s, `"tickets"`) {
		t.Fatal("ekspor memuat pesanan & tiket")
	}
	// kedaluwarsa → berkas dihapus job purge
	e.Clock.Advance(8 * 24 * time.Hour)
	if _, err := e.Svc.Privacy.Purge(t.Context()); err != nil {
		t.Fatal(err)
	}
	if e.QueryInt("SELECT count(*) FROM export_jobs WHERE object_key IS NOT NULL") != 0 {
		t.Fatal("berkas ekspor kedaluwarsa harus dibersihkan")
	}
	// batas 3 ekspor/hari
	a = e.Login(a.Email)
	for i := 0; i < 2; i++ {
		a.POST(api+"/me/privacy/exports", nil).Expect(202)
	}
}

func TestAccountDeletionAnonymizesButKeepsTransactions(t *testing.T) {
	e := NewEnv(t)
	_, evID, slug, tier := e.publishedGA(100000, 10)
	admin := e.UserWithRole("admin", "Admin Privasi")
	u := e.Verified("Mau Hapus")
	oid := e.buyPaid(u, slug, tier, 1, "gopay")

	u.POST(api+"/me/privacy/delete-account", M{"password": "salah-banget-123"}).ExpectErr(401, "UNAUTHENTICATED")
	var d struct{ ID, Status string }
	u.POST(api+"/me/privacy/delete-account", M{"password": testPassword, "reason": "tidak dipakai lagi"}).Expect(201).Data(&d)
	if d.Status != "identity_verified" {
		t.Fatalf("status: %s", d.Status)
	}
	u.POST(api+"/me/privacy/delete-account", M{"password": testPassword}).ExpectErr(409, "CONFLICT")
	// masih punya tiket event mendatang → pelaksanaan ditolak
	admin.POST(api+"/admin/privacy/requests/"+d.ID+"/execute", nil).ExpectErr(422, "VALIDATION_FAILED")
	// event selesai → bisa dieksekusi
	e.Exec("UPDATE events SET status='finished', starts_at=now()-interval '3 days', ends_at=now()-interval '2 days' WHERE id=$1", evID)
	admin.POST(api+"/admin/privacy/requests/"+d.ID+"/execute", nil).Expect(204)
	if st := e.QueryStr("SELECT status FROM users WHERE id=$1", u.UserID); st != "deleted" {
		t.Fatalf("user: %s", st)
	}
	if strings.Contains(e.QueryStr("SELECT email FROM users WHERE id=$1", u.UserID), "@example.com") {
		t.Fatal("email harus dianonimisasi")
	}
	// transaksi tetap ada (kewajiban hukum), PII di item pesanan hilang
	if e.QueryStr("SELECT status FROM orders WHERE id=$1", oid) != "paid" || e.QueryInt("SELECT count(*) FROM invoices WHERE order_id=$1", oid) != 1 {
		t.Fatal("order & invoice dipertahankan")
	}
	if e.QueryStr("SELECT attendee_name FROM order_items WHERE order_id=$1", oid) != "Anonim" {
		t.Fatal("nama peserta pada item order harus dianonimisasi")
	}
	// sesi lama tak berlaku; login ditolak
	u.GET(api + "/me/privacy/requests").Expect(401)
	e.Anon().POST(api+"/auth/login", M{"email": u.Email, "password": testPassword}).Expect(401)
	if e.QueryStr("SELECT status FROM dsr_requests WHERE id=$1", d.ID) != "executed" {
		t.Fatal("DSR executed")
	}
}

func TestDSRRejectNeedsReasonAndPrivacyComplaint(t *testing.T) {
	e := NewEnv(t)
	admin := e.UserWithRole("admin", "Admin Priv")
	u := e.Verified("Penolak")
	var d struct{ ID string }
	u.POST(api+"/me/privacy/delete-account", M{"password": testPassword}).Expect(201).Data(&d)
	admin.POST(api+"/admin/privacy/requests/"+d.ID+"/reject", M{"reason": "singkat"}).ExpectErr(400, "VALIDATION_FAILED")
	admin.POST(api+"/admin/privacy/requests/"+d.ID+"/reject", M{"reason": "Masih ada saldo organizer yang harus diselesaikan."}).Expect(204)
	var lst []struct{ Status string }
	u.GET(api + "/me/privacy/requests").Expect(200).Data(&lst)
	if len(lst) != 1 || lst[0].Status != "rejected_with_reason" {
		t.Fatalf("status: %+v", lst)
	}
	// pengaduan publik
	e.Anon().POST(api+"/privacy/complaints", M{"email": "warga@example.com", "subject": "Data saya bocor?", "body": "Saya menerima email spam setelah mendaftar."}).Expect(204)
	e.Anon().POST(api+"/privacy/complaints", M{"email": "bukan", "subject": "x", "body": "y"}).ExpectErr(400, "VALIDATION_FAILED")
	var cl []struct{ ID string }
	admin.GET(api + "/admin/privacy/complaints?status=open").Expect(200).Data(&cl)
	if len(cl) != 1 {
		t.Fatalf("pengaduan: %d", len(cl))
	}
	admin.POST(api+"/admin/privacy/complaints/"+cl[0].ID+"/close", nil).Expect(204)
	admin.POST(api+"/admin/privacy/complaints/"+cl[0].ID+"/close", nil).ExpectErr(404, "NOT_FOUND")
}

// Retensi outbox: baris yang sudah terbit >1 hari harus dibuang job purge; yang belum terbit atau masih baru tetap.
func TestPurgeRemovesOldPublishedOutboxOnly(t *testing.T) {
	e := NewEnv(t)
	e.Exec("DELETE FROM outbox")
	e.Exec("INSERT INTO outbox (channel, topic, payload, published_at) VALUES ('t.old','x','{}', now() - interval '2 days')")
	e.Exec("INSERT INTO outbox (channel, topic, payload, published_at) VALUES ('t.fresh','x','{}', now() - interval '1 hour')")
	e.Exec("INSERT INTO outbox (channel, topic, payload) VALUES ('t.pending','x','{}')")
	if _, err := e.Svc.Privacy.Purge(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := e.QueryInt("SELECT count(*) FROM outbox WHERE channel='t.old'"); n != 0 {
		t.Fatalf("outbox lama harus dibuang, sisa %d", n)
	}
	if n := e.QueryInt("SELECT count(*) FROM outbox WHERE channel IN ('t.fresh','t.pending')"); n != 2 {
		t.Fatalf("outbox baru/belum terbit harus tetap, dapat %d", n)
	}
}

func TestNotificationInboxTitlesUnreadAndRead(t *testing.T) {
	e := NewEnv(t)
	_, _, slug, tier := e.publishedGA(100000, 10)
	a := e.Verified("Pembeli Inbox")
	b := e.Verified("Orang Lain")
	e.buyPaid(a, slug, tier, 1, "gopay")

	type item struct {
		ID, Template, Title, Body string
		Read                      bool
	}
	var items []item
	a.GET(api + "/me/notifications").Expect(200).Data(&items)
	var paid *item
	for i := range items {
		if items[i].Template == "verify_email" || items[i].Template == "reset_password" {
			t.Fatalf("email bertautan rahasia tidak boleh tampil di kotak masuk: %s", items[i].Template)
		}
		if items[i].Template == "order_paid" {
			paid = &items[i]
		}
	}
	if paid == nil || paid.Title == "" || paid.Body == "" || paid.Read {
		t.Fatalf("order_paid harus tampil dengan judul/isi dan belum dibaca: %+v", paid)
	}
	var cnt struct{ Unread int }
	a.GET(api + "/me/notifications/unread-count").Expect(200).Data(&cnt)
	if cnt.Unread < 1 {
		t.Fatalf("unread harus >=1, dapat %d", cnt.Unread)
	}
	// Orang lain tidak bisa menandai milik a.
	b.POST(api+"/me/notifications/read", M{"ids": []string{paid.ID}}).Expect(204)
	a.GET(api + "/me/notifications/unread-count").Expect(200).Data(&cnt)
	if cnt.Unread < 1 {
		t.Fatal("tanda baca b tidak boleh memengaruhi a")
	}
	a.POST(api+"/me/notifications/read", M{"ids": []string{paid.ID}}).Expect(204)
	a.POST(api+"/me/notifications/read", M{"ids": []string{"bukan-id"}}).ExpectErr(400, "VALIDATION_FAILED")
	a.POST(api+"/me/notifications/read", M{}).Expect(204) // kosong = semua
	a.GET(api + "/me/notifications/unread-count").Expect(200).Data(&cnt)
	if cnt.Unread != 0 {
		t.Fatalf("setelah tandai semua, unread harus 0, dapat %d", cnt.Unread)
	}
}
