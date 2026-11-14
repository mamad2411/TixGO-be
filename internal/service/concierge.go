package service

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"tixgo/backend/internal/apperr"
)

// Concierge = asisten penemu event berbasis aturan (docs/10 §10, M18). Hanya membaca data event PUBLIK + artikel bantuan terbit.
// Tidak punya akses data pribadi/pesanan, tidak mengeksekusi aksi apa pun (beli/refund), tanpa LLM (provider belum diputuskan).
type Concierge struct{ s *Services }

func newConcierge(s *Services) *Concierge { return &Concierge{s: s} }

type ConciergeAnswer struct {
	Answer     string         `json:"answer"`
	Events     []EventCard    `json:"events"`
	Help       []HelpHit      `json:"help"`
	Understood map[string]any `json:"understood"` // transparansi: apa yang dipahami dari pertanyaan
	Disclaimer string         `json:"disclaimer"`
}

type HelpHit struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

var (
	budgetRe = regexp.MustCompile(`(?:di ?bawah|kurang dari|maks(?:imal)?|<=?|budget|sekitar|paling mahal)\s*(?:rp\.?\s*)?(\d+(?:[.,]\d+)?)\s*(rb|ribu|k|jt|juta)?`)
	helpKeys = map[string][]string{
		"refund":   {"refund", "batal", "pengembalian", "kembalikan"},
		"bayar":    {"bayar", "pembayaran", "qris", "gopay", "va ", "virtual account"},
		"transfer": {"transfer", "kirim tiket", "teman", "klaim"},
		"qr":       {"qr", "masuk", "gate", "scan", "offline"},
		"antrean":  {"antre", "antrean", "waiting room", "war"},
		"tiket":    {"tiket hilang", "tidak terbit", "belum terbit", "invoice"},
	}
)

// parseBudget mengembalikan rupiah (0 = tidak ada).
func parseBudget(q string) int64 {
	m := budgetRe.FindStringSubmatch(q)
	if m == nil {
		return 0
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", "."), 64)
	if err != nil || f <= 0 {
		return 0
	}
	switch m[2] {
	case "rb", "ribu", "k":
		f *= 1_000
	case "jt", "juta":
		f *= 1_000_000
	default:
		if f < 1000 { // "300" tanpa satuan → ribu
			f *= 1_000
		}
	}
	return int64(f)
}

func (c *Concierge) dateRange(q string) (*time.Time, *time.Time, string) {
	now := c.s.now()
	day := func(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()) }
	switch {
	case strings.Contains(q, "hari ini"):
		f, t := day(now), day(now).AddDate(0, 0, 1)
		return &f, &t, "hari ini"
	case strings.Contains(q, "besok"):
		f, t := day(now).AddDate(0, 0, 1), day(now).AddDate(0, 0, 2)
		return &f, &t, "besok"
	case strings.Contains(q, "minggu ini") || strings.Contains(q, "weekend") || strings.Contains(q, "akhir pekan"):
		f, t := day(now), day(now).AddDate(0, 0, 7)
		return &f, &t, "7 hari ke depan"
	case strings.Contains(q, "bulan depan"):
		first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).AddDate(0, 1, 0)
		t := first.AddDate(0, 1, 0)
		return &first, &t, "bulan depan"
	case strings.Contains(q, "bulan ini"):
		f := day(now)
		t := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).AddDate(0, 1, 0)
		return &f, &t, "bulan ini"
	}
	return nil, nil, ""
}

