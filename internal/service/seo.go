package service

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"os"
	"strings"
	"time"

	"tixgo/backend/internal/apperr"
)

// SEOService = meta + JSON-LD yang tersedia TANPA JS untuk crawler (keputusan Q11), sitemap, robots (docs/20 M32).
type SEOService struct{ s *Services }

func newSEOService(s *Services) *SEOService { return &SEOService{s: s} }

const defaultShell = `<!doctype html><html lang="id"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">` +
	`<!--tixgo:head--></head><body><div id="app"></div><!--tixgo:noscript--></body></html>`

func (o *SEOService) shell() string {
	if p := o.s.Cfg.SEOIndexHTML; p != "" {
		if b, err := os.ReadFile(p); err == nil && strings.Contains(string(b), "</head>") {
			return strings.Replace(string(b), "</head>", "<!--tixgo:head--></head>", 1)
		}
		o.s.Log.Warn("SEO_INDEX_HTML tidak terbaca/valid; memakai shell bawaan", "path", p)
	}
	return defaultShell
}

// eventStatus memetakan status internal → schema.org (AC2).
func eventStatus(ev *PublicEvent) string {
	switch {
	case ev.ScheduleState == "cancelled" || ev.SaleState == SaleCancelled:
		return "https://schema.org/EventCancelled"
	case ev.ScheduleState == "postponed":
		return "https://schema.org/EventPostponed"
	case ev.ScheduleState == "rescheduled":
		return "https://schema.org/EventRescheduled"
	}
	return "https://schema.org/EventScheduled"
}

func availability(ev *PublicEvent, t PublicTier) string {
	switch {
	case ev.SaleState == SaleSoldOut || t.Status == SaleSoldOut:
		return "https://schema.org/SoldOut"
	case ev.NextPhaseAt != nil && ev.SaleState != SaleOnsale && ev.SaleState != SaleFullBooked:
		return "https://schema.org/PreOrder"
	}
	return "https://schema.org/InStock"
}

func (o *SEOService) abs(path string) string {
	if path == "" || strings.HasPrefix(path, "http") {
		return path
	}
	return o.s.Cfg.SEOSiteURL + path
}

// JSONLD membangun objek Event Google. Field wajib: name, startDate, location(+address).
func (o *SEOService) JSONLD(ev *PublicEvent) map[string]any {
	loc := map[string]any{"@type": "Place", "name": ""}
	if ev.Venue != nil {
		loc["name"] = ev.Venue.Name
		addr := map[string]any{"@type": "PostalAddress", "addressLocality": ev.Venue.City, "addressCountry": "ID"}
		if ev.Venue.Address != nil {
			addr["streetAddress"] = *ev.Venue.Address
		}
		loc["address"] = addr
		if ev.Venue.Lat != nil && ev.Venue.Lng != nil {
			loc["geo"] = map[string]any{"@type": "GeoCoordinates", "latitude": *ev.Venue.Lat, "longitude": *ev.Venue.Lng}
		}
	}
	ld := map[string]any{
		"@context": "https://schema.org", "@type": "Event", "name": ev.Title,
		"startDate": ev.StartsAt.Format(time.RFC3339), "eventStatus": eventStatus(ev),
		"eventAttendanceMode": "https://schema.org/OfflineEventAttendanceMode", "location": loc,
		"url": o.s.Cfg.SEOSiteURL + "/events/" + ev.Slug,
	}
	if ev.EndsAt != nil {
		ld["endDate"] = ev.EndsAt.Format(time.RFC3339)
	}
	if ev.PreviousStartsAt != nil {
		ld["previousStartDate"] = ev.PreviousStartsAt.Format(time.RFC3339)
	}
	if ev.Description != nil && *ev.Description != "" {
		ld["description"] = *ev.Description
	}
	if ev.PosterURL != "" {
		ld["image"] = []string{o.abs(ev.PosterURL)}
	}
	var perf []map[string]any
	for _, a := range ev.Artists {
		perf = append(perf, map[string]any{"@type": "PerformingGroup", "name": a.Name})
	}
	if len(perf) > 0 {
		ld["performer"] = perf
	}
	var offers []map[string]any
	for _, t := range ev.Tiers {
		of := map[string]any{"@type": "Offer", "name": t.Name, "price": t.FinalPrice, "priceCurrency": "IDR", "availability": availability(ev, t), "url": ld["url"]}
		offers = append(offers, of)
	}
	if len(offers) > 0 {
		ld["offers"] = offers
	}
	return ld
}

