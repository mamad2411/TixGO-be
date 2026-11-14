package apitest

import (
	"strings"
	"testing"
	"time"
)

func TestCMSEditorialFlowSanitizeVersionsAndSeparation(t *testing.T) {
	e := NewEnv(t)
	ed := e.UserWithRole("editor", "Editor Satu")
	rev := e.UserWithRole("editor", "Editor Dua")
	base := api + "/admin/cms/pages"
	in := M{"kind": "help", "slug": "cara-refund", "locale": "id", "title": "Cara mengajukan refund", "category": "refund",
		"body": "## Langkah\n1. Buka pesanan\n\nParagraf **tebal**\n\n<script>alert(1)</script>\n\n<img src=x onerror=alert(2)> [klik](javascript:alert(3))",
		"seo":  M{"title": "Refund TixGo", "description": "Panduan refund", "evil": "x"}}
	e.Anon().GET(api+"/content/help/cara-refund").ExpectErr(404, "NOT_FOUND")
	e.Anon().POST(api+"/admin/cms/pages", in).Expect(401)
	u := e.Verified("Pembeli")
	u.POST(base, in).ExpectErr(403, "FORBIDDEN")
	var pg struct {
		ID, Status string
		Version    int
	}
	ed.POST(base, in).Expect(201).Data(&pg)
	if pg.Status != "draft" || pg.Version != 1 {
		t.Fatalf("%+v", pg)
	}
	ed.POST(base, in).ExpectErr(409, "CONFLICT") // draf ganda
	// belum terbit → publik 404; tak bisa publish dari draft
	e.Anon().GET(api+"/content/help/cara-refund").ExpectErr(404, "NOT_FOUND")
	ed.POST(base+"/"+pg.ID+"/publish", M{}).ExpectErr(409, "INVALID_STATE")
	ed.POST(base+"/"+pg.ID+"/submit", nil).Expect(200)
	ed.PUT(base+"/"+pg.ID, in).ExpectErr(409, "INVALID_STATE") // in_review tak bisa disunting
	// penyunting tak boleh menerbitkan sendiri
	ed.POST(base+"/"+pg.ID+"/publish", M{}).ExpectErr(403, "FORBIDDEN")
	rev.POST(base+"/"+pg.ID+"/publish", M{}).Expect(200)

	var pub struct {
		BodyHTML string `json:"body_html"`
		Title    string
	}
	e.Anon().GET(api + "/content/help/cara-refund").Expect(200).Data(&pub)
	for _, bad := range []string{"<script", "onerror", "javascript:"} {
		if strings.Contains(strings.ToLower(pub.BodyHTML), bad) {
			t.Fatalf("HTML harus disanitasi, ditemukan %q: %s", bad, pub.BodyHTML)
		}
	}
	if !strings.Contains(pub.BodyHTML, "<strong>tebal</strong>") || !strings.Contains(pub.BodyHTML, "<h2") {
		t.Fatalf("markdown dirender: %s", pub.BodyHTML)
	}
	// locale lain tidak diam-diam jatuh ke bahasa lain
	e.Anon().GET(api+"/content/help/cara-refund?locale=en").ExpectErr(404, "NOT_FOUND")
	// versi baru: yang lama tetap terbit sampai versi baru diterbitkan, lalu diarsipkan
	in["title"] = "Cara refund (diperbarui)"
	var v2 struct{ ID string }
	ed.POST(base, in).Expect(201).Data(&v2)
	e.Anon().GET(api + "/content/help/cara-refund").Expect(200).Data(&pub)
	if pub.Title != "Cara mengajukan refund" {
		t.Fatal("selama draf, versi lama tetap tampil")
	}
	ed.POST(base+"/"+v2.ID+"/submit", nil).Expect(200)
	rev.POST(base+"/"+v2.ID+"/publish", M{}).Expect(200)
	e.Anon().GET(api + "/content/help/cara-refund").Expect(200).Data(&pub)
	if pub.Title != "Cara refund (diperbarui)" {
		t.Fatal("versi baru tampil")
	}
	var vs []map[string]any
	ed.GET(api + "/admin/cms/versions?kind=help&slug=cara-refund").Expect(200).Data(&vs)
	if len(vs) != 2 || vs[1]["status"] != "archived" {
		t.Fatalf("riwayat versi: %v", vs)
	}
	// pencarian bantuan
	var sr []map[string]any
	e.Anon().GET(api + "/content/help/search?q=refund").Expect(200).Data(&sr)
	if len(sr) != 1 {
		t.Fatalf("cari: %v", sr)
	}
	e.Anon().GET(api + "/content/help/search?q=x").Expect(200)
}

