package main

// Seed katalog demo yang kaya (hanya dev/staging): 14 konser di 6 kota, artis, tier, status penjualan bervariasi, dan
// gambar poster/artis yang diunggah lewat jalur resmi MediaService (varian 400/800/1600). Jalankan SETELAH cmd/seed.
//
//	cd backend && set -a && . ./.env && set +a && go run ./cmd/seed-demo -photos ../frontend/design-src/originals/concerts
//
// Foto: frontend/scripts/assets/fetch_concert_photos.py (CC0, kredit di meta.json). Idempoten: berhenti bila sudah ada.
// DITOLAK di production.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"tixgo/backend/internal/app"
	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/config"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/platform"
)

type venue struct {
	key, name, city, addr, tz string
	lat, lng                  float64
	cap                       int
}
type artist struct{ slug, name, bio, photo string }
type tier struct {
	name        string
	price       int64
	quota, sold int
}
type event struct {
	slug, title, genre, venue, desc, photo string
	artists                                []string
	days                                   int  // mulai acara: hari dari sekarang
	saleInDays                             int  // >0 = penjualan belum dibuka (upcoming)
	queue                                  bool // wajib antre
	age                                    int
	tiers                                  []tier
}

var venues = []venue{
	{"jcc", "Jakarta Convention Center", "Jakarta", "Jl. Gatot Subroto, Senayan", "Asia/Jakarta", -6.2255, 106.8017, 5000},
	{"ganesha", "Sasana Budaya Ganesha", "Bandung", "Jl. Tamansari 73", "Asia/Jakarta", -6.8938, 107.6105, 2500},
	{"tbj", "Taman Budaya Yogyakarta", "Yogyakarta", "Jl. Sri Wedani No. 1", "Asia/Jakarta", -7.7996, 110.3695, 3000},
	{"grandcity", "Grand City Convention Hall", "Surabaya", "Jl. Walikota Mustajab 1", "Asia/Jakarta", -7.2607, 112.7509, 6000},
	{"gwk", "GWK Cultural Park", "Bali", "Jl. Raya Uluwatu, Ungasan", "Asia/Makassar", -8.8106, 115.1675, 8000},
	{"medan", "Medan International Convention Center", "Medan", "Jl. Gagak Hitam, Ringroad", "Asia/Jakarta", 3.5776, 98.6381, 4000},
}

var artists = []artist{
	{"senja-pagi", "Senja Pagi", "Duo folk-indie dari Yogyakarta yang menulis lagu tentang kota kecil, hujan sore, dan kopi yang terlalu manis.", "acoustic"},
	{"kota-jazz-trio", "Kota Jazz Trio", "Trio piano-bas-drum dari Bandung. Aransemen standar jazz dengan nuansa Nusantara dan ruang improvisasi yang lapang.", "jazz"},
	{"nada-malam", "Nada Malam", "Penyanyi pop dengan vokal hangat dan panggung yang selalu dipenuhi paduan suara penonton.", "singer"},
	{"rimba-raya", "Rimba Raya", "Kuartet rock dari Jakarta. Gitar tebal, drum berat, dan lirik yang jujur soal kota yang tidak pernah tidur.", "rock"},
	{"merah-delima", "Merah Delima", "Band rock alternatif dengan permainan cahaya merah khas di setiap panggung.", "redguitar"},
	{"aurora-prisma", "Aurora Prisma", "DJ dan produser elektronik dari Surabaya, dikenal lewat set berdurasi panjang yang naik perlahan.", "djnight"},
	{"horizon-collective", "Horizon Collective", "Kolektif musik elektronik lintas pulau yang membawa festival layar-lebar dan lighting sinematik.", "edmfest"},
	{"bintang-timur", "Bintang Timur", "Grup pop enam personel dengan koreografi rapi dan lagu yang gampang dinyanyikan bersama.", "cheer"},
	{"kirana-orkestra", "Kirana Orkestra", "Orkestra kamar 32 pemain yang menggabungkan repertoar klasik dengan karya komponis Indonesia.", "hall"},
	{"lembayung", "Lembayung", "Band folk-pop dari Yogyakarta, panggungnya selalu berakhir dengan penonton menyalakan lampu ponsel.", "festcrowd"},
	{"sabda-beat", "Sabda Beat", "Kolektif hip-hop dan produser beat dari Bandung dengan visual asap dan laser yang menjadi ciri khas.", "smoke"},
	{"biru-senja", "Biru Senja", "Band indie-rock bernuansa biru-merah, vokalis dan gitaris yang saling berbalas di atas panggung.", "bluered"},
	{"tur-terakhir", "Dara & Para Pejalan", "Penutup tur nasional 24 kota. Seluruh tiket habis dalam tiga menit.", "soldout"},
	{"piringan-hitam", "Piringan Hitam Society", "Kolektor dan DJ vinyl yang memutar soul, disko, dan city pop dari koleksi pribadi.", "vinyl"},
}