func esc(s string) string { return html.EscapeString(s) }

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// EventPage mengembalikan HTML lengkap untuk /events/:slug. status 404 bila event tak publik (halaman draft/privat tidak diindeks).
func (o *SEOService) EventPage(ctx context.Context, slug, password, ip string) (string, int, error) {
	ev, err := o.s.Catalog.Detail(ctx, slug, password, ip)
	if err != nil {
		if ae, ok := err.(*apperr.Error); ok && ae.Status == 404 {
			return o.notFound(), 404, nil
		}
		return "", 0, err
	}
	url := o.s.Cfg.SEOSiteURL + "/events/" + ev.Slug
	desc := fmt.Sprintf("%s — %s, %s. %s", ev.Title, ev.VenueName, ev.City, ev.StartsAt.Format("2 Jan 2006"))
	if ev.Description != nil && *ev.Description != "" {
		desc = trunc(strings.Join(strings.Fields(*ev.Description), " "), 160)
	}
	robots := "index,follow"
	if ev.Visibility != "public" {
		robots = "noindex,nofollow"
	}
	ldJSON, _ := json.Marshal(o.JSONLD(ev)) // json.Marshal meng-escape <,>,& → aman di dalam <script>
	var b strings.Builder
	fmt.Fprintf(&b, "<title>%s | TixGo</title>", esc(ev.Title))
	fmt.Fprintf(&b, `<meta name="description" content="%s"><meta name="robots" content="%s">`, esc(desc), robots)
	fmt.Fprintf(&b, `<link rel="canonical" href="%s"><link rel="alternate" hreflang="id" href="%s"><link rel="alternate" hreflang="en" href="%s?lang=en"><link rel="alternate" hreflang="x-default" href="%s">`, esc(url), esc(url), esc(url), esc(url))
	fmt.Fprintf(&b, `<meta property="og:type" content="website"><meta property="og:site_name" content="TixGo"><meta property="og:title" content="%s"><meta property="og:description" content="%s"><meta property="og:url" content="%s">`, esc(ev.Title), esc(desc), esc(url))
	if ev.PosterURL != "" {
		img := o.abs(ev.PosterURL)
		fmt.Fprintf(&b, `<meta property="og:image" content="%s"><meta name="twitter:image" content="%s">`, esc(img), esc(img))
	}
	fmt.Fprintf(&b, `<meta name="twitter:card" content="summary_large_image"><meta name="twitter:title" content="%s"><meta name="twitter:description" content="%s">`, esc(ev.Title), esc(desc))
	fmt.Fprintf(&b, `<script type="application/ld+json">%s</script>`, ldJSON)
	page := strings.Replace(o.shell(), "<!--tixgo:head-->", b.String(), 1)
	nos := fmt.Sprintf(`<noscript><h1>%s</h1><p>%s</p><p>%s, %s — %s</p></noscript>`, esc(ev.Title), esc(desc), esc(ev.VenueName), esc(ev.City), ev.StartsAt.Format("2 January 2006 15:04"))
	page = strings.Replace(page, "<!--tixgo:noscript-->", nos, 1)
	if !strings.Contains(page, "<!--tixgo:noscript-->") && !strings.Contains(page, "<noscript><h1>") {
		page = strings.Replace(page, "</body>", nos+"</body>", 1)
	}
	return page, 200, nil
}

func (o *SEOService) notFound() string {
	head := `<title>Halaman tidak ditemukan | TixGo</title><meta name="robots" content="noindex">`
	return strings.Replace(strings.Replace(o.shell(), "<!--tixgo:head-->", head, 1), "<!--tixgo:noscript-->", "", 1)
}

type urlEntry struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod,omitempty"`
}

type urlSet struct {
	XMLName xml.Name   `xml:"urlset"`
	NS      string     `xml:"xmlns,attr"`
	URLs    []urlEntry `xml:"url"`
}

func (o *SEOService) Sitemap(ctx context.Context) ([]byte, error) {
	base := o.s.Cfg.SEOSiteURL
	set := urlSet{NS: "http://www.sitemaps.org/schemas/sitemap/0.9"}
	for _, p := range []string{"/", "/events", "/help", "/status"} {
		set.URLs = append(set.URLs, urlEntry{Loc: base + p})
	}
	evs, err := o.s.Q.SitemapEvents(ctx)
	if err != nil {
		return nil, err
	}
	for _, e := range evs {
		set.URLs = append(set.URLs, urlEntry{Loc: base + "/events/" + e.Slug, LastMod: e.UpdatedAt.UTC().Format("2006-01-02")})
	}
	ars, err := o.s.Q.SitemapArtists(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range ars {
		set.URLs = append(set.URLs, urlEntry{Loc: base + "/artists/" + a.Slug, LastMod: a.UpdatedAt.UTC().Format("2006-01-02")})
	}
	out, err := xml.MarshalIndent(set, "", "  ")
	return append([]byte(xml.Header), out...), err
}

func (o *SEOService) Robots() string {
	return "User-agent: *\nAllow: /\nDisallow: /api/\nDisallow: /account\nDisallow: /organizer\nDisallow: /admin\nDisallow: /gate\nDisallow: /checkout\n\nSitemap: " + o.s.Cfg.SEOSiteURL + "/sitemap.xml\n"
}
