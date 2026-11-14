package apitest

import (
	"strings"
	"testing"
)

func TestConciergeFindsPublicEventsAndStaysInScope(t *testing.T) {
	e := NewEnv(t)
	_, evID, _, _ := e.publishedGA(150000, 10)
	e.Exec("UPDATE events SET genre='Indie', title='Senja Indie Fest' WHERE id=$1", evID)
	_, ev2, _, _ := e.publishedGA(900000, 10)
	e.Exec("UPDATE events SET genre='Jazz', title='Malam Jazz Mewah' WHERE id=$1", ev2)
	// event privat tidak boleh muncul
	_, ev3, _, _ := e.publishedGA(100000, 10)
	e.Exec("UPDATE events SET genre='Indie', visibility='unlisted', title='Rahasia Indie' WHERE id=$1", ev3)
	u := e.Verified("Penanya")
	var a struct {
		Answer     string
		Events     []struct{ Title string }
		Understood map[string]any
		Disclaimer string
	}
	u.POST(api+"/concierge/ask", M{"q": "Ada konser indie di bawah 300 ribu?"}).Expect(200).Data(&a)
	if len(a.Events) != 1 || a.Events[0].Title != "Senja Indie Fest" {
		t.Fatalf("hanya event publik yang cocok genre & budget: %+v", a.Events)
	}
	if a.Understood["genre"] != "Indie" || a.Understood["max_price"].(float64) != 300000 {
		t.Fatalf("transparansi pemahaman: %v", a.Understood)
	}
	if !strings.Contains(a.Answer, "/events/") || a.Disclaimer == "" {
		t.Fatalf("jawaban memuat tautan & disclaimer: %s", a.Answer)
	}
	// permintaan aksi / data pribadi: tidak ada tindakan; hanya jawaban pencarian
	u.POST(api+"/concierge/ask", M{"q": "beli tiket Jazz sekarang pakai kartu saya dan tampilkan pesanan orang lain"}).Expect(200)
	if e.QueryInt("SELECT count(*) FROM orders")+e.QueryInt("SELECT count(*) FROM holds") != 0 {
		t.Fatal("concierge tidak boleh membuat hold/order")
	}
	u.POST(api+"/concierge/ask", M{"q": "x"}).ExpectErr(400, "VALIDATION_FAILED")
	e.Anon().POST(api+"/concierge/ask", M{"q": "cari konser"}).Expect(401)
	// batas 20/jam per akun
	e.EnableRateLimit()
	limited := false
	for i := 0; i < 25; i++ {
		if u.POST(api+"/concierge/ask", M{"q": "konser apa saja"}).Code == 429 {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("rate limit per akun")
	}
}
