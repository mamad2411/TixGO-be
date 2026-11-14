package apitest

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type hook struct {
	mu    sync.Mutex
	calls []struct {
		sig, event string
		body       []byte
	}
	fail int // jumlah panggilan awal yang dijawab 500
}

func (h *hook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, struct {
		sig, event string
		body       []byte
	}{r.Header.Get("X-TixGo-Signature"), r.Header.Get("X-TixGo-Event"), b})
	if len(h.calls) <= h.fail {
		w.WriteHeader(500)
		return
	}
	w.WriteHeader(200)
}

func (h *hook) n() int { h.mu.Lock(); defer h.mu.Unlock(); return len(h.calls) }

func TestOrganizerWebhookSignedAndRetried(t *testing.T) {
	e := NewEnv(t)
	o, _, slug, tier := e.publishedGA(100000, 10)
	rec := &hook{fail: 1}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	var wh struct {
		ID     string
		Secret string
	}
	o.C.POST(api+"/organizer/integrations/webhooks", M{"url": srv.URL, "events": []string{"order.paid", "ticket.issued"}}).Expect(201).Data(&wh)
	if !strings.HasPrefix(wh.Secret, "whsec_") {
		t.Fatal("secret ditampilkan sekali saat dibuat")
	}
	var list []map[string]any
	o.C.GET(api + "/organizer/integrations/webhooks").Expect(200).Data(&list)
	if list[0]["secret"] != nil {
		t.Fatal("secret tak boleh muncul lagi")
	}
	u := e.Verified("Pembeli")
	e.buyPaid(u, slug, tier, 1, "gopay")
	// panggilan pertama gagal (500) → dijadwalkan ulang (1 menit); jalankan pengiriman ulang
	if rec.n() < 1 {
		t.Fatalf("seharusnya ada pengiriman: %d", rec.n())
	}
	e.Clock.Advance(2 * time.Minute)
	e.Jobs.RunDue(t.Context())
	e.Clock.Advance(10 * time.Minute)
	e.Jobs.RunDue(t.Context())
	if rec.n() < 3 {
		t.Fatalf("order.paid (retry) + ticket.issued: %d panggilan", rec.n())
	}
	// verifikasi tanda tangan HMAC pada panggilan sukses
	rec.mu.Lock()
	defer rec.mu.Unlock()
	okSig := false
	for _, c := range rec.calls {
		parts := strings.Split(c.sig, ",")
		ts := strings.TrimPrefix(parts[0], "t=")
		m := hmac.New(sha256.New, []byte(wh.Secret))
		m.Write([]byte(ts + "."))
		m.Write(c.body)
		if strings.TrimPrefix(parts[1], "v1=") == hex.EncodeToString(m.Sum(nil)) {
			okSig = true
		}
		if _, err := strconv.ParseInt(ts, 10, 64); err != nil {
			t.Fatal("timestamp")
		}
		if strings.Contains(string(c.body), u.Email) {
			t.Fatal("payload tidak boleh memuat PII pembeli")
		}
	}
	if !okSig {
		t.Fatal("signature HMAC harus valid")
	}
	if e.QueryInt("SELECT count(*) FROM webhook_deliveries WHERE delivered_at IS NOT NULL") < 2 {
		t.Fatal("log pengiriman: terkirim")
	}
	if e.QueryInt("SELECT max(attempts) FROM webhook_deliveries") < 2 {
		t.Fatal("percobaan ulang tercatat")
	}
}

func TestWebhookSSRFAndValidation(t *testing.T) {
	e := NewEnv(t)
	o, _, _, _ := e.publishedGA(100000, 10)
	e.Cfg.WebhookAllowPrivate = false
	defer func() { e.Cfg.WebhookAllowPrivate = true }()
	base := api + "/organizer/integrations/webhooks"
	for _, bad := range []string{"http://example.com/h", "https://127.0.0.1/h", "https://169.254.169.254/latest", "https://localhost/h", "https://10.0.0.5/h", "https://user:pw@example.com/h", "ftp://example.com"} {
		o.C.POST(base, M{"url": bad, "events": []string{"order.paid"}}).ExpectErr(400, "VALIDATION_FAILED")
	}
	o.C.POST(base, M{"url": "https://example.com/h", "events": []string{"salah.event"}}).ExpectErr(400, "VALIDATION_FAILED")
	var wh struct{ ID string }
	o.C.POST(base, M{"url": "https://example.com/h", "events": []string{"order.paid"}}).Expect(201).Data(&wh)
	// DNS yang mengarah ke alamat internal diblokir saat dial (endpoint disimpan lalu host dialihkan ke loopback)
	e.Exec("UPDATE webhook_endpoints SET url='https://127.0.0.1:9/h' WHERE id=$1", wh.ID)
	var res struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	o.C.POST(base+"/"+wh.ID+"/ping", nil).Expect(200).Data(&res)
	if res.OK || !strings.Contains(res.Error, "internal") {
		t.Fatalf("dial ke alamat internal harus diblokir: %+v", res)
	}
	// anggota biasa (bukan owner) tidak boleh
	o2 := e.NewOrganizer(true)
	o2.C.DELETE(base+"/"+wh.ID).ExpectErr(404, "NOT_FOUND")
}

