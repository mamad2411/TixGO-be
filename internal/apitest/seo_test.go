package apitest

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

var ldRe = regexp.MustCompile(`(?s)<script type="application/ld\+json">(.*?)</script>`)

func TestEventPageHasMetaAndJSONLDWithoutJS(t *testing.T) { // Q11, M32 AC1/AC4/AC6
	e := NewEnv(t)
	o, evID, slug, _ := e.publishedGA(150000, 10)
	e.Exec("UPDATE events SET title=$2, description='Konser <b>spesial</b> malam hari' WHERE id=$1", evID, `Festival "Senja" </script><img src=x onerror=alert(1)>`)
	_ = o
	r := e.Anon().GET("/events/" + slug).Expect(200)
	body := string(r.Body)
	if !strings.Contains(r.Header.Get("Content-Type"), "text/html") {
		t.Fatal("harus HTML")
	}
	for _, want := range []string{`rel="canonical"`, `hreflang="id"`, `property="og:title"`, `name="twitter:card"`, `content="index,follow"`, "<noscript>"} {
		if !strings.Contains(body, want) {
			t.Fatalf("meta %q hilang", want)
		}
	}
	if strings.Contains(body, "<img src=x") || strings.Contains(body, "</script><img") {
		t.Fatal("judul harus di-escape (XSS)")
	}
	m := ldRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("JSON-LD tidak ada")
	}
	var ld map[string]any
	if err := json.Unmarshal([]byte(m[1]), &ld); err != nil {
		t.Fatalf("JSON-LD tidak valid: %v", err)
	}
	for _, k := range []string{"name", "startDate", "location", "eventStatus", "offers"} {
		if ld[k] == nil {
			t.Fatalf("JSON-LD wajib %q", k)
		}
	}
	loc := ld["location"].(map[string]any)
	if loc["address"] == nil {
		t.Fatal("location.address wajib")
	}
	if ld["eventStatus"] != "https://schema.org/EventScheduled" {
		t.Fatal(ld["eventStatus"])
	}
	offer := ld["offers"].([]any)[0].(map[string]any)
	if offer["priceCurrency"] != "IDR" || offer["availability"] == nil {
		t.Fatalf("offer: %v", offer)
	}
}

func TestEventPageStatusMappingNoindexAndNotFound(t *testing.T) {
	e := NewEnv(t)
	_, evID, slug, _ := e.publishedGA(100000, 10)
	ld := func() map[string]any {
		body := string(e.Anon().GET("/events/" + slug).Expect(200).Body)
		m := ldRe.FindStringSubmatch(body)
		var out map[string]any
		_ = json.Unmarshal([]byte(m[1]), &out)
		return out
	}
	e.Exec("UPDATE events SET schedule_state='postponed' WHERE id=$1", evID)
	if ld()["eventStatus"] != "https://schema.org/EventPostponed" {
		t.Fatal("postponed")
	}
	e.Exec("UPDATE events SET schedule_state='rescheduled', previous_starts_at=starts_at - interval '10 days' WHERE id=$1", evID)
	l := ld()
	if l["eventStatus"] != "https://schema.org/EventRescheduled" || l["previousStartDate"] == nil {
		t.Fatalf("rescheduled: %v", l)
	}
	e.Exec("UPDATE events SET schedule_state='cancelled', status='cancelled' WHERE id=$1", evID)
	if ld()["eventStatus"] != "https://schema.org/EventCancelled" {
		t.Fatal("cancelled")
	}
	// unlisted: noindex; draft: 404 + noindex
	e.Exec("UPDATE events SET schedule_state='scheduled', status='published', visibility='unlisted' WHERE id=$1", evID)
	if !strings.Contains(string(e.Anon().GET("/events/"+slug).Expect(200).Body), `content="noindex,nofollow"`) {
		t.Fatal("unlisted harus noindex")
	}
	e.Exec("UPDATE events SET status='draft' WHERE id=$1", evID)
	r := e.Anon().GET("/events/" + slug).Expect(404)
	if !strings.Contains(string(r.Body), "noindex") {
		t.Fatal("404 harus noindex")
	}
}

func TestSitemapAndRobots(t *testing.T) {
	e := NewEnv(t)
	_, _, slugPub, _ := e.publishedGA(100000, 10)
	_, evUn, slugUn, _ := e.publishedGA(100000, 10)
	e.Exec("UPDATE events SET visibility='unlisted' WHERE id=$1", evUn)
	sm := string(e.Anon().GET("/sitemap.xml").Expect(200).Body)
	if !strings.Contains(sm, "/events/"+slugPub) || strings.Contains(sm, slugUn) {
		t.Fatalf("sitemap memuat event publik saja: %s", sm)
	}
	rb := string(e.Anon().GET("/robots.txt").Expect(200).Body)
	if !strings.Contains(rb, "Disallow: /api/") || !strings.Contains(rb, "Sitemap: ") {
		t.Fatal(rb)
	}
}