const refund = `{"type":"refundable","refundable_until":"%s","deduction_pct":10,"fee_refundable":false,"on_reschedule":{"window_days":14}}`

var events = []event{
	{"rimba-rock-fest", "Rimba Rock Fest 2026", "Rock", "jcc", "Satu malam penuh distorsi: empat band rock terbaik tahun ini, tata suara baru, dan area penonton berdiri tanpa sekat. Pintu dibuka 90 menit sebelum acara dimulai, band pembuka naik panggung tepat waktu.", "rock", []string{"rimba-raya", "merah-delima"}, 21, 0, true, 17, []tier{{"Festival", 275000, 600, 540}, {"VIP Standing", 650000, 150, 138}}},
	{"neon-nusantara-night", "Neon Nusantara Night", "Elektronik", "grandcity", "Delapan jam musik elektronik dengan laser, LED wall, dan sistem suara line-array. Dress code bebas, sepatu nyaman sangat dianjurkan.", "djnight", []string{"aurora-prisma"}, 14, 0, false, 18, []tier{{"General Admission", 220000, 1500, 760}, {"Front of Stage", 480000, 300, 120}}},
	{"horizon-electronic-festival", "Horizon Electronic Festival", "Elektronik", "gwk", "Festival elektronik dua hari di tebing Uluwatu. Tiga panggung, matahari terbenam di atas laut, dan penutup set sunrise.", "edmfest", []string{"horizon-collective", "aurora-prisma"}, 75, 10, true, 18, []tier{{"1-Day Pass", 550000, 3000, 0}, {"2-Day Pass", 950000, 2000, 0}, {"VIP Cliffside", 2400000, 200, 0}}},
	{"pesta-pop-anthem", "Pesta Pop Anthem", "Pop", "jcc", "Konser pop terbesar akhir tahun. Dua jam lagu yang kamu hafal di luar kepala, ditutup kembang api.", "cheer", []string{"bintang-timur", "nada-malam"}, 40, 0, false, 0, []tier{{"CAT 3", 350000, 2000, 1850}, {"CAT 2", 600000, 1200, 1100}, {"CAT 1", 950000, 500, 470}}},
	{"kirana-orkestra-malam", "Kirana Orkestra: Malam Nusantara", "Klasik", "tbj", "Orkestra kamar membawakan karya Ismail Marzuki, Gesang, dan komponis muda Indonesia dalam aransemen baru. Dianjurkan datang 30 menit lebih awal.", "hall", []string{"kirana-orkestra"}, 33, 0, false, 0, []tier{{"Balkon", 200000, 400, 380}, {"Orchestra", 350000, 600, 570}}},
	{"lembayung-festival", "Lembayung Festival", "Festival", "tbj", "Festival musik dan kuliner dua hari: folk, indie, dan jajanan kaki lima terbaik se-Yogyakarta di satu halaman luas.", "festcrowd", []string{"lembayung", "senja-pagi"}, 90, 5, false, 0, []tier{{"Early Bird", 180000, 500, 0}, {"Reguler", 260000, 2500, 0}}},
	{"gelombang-hiphop", "Gelombang: Hip-Hop & Beat Night", "Hip-Hop", "ganesha", "Panggung terbuka untuk produser beat dan rapper Bandung, ditambah sesi cipher di lobi. Bawa energi, jangan bawa botol kaca.", "smoke", []string{"sabda-beat"}, 28, 0, false, 17, []tier{{"Reguler", 165000, 1200, 1100}, {"Backstage Meet", 420000, 80, 75}}},
	{"biru-merah-live", "Biru Merah Live", "Indie", "grandcity", "Konser tunggal Biru Senja setelah dua tahun vakum. Album baru dimainkan utuh, lalu satu set lagu lama yang diminta penonton.", "bluered", []string{"biru-senja"}, 50, 3, false, 0, []tier{{"Festival", 240000, 1800, 0}, {"VIP", 520000, 200, 0}}},
	{"dara-tur-terakhir", "Dara & Para Pejalan: Tur Terakhir", "Pop", "jcc", "Penutup tur 24 kota. Tiket sudah habis; daftar tunggu dibuka untuk pembatalan dan resale resmi.", "soldout", []string{"tur-terakhir"}, 12, 0, true, 0, []tier{{"Festival", 400000, 800, 800}, {"VIP", 1100000, 120, 120}}},
	{"vinyl-night-session", "Vinyl Night Session", "Elektronik", "ganesha", "Soul, disko, dan city pop dari piringan hitam asli. Kapasitas dibatasi supaya ruang dansa tetap nyaman.", "vinyl", []string{"piringan-hitam"}, 7, 0, false, 18, []tier{{"Entry", 120000, 250, 170}}},
	{"red-room-sessions", "Red Room Sessions", "Rock", "medan", "Format intim: band rock memainkan set akustik-elektrik di ruangan bercahaya merah, maksimal 400 penonton.", "redguitar", []string{"merah-delima"}, 19, 0, false, 17, []tier{{"Standing", 195000, 400, 210}}},
	{"senja-indie-fest", "Senja Indie Fest", "Indie", "", "Festival indie sore hari dengan deretan band lokal, pasar zine, dan area duduk beralas rumput. Cocok datang bersama teman.", "acoustic", nil, 0, 0, false, 0, nil},
	{"malam-jazz-bandung", "Malam Jazz Bandung", "Jazz", "", "Kursi bernomor, sistem suara akustik, dan repertoar standar jazz yang dibawakan ulang dengan rasa Nusantara.", "jazz", nil, 0, 0, false, 0, nil},
	{"nada-malam-live", "Nada Malam Live 2027", "Pop", "", "Konser tunggal Nada Malam dengan antrean adil dan e-tiket dinamis. Lagu-lagu baru dan medley akustik di tengah acara.", "singer", nil, 0, 0, true, 0, nil},
}