func TestAPIKeyScopesRateLimitAndRotation(t *testing.T) {
	e := NewEnv(t)
	o, evID, slug, tier := e.publishedGA(100000, 10)
	u := e.Verified("Pembeli Key")
	e.buyPaid(u, slug, tier, 2, "gopay")
	base := api + "/organizer/integrations/api-keys"
	o.C.POST(base, M{"name": "BI", "scopes": []string{"bogus"}}).ExpectErr(400, "VALIDATION_FAILED")
	var k1 struct{ ID, Key string }
	o.C.POST(base, M{"name": "Dashboard BI", "scopes": []string{"sales:read"}}).Expect(201).Data(&k1)
	if !strings.HasPrefix(k1.Key, "tgk_") {
		t.Fatal("format key")
	}
	var list []map[string]any
	o.C.GET(base).Expect(200).Data(&list)
	if list[0]["key"] != nil {
		t.Fatal("key mentah tak boleh muncul lagi")
	}
	pk := e.Anon()
	pk.GET(api+"/partner/v1/events").ExpectErr(401, "UNAUTHENTICATED")
	h := []string{"X-API-Key", k1.Key}
	var rep struct{ Orders int }
	pk.GET(api+"/partner/v1/events/"+evID+"/sales", h...).Expect(200).Data(&rep)
	if rep.Orders != 1 {
		t.Fatalf("sales: %+v", rep)
	}
	pk.GET(api+"/partner/v1/events/"+evID+"/attendees", h...).ExpectErr(403, "FORBIDDEN") // scope kurang
	// event organizer lain tidak terbaca
	o2 := e.NewOrganizer(true)
	var k2 struct{ Key string }
	o2.C.POST(base, M{"name": "Lain", "scopes": []string{"sales:read", "attendees:read"}}).Expect(201).Data(&k2)
	pk.GET(api+"/partner/v1/events/"+evID+"/sales", "X-API-Key", k2.Key).ExpectErr(404, "NOT_FOUND")
	// rotasi: key lama mati, baru hidup
	var k3 struct{ Key string }
	o.C.POST(base+"/"+k1.ID+"/rotate", nil).Expect(201).Data(&k3)
	pk.GET(api+"/partner/v1/events", h...).ExpectErr(401, "UNAUTHENTICATED")
	pk.GET(api+"/partner/v1/events", "X-API-Key", k3.Key).Expect(200)
	if e.QueryInt("SELECT count(*) FROM audit_logs WHERE action IN ('apikey.create','apikey.revoke')") < 3 {
		t.Fatal("teraudit")
	}
	// rate limit 120/menit per key
	e.EnableRateLimit()
	limited := false
	for i := 0; i < 130; i++ {
		if pk.GET(api+"/partner/v1/events", "X-API-Key", k3.Key).Code == 429 {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("rate limit per key")
	}
}

func TestWidgetAllowlistAndPixelsRequireMarketingConsent(t *testing.T) {
	e := NewEnv(t)
	o, evID, slug, _ := e.publishedGA(100000, 10)
	dom := api + "/organizer/integrations/widget-domains"
	o.C.POST(dom, M{"domain": "bukan domain"}).ExpectErr(400, "VALIDATION_FAILED")
	o.C.POST(dom, M{"domain": "Tiket.Contoh.com"}).Expect(204)
	r := e.Anon().GET(api+"/widget/events/"+slug, "Origin", "https://tiket.contoh.com").Expect(200)
	if r.Header.Get("Access-Control-Allow-Origin") != "https://tiket.contoh.com" || !strings.Contains(r.Header.Get("Content-Security-Policy"), "frame-ancestors https://tiket.contoh.com") {
		t.Fatalf("header widget: %v", r.Header)
	}
	e.Anon().GET(api+"/widget/events/"+slug, "Origin", "https://jahat.example").ExpectErr(403, "FORBIDDEN")
	e.Anon().GET(api+"/widget/events/"+slug).ExpectErr(403, "FORBIDDEN")

	// piksel
	o.C.PUT(api+"/organizer/integrations/events/"+evID+"/tracking", M{"ga_id": "G-ABC12345", "meta_pixel_id": "123456789"}).Expect(200)
	o.C.PUT(api+"/organizer/integrations/events/"+evID+"/tracking", M{"ga_id": "bad id!"}).ExpectErr(400, "VALIDATION_FAILED")
	var px map[string]string
	e.Anon().GET(api+"/events/"+slug+"/pixels", "X-Cookie-Subject", "anon-subject-1").Expect(200).Data(&px)
	if len(px) != 0 {
		t.Fatalf("tanpa persetujuan tidak ada piksel: %v", px)
	}
	e.Anon().PUT(api+"/cookie-consent", M{"analytics": true, "marketing": true}, "X-Cookie-Subject", "anon-subject-1").Expect(200)
	e.Anon().GET(api+"/events/"+slug+"/pixels", "X-Cookie-Subject", "anon-subject-1").Expect(200).Data(&px)
	if px["ga_id"] != "G-ABC12345" {
		t.Fatalf("dengan persetujuan marketing: %v", px)
	}
}