func (c *Concierge) Ask(ctx context.Context, userID, question string) (*ConciergeAnswer, error) {
	q := strings.ToLower(strings.TrimSpace(question))
	if len([]rune(q)) < 3 || len([]rune(q)) > 300 {
		return nil, apperr.Validation("pertanyaan 3–300 karakter").With("field", "q")
	}
	if ok, ra, err := c.s.Limiter.Allow(ctx, "concierge:"+userID, 20, time.Hour); err == nil && !ok {
		return nil, apperr.TooMany("batas pertanyaan per jam tercapai").With("retry_after", int(ra.Seconds())+1)
	}
	fo, err := c.s.Catalog.Filters(ctx)
	if err != nil {
		return nil, err
	}
	und := map[string]any{}
	f := EventFilter{Sort: "date", PerPage: 5, Status: "upcoming"}
	f.Status = ""
	for _, city := range fo.Cities {
		if city != "" && strings.Contains(q, strings.ToLower(city)) {
			f.City = city
			und["city"] = city
			break
		}
	}
	for _, g := range fo.Genres {
		if g != "" && strings.Contains(q, strings.ToLower(g)) {
			f.Genre = g
			und["genre"] = g
			break
		}
	}
	if b := parseBudget(q); b > 0 {
		f.PriceMax = &b
		und["max_price"] = b
	} else if strings.Contains(q, "murah") || strings.Contains(q, "hemat") {
		b := int64(200_000)
		f.PriceMax = &b
		und["max_price"] = b
	}
	if from, to, label := c.dateRange(q); from != nil {
		f.DateFrom, f.DateTo = from, to
		und["when"] = label
	}
	if len(und) == 0 { // tidak ada penyaring dikenali → pakai kata kunci bebas (nama artis/venue/judul)
		f.Q = q
		for _, stop := range []string{"cari", "konser", "ada", "apa", "yang", "dong", "tolong", "mau", "nonton", "tiket", "event"} {
			f.Q = strings.ReplaceAll(" "+f.Q+" ", " "+stop+" ", " ")
		}
		f.Q = strings.TrimSpace(strings.Join(strings.Fields(f.Q), " "))
		if len([]rune(f.Q)) >= 2 {
			und["keyword"] = f.Q
		} else {
			f.Q = ""
		}
	}
	cards, _, err := c.s.Catalog.List(ctx, f)
	if err != nil {
		return nil, err
	}
	ans := &ConciergeAnswer{Events: cards, Help: []HelpHit{}, Understood: und, Disclaimer: "Saya hanya membantu mencari event dan menjawab pertanyaan umum. Pembelian dan refund tetap kamu lakukan sendiri lewat halaman resmi."}
	if cards == nil {
		ans.Events = []EventCard{}
	}
	// bantuan: kata kunci → artikel terbit
	for topic, keys := range helpKeys {
		for _, k := range keys {
			if strings.Contains(q+" ", k) {
				hits, _ := c.s.CMS.SearchHelp(ctx, "id", topic)
				for _, h := range hits {
					if len(ans.Help) >= 2 {
						break
					}
					slug, _ := h["slug"].(string)
					title, _ := h["title"].(string)
					ans.Help = append(ans.Help, HelpHit{Slug: slug, Title: title, URL: "/help/" + slug})
				}
				break
			}
		}
	}
	switch {
	case len(cards) > 0:
		var b strings.Builder
		b.WriteString("Ini yang kutemukan")
		if len(und) > 0 {
			b.WriteString(" sesuai permintaanmu")
		}
		b.WriteString(":\n")
		for i, e := range cards {
			price := ""
			if e.MinFinalPrice != nil {
				price = " mulai " + formatIDR(*e.MinFinalPrice)
			}
			fmt.Fprintf(&b, "%d. %s — %s, %s (%s)%s → /events/%s\n", i+1, e.Title, e.VenueName, e.City, e.StartsAt.Format("2 Jan 2006"), price, e.Slug)
		}
		ans.Answer = strings.TrimSpace(b.String())
	case len(ans.Help) > 0:
		ans.Answer = "Aku belum menemukan event yang cocok, tapi mungkin artikel bantuan ini menjawab pertanyaanmu."
	default:
		ans.Answer = "Aku belum menemukan event yang cocok. Coba sebut kota, genre, atau budget — misalnya \"konser indie di Bandung di bawah 300 ribu\"."
	}
	return ans, nil
}