func TestCMSScheduledPublish(t *testing.T) {
	e := NewEnv(t)
	ed := e.UserWithRole("editor", "Ed")
	rev := e.UserWithRole("admin", "Admin CMS")
	var pg struct{ ID string }
	ed.POST(api+"/admin/cms/pages", M{"kind": "article", "slug": "kabar-baru", "locale": "id", "title": "Kabar Baru", "body": "isi artikel"}).Expect(201).Data(&pg)
	ed.POST(api+"/admin/cms/pages/"+pg.ID+"/submit", nil).Expect(200)
	rev.POST(api+"/admin/cms/pages/"+pg.ID+"/publish", M{"publish_at": time.Now().Add(-time.Hour)}).ExpectErr(400, "VALIDATION_FAILED")
	rev.POST(api+"/admin/cms/pages/"+pg.ID+"/publish", M{"publish_at": e.Clock.Now().Add(2 * time.Hour)}).Expect(200)
	e.Anon().GET(api+"/content/article/kabar-baru").ExpectErr(404, "NOT_FOUND")
	e.Clock.Advance(3 * time.Hour)
	if n, err := e.Svc.CMS.RunScheduled(t.Context()); err != nil || n != 1 {
		t.Fatalf("terjadwal: %d %v", n, err)
	}
	e.Anon().GET(api + "/content/article/kabar-baru").Expect(200)
}

func TestBannersCollectionsPublicWindow(t *testing.T) {
	e := NewEnv(t)
	ed := e.UserWithRole("editor", "Ed Banner")
	_, ev1, _, _ := e.publishedGA(100000, 10)
	_, ev2, _, _ := e.publishedGA(100000, 10)
	_, evDraft, _, _ := e.publishedGA(100000, 10)
	e.Exec("UPDATE events SET status='draft' WHERE id=$1", evDraft)
	asset := newID()
	e.Exec("INSERT INTO media_assets (id, owner_user_id, kind, object_key, content_type) VALUES ($1,$2,'banner','k','image/jpeg')", asset, ed.UserID)
	b := api + "/admin/cms/banners"
	ed.POST(b, M{"title": "Promo", "image_key": asset, "link": "javascript:alert(1)", "starts_at": e.Clock.Now().Add(-time.Hour)}).ExpectErr(400, "VALIDATION_FAILED")
	ed.POST(b, M{"title": "Promo", "image_key": newID(), "starts_at": e.Clock.Now().Add(-time.Hour)}).ExpectErr(400, "VALIDATION_FAILED")
	ed.POST(b, M{"title": "Promo Aktif", "image_key": asset, "link": "/events", "starts_at": e.Clock.Now().Add(-time.Hour), "ends_at": e.Clock.Now().Add(time.Hour)}).Expect(201)
	ed.POST(b, M{"title": "Promo Nanti", "image_key": asset, "starts_at": e.Clock.Now().Add(5 * time.Hour)}).Expect(201)
	var act []map[string]any
	e.Anon().GET(api + "/content/banners").Expect(200).Data(&act)
	if len(act) != 1 || act[0]["title"] != "Promo Aktif" {
		t.Fatalf("hanya banner dalam jendela: %v", act)
	}
	// koleksi: urutan kurasi, event draft tak muncul
	ed.PUT(api+"/admin/cms/collections", M{"slug": "minggu-ini", "title": "Minggu ini", "event_ids": []string{ev2, evDraft, ev1}}).Expect(200)
	var col struct {
		Events []struct{ ID string } `json:"events"`
	}
	e.Anon().GET(api + "/content/collections/minggu-ini").Expect(200).Data(&col)
	if len(col.Events) != 2 || col.Events[0].ID != ev2 || col.Events[1].ID != ev1 {
		t.Fatalf("urutan kurasi & hanya terbit: %+v", col.Events)
	}
	ed.PUT(api+"/admin/cms/collections", M{"slug": "Bukan Slug", "title": "x", "event_ids": []string{ev1}}).ExpectErr(400, "VALIDATION_FAILED")
}