var venueTZ = func() map[string]string {
	m := map[string]string{}
	for _, v := range venues {
		m[v.key] = v.tz
	}
	return m
}()

func main() {
	if err := run(); err != nil {
		slog.Error("seed-demo gagal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	dir := flag.String("photos", "", "folder foto (hasil fetch_concert_photos.py)")
	lay := flag.Bool("layouts", false, "buat ulang denah kursi realistis (teater/arena) untuk acara bernomor")
	venueDir := flag.String("venue-photos", "", "folder foto gedung venue + meta.json (frontend/design-src/originals/venues); mengisi foto & kredit")
	flag.Parse()
	if *dir == "" && !*lay && *venueDir == "" {
		return fmt.Errorf("-photos wajib diisi (atau pakai -layouts / -venue-photos)")
	}
	cfg, err := config.FromOS()
	if err != nil {
		return err
	}
	if cfg.IsProd() {
		return fmt.Errorf("seed-demo ditolak di production")
	}
	ctx := context.Background()
	b, err := app.Build(ctx, cfg, app.NewLogger(cfg))
	if err != nil {
		return err
	}
	defer b.Close()
	pool := b.Svc.Pool
	if *lay {
		return seedLayouts(ctx, b)
	}
	if *venueDir != "" && *dir == "" {
		return backfillVenuePhotos(ctx, b, *venueDir)
	}

	var adminID, ownerID, orgID string
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE email='admin@tixgo.local'`).Scan(&adminID); err != nil {
		return fmt.Errorf("jalankan cmd/seed dulu: %w", err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE email='organizer@tixgo.local'`).Scan(&ownerID); err != nil {
		return err
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM organizers WHERE owner_user_id=$1 LIMIT 1`, ownerID).Scan(&orgID); err != nil {
		return err
	}
	var done bool
	_ = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE slug='rimba-rock-fest')`).Scan(&done)
	if done {
		fmt.Println("seed-demo: sudah ada — dilewati")
		return nil
	}

	// 1) unggah semua foto sekali lewat MediaService (varian 400/800/1600)
	principal := &auth.Principal{UserID: adminID, Role: "admin"}
	assets := map[string]string{}
	photoOf := func(key, kind string) (string, error) {
		if a, ok := assets[kind+":"+key]; ok {
			return a, nil
		}
		f, err := os.Open(filepath.Join(*dir, key+".jpg"))
		if err != nil {
			return "", err
		}
		defer f.Close()
		v, err := b.Svc.Media.Upload(ctx, principal, kind, f)
		if err != nil {
			return "", fmt.Errorf("unggah %s: %w", key, err)
		}
		assets[kind+":"+key] = v.ID
		return v.ID, nil
	}

	now := time.Now().UTC()
	err = platform.WithTx(ctx, pool, func(tx pgx.Tx) error {
		venueID := map[string]string{}
		for _, v := range venues {
			vid := id.New()
			venueID[v.key] = vid
			if _, err := tx.Exec(ctx, `INSERT INTO venues (id,name,city,address,lat,lng,tz,capacity,created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
				vid, v.name, v.city, v.addr, v.lat, v.lng, v.tz, v.cap, adminID); err != nil {
				return err
			}
		}
		artistID := map[string]string{}
		for _, a := range artists {
			img, err := photoOf(a.photo, "artist")
			if err != nil {
				return err
			}
			var aid string
			err = tx.QueryRow(ctx, `INSERT INTO artists (id,slug,name,bio,image_key) VALUES ($1,$2,$3,$4,$5)
				ON CONFLICT (slug) DO UPDATE SET bio=EXCLUDED.bio, image_key=EXCLUDED.image_key RETURNING id`, id.New(), a.slug, a.name, a.bio, img).Scan(&aid)
			if err != nil {
				return err
			}
			artistID[a.slug] = aid
		}
		for _, e := range events {
			poster, err := photoOf(e.photo, "poster")
			if err != nil {
				return err
			}
			if e.venue == "" { // acara bawaan cmd/seed: lengkapi poster, deskripsi, artis
				if _, err := tx.Exec(ctx, `UPDATE events SET poster_key=$2, description=$3 WHERE slug=$1`, e.slug, poster, e.desc); err != nil {
					return err
				}
				continue
			}
			eid := id.New()
			loc, err := time.LoadLocation(venueTZ[e.venue])
			if err != nil {
				return err
			}
			d := now.AddDate(0, 0, e.days).In(loc)
			start := time.Date(d.Year(), d.Month(), d.Day(), 19, 0, 0, 0, loc).UTC() // konser mulai 19.00 waktu venue
			var age any
			if e.age > 0 {
				age = e.age
			}
			if _, err := tx.Exec(ctx, `INSERT INTO events (id,organizer_id,venue_id,slug,title,description,genre,poster_key,starts_at,doors_at,ends_at,mode,status,published_at,queue_required,age_min,refund_policy,service_fee_bp,created_by)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'general','published',now(),$12,$13,$14,500,$15)`,
				eid, orgID, venueID[e.venue], e.slug, e.title, e.desc, e.genre, poster, start, start.Add(-90*time.Minute), start.Add(5*time.Hour), e.queue, age,
				fmt.Sprintf(refund, start.AddDate(0, 0, -7).Format(time.RFC3339)), ownerID); err != nil {
				return err
			}
			for i, a := range e.artists {
				if _, err := tx.Exec(ctx, `INSERT INTO event_artists (event_id,artist_id,billing_order) VALUES ($1,$2,$3)`, eid, artistID[a], i); err != nil {
					return err
				}
			}
			opens := now.Add(-24 * time.Hour)
			if e.saleInDays > 0 {
				opens = now.AddDate(0, 0, e.saleInDays)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO sale_phases (id,event_id,name,audience,starts_at,ends_at) VALUES ($1,$2,'Penjualan Umum','public',$3,$4)`, id.New(), eid, opens, start); err != nil {
				return err
			}
			for i, t := range e.tiers {
				if _, err := tx.Exec(ctx, `INSERT INTO tiers (id,event_id,name,price,quota,sold,sort,comp_quota) VALUES ($1,$2,$3,$4,$5,$6,$7,0)`, id.New(), eid, t.name, t.price, t.quota, t.sold, i); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Printf("seed-demo: selesai — %d venue, %d artis, %d konser, %d gambar diunggah\n", len(venues), len(artists), len(events), len(assets))
	return nil
}

// venueKey memetakan nama venue (termasuk yang dibuat cmd/seed) ke berkas foto gedungnya.
var venueKey = map[string]string{
	"Jakarta Convention Center":             "jcc",
	"Sasana Budaya Ganesha":                 "ganesha",
	"Taman Budaya Yogyakarta":               "tbj",
	"Grand City Convention Hall":            "grandcity",
	"GWK Cultural Park":                     "gwk",
	"Istora Senayan":                        "istora",
	"Medan International Convention Center": "medan",
}

type photoMeta struct {
	License string `json:"license"`
	Author  string `json:"author"`
	Source  string `json:"source"`
}

func creditOf(m photoMeta) string {
	lic := m.License
	if strings.HasPrefix(lic, "Public domain") {
		lic = "domain publik"
	}
	return "Foto: " + m.Author + ", " + lic
}

// backfillVenuePhotos memasang foto gedung asli beserta kredit; venue tanpa berkas foto dikosongkan
// (lebih baik tanpa foto daripada foto yang salah). Aman diulang.
func backfillVenuePhotos(ctx context.Context, b *app.Built, dir string) error {
	pool := b.Svc.Pool
	var adminID string
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE email='admin@tixgo.local'`).Scan(&adminID); err != nil {
		return fmt.Errorf("jalankan cmd/seed dulu: %w", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return err
	}
	metas := map[string]photoMeta{}
	if err := json.Unmarshal(raw, &metas); err != nil {
		return fmt.Errorf("meta.json: %w", err)
	}
	p := &auth.Principal{UserID: adminID, Role: "admin"}
	assetOf := map[string]string{}
	for name, key := range venueKey {
		f, err := os.Open(filepath.Join(dir, key+".jpg"))
		if err != nil {
			tag, err2 := pool.Exec(ctx, `UPDATE venues SET image_key=NULL, image_credit=NULL, image_source=NULL WHERE name=$1`, name)
			if err2 != nil {
				return err2
			}
			fmt.Printf("seed-demo: %s tanpa foto (%d baris dikosongkan)\n", name, tag.RowsAffected())
			continue
		}
		m, ok := metas[key]
		if !ok {
			_ = f.Close()
			return fmt.Errorf("meta.json tidak punya kredit untuk %q", key)
		}
		id, seen := assetOf[key]
		if !seen {
			v, err := b.Svc.Media.Upload(ctx, p, "venue", f)
			if err != nil {
				_ = f.Close()
				return fmt.Errorf("unggah %s: %w", key, err)
			}
			id = v.ID
			assetOf[key] = id
		}
		_ = f.Close()
		tag, err := pool.Exec(ctx, `UPDATE venues SET image_key=$1, image_credit=$2, image_source=$3 WHERE name=$4`, id, creditOf(m), m.Source, name)
		if err != nil {
			return err
		}
		fmt.Printf("seed-demo: %s -> %s (%d baris)\n", name, creditOf(m), tag.RowsAffected())
	}
	return nil
}
