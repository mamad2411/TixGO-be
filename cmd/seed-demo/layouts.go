package main

// Layout kursi realistis untuk acara bernomor (teater melengkung & arena berundak). Dipanggil lewat `-layouts`.
// Koordinat: panggung berpusat di (0,0), kursi berada pada busur di bawahnya (y positif ke bawah). Satuan bebas;
// FE menormalkan. Mengganti seat/section acara target (hanya dev/staging; seed-demo menolak production).

import (
	"context"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"

	"tixgo/backend/internal/app"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/platform"
)

type arc struct {
	code, name string
	rows       int
	r0, dr     float64 // radius baris pertama & jarak antar baris
	a1, a2     float64 // sudut (derajat, 0 = lurus ke depan panggung)
	pitch      float64 // lebar per kursi
	tier       func(row int) string
	wheelchair bool // dua kursi paling luar baris pertama = ruang kursi roda
}

type layoutTier struct {
	name  string
	price int64
}

type layout struct {
	slug  string
	tiers []layoutTier // dibuat bila belum ada
	arcs  []arc
	sold  float64 // proporsi terjual (acak, deterministik)
	held  float64
}

func rowLetter(i int) string {
	if i < 26 {
		return string(rune('A' + i))
	}
	return string(rune('A'+i/26-1)) + string(rune('A'+i%26))
}

var layouts = []layout{
	{ // Teater klasik: orkestra 3 blok + balkon 3 blok
		slug:  "kirana-orkestra-malam",
		tiers: []layoutTier{{"Loge", 450000}},
		sold:  0.46, held: 0.02,
		arcs: []arc{
			{"ORC-L", "Orkestra Kiri", 13, 250, 24, -54, -17, 23, func(r int) string { return pick(r < 4, "Loge", "Orchestra") }, true},
			{"ORC-C", "Orkestra Tengah", 13, 250, 24, -13, 13, 23, func(r int) string { return pick(r < 5, "Loge", "Orchestra") }, false},
			{"ORC-R", "Orkestra Kanan", 13, 250, 24, 17, 54, 23, func(r int) string { return pick(r < 4, "Loge", "Orchestra") }, true},
			{"BAL-L", "Balkon Kiri", 6, 610, 26, -52, -15, 24, func(int) string { return "Balkon" }, false},
			{"BAL-C", "Balkon Tengah", 6, 610, 26, -11, 11, 24, func(int) string { return "Balkon" }, false},
			{"BAL-R", "Balkon Kanan", 6, 610, 26, 15, 52, 24, func(int) string { return "Balkon" }, false},
		},
	},
	{ // Aula pertunjukan: VIP depan, CAT1 tengah, CAT2 balkon
		slug:  "malam-jazz-bandung",
		tiers: []layoutTier{{"VIP", 750000}, {"CAT2", 300000}},
		sold:  0.34, held: 0.015,
		arcs: []arc{
			{"ORC-L", "Orkestra Kiri", 10, 230, 24, -50, -14, 23, func(r int) string { return pick(r < 2, "VIP", "CAT1") }, true},
			{"ORC-C", "Orkestra Tengah", 10, 230, 24, -10, 10, 23, func(r int) string { return pick(r < 3, "VIP", "CAT1") }, false},
			{"ORC-R", "Orkestra Kanan", 10, 230, 24, 14, 50, 23, func(r int) string { return pick(r < 2, "VIP", "CAT1") }, true},
			{"BAL-C", "Balkon", 5, 520, 26, -42, 42, 24, func(int) string { return "CAT2" }, false},
		},
	},
	{ // Arena berundak: lantai, bowl bawah, ring atas
		slug:  "pesta-pop-anthem",
		tiers: []layoutTier{{"VIP Floor", 1400000}},
		sold:  0.52, held: 0.02,
		arcs: []arc{
			{"FLR-L", "Floor Kiri", 9, 150, 22, -42, -4, 22, func(r int) string { return pick(r < 3, "VIP Floor", "CAT 1") }, true},
			{"FLR-R", "Floor Kanan", 9, 150, 22, 4, 42, 22, func(r int) string { return pick(r < 3, "VIP Floor", "CAT 1") }, true},
			{"LWR-L", "Bowl Bawah Kiri", 8, 400, 24, -102, -46, 24, func(int) string { return "CAT 2" }, false},
			{"LWR-C", "Bowl Bawah Tengah", 8, 400, 24, -40, 40, 24, func(int) string { return "CAT 2" }, false},
			{"LWR-R", "Bowl Bawah Kanan", 8, 400, 24, 46, 102, 24, func(int) string { return "CAT 2" }, false},
			{"UPR-L", "Ring Atas Kiri", 8, 660, 26, -112, -38, 26, func(int) string { return "CAT 3" }, false},
			{"UPR-C", "Ring Atas Tengah", 8, 660, 26, -33, 33, 26, func(int) string { return "CAT 3" }, false},
			{"UPR-R", "Ring Atas Kanan", 8, 660, 26, 38, 112, 26, func(int) string { return "CAT 3" }, false},
		},
	},
}

func pick(c bool, a, b string) string {
	if c {
		return a
	}
	return b
}

func seedLayouts(ctx context.Context, b *app.Built) error {
	if err := seedPromos(ctx, b); err != nil {
		return err
	}
	pool := b.Svc.Pool
	for _, l := range layouts {
		var eid string
		if err := pool.QueryRow(ctx, `SELECT id FROM events WHERE slug=$1`, l.slug).Scan(&eid); err != nil {
			return fmt.Errorf("%s: %w (jalankan seed & seed-demo dulu)", l.slug, err)
		}
		err := platform.WithTx(ctx, pool, func(tx pgx.Tx) error {
			// Bersihkan peta lama + jadikan acara bernomor. Jumlah terjual berasal dari status kursi.
			if _, err := tx.Exec(ctx, `DELETE FROM seats WHERE event_id=$1`, eid); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM sections WHERE event_id=$1`, eid); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE events SET mode='seated' WHERE id=$1`, eid); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE tiers SET quota=0, sold=0, held=0 WHERE event_id=$1`, eid); err != nil {
				return err
			}
			for i, t := range l.tiers {
				if _, err := tx.Exec(ctx, `INSERT INTO tiers (id,event_id,name,price,quota,sold,sort,comp_quota) SELECT $1,$2,$3,$4,0,0,$5,0
					WHERE NOT EXISTS (SELECT 1 FROM tiers WHERE event_id=$2 AND name=$3)`, id.New(), eid, t.name, t.price, 10+i); err != nil {
					return err
				}
			}
			tierID := map[string]string{}
			rows, err := tx.Query(ctx, `SELECT name, id FROM tiers WHERE event_id=$1`, eid)
			if err != nil {
				return err
			}
			for rows.Next() {
				var n, tid string
				if err := rows.Scan(&n, &tid); err != nil {
					return err
				}
				tierID[n] = tid
			}
			rows.Close()

			rng := newDetRand(uint64(len(l.slug)) * 7919)
			var data [][]any
			for si, a := range l.arcs {
				sec := id.New()
				first := tierID[a.tier(0)]
				if _, err := tx.Exec(ctx, `INSERT INTO sections (id,event_id,tier_id,name,sort) VALUES ($1,$2,$3,$4,$5)`, sec, eid, first, a.name, si); err != nil {
					return err
				}
				for r := 0; r < a.rows; r++ {
					R := a.r0 + float64(r)*a.dr
					n := int(R * (a.a2 - a.a1) * math.Pi / 180 / a.pitch)
					if n < 3 {
						n = 3
					}
					tn := a.tier(r)
					tid, ok := tierID[tn]
					if !ok {
						return fmt.Errorf("tier %q tidak ada di %s", tn, l.slug)
					}
					for k := 0; k < n; k++ {
						th := (a.a1 + (a.a2-a.a1)*(float64(k)+0.5)/float64(n)) * math.Pi / 180
						x, y := R*math.Sin(th), R*math.Cos(th)
						status := "available"
						// kursi depan-tengah lebih cepat terjual
						bias := 1.0
						if r < 4 {
							bias = 1.25
						}
						switch p := rng.Float64(); {
						case p < l.sold*bias:
							status = "sold"
						case p < l.sold*bias+l.held:
							status = "held"
						}
						wheel := a.wheelchair && r == 0 && (k < 2 || k >= n-2)
						if wheel {
							status = "available"
						}
						data = append(data, []any{id.New(), eid, sec, tid, rowLetter(r), fmt.Sprint(k + 1), fmt.Sprintf("%s %s%d", a.code, rowLetter(r), k+1),
							math.Round(x*10) / 10, math.Round(y*10) / 10, wheel, status})
					}
				}
			}
			_, err = tx.CopyFrom(ctx, pgx.Identifier{"seats"},
				[]string{"id", "event_id", "section_id", "tier_id", "row_label", "seat_no", "label", "x", "y", "accessible", "status"}, pgx.CopyFromRows(data))
			if err == nil {
				fmt.Printf("layout %-24s %5d kursi, %d seksi\n", l.slug, len(data), len(l.arcs))
			}
			return err
		})
		if err != nil {
			return fmt.Errorf("%s: %w", l.slug, err)
		}
	}
	return nil
}

// demoPromos = voucher contoh per acara (kode, jenis, nilai, minimal belanja). Idempoten (ON CONFLICT DO NOTHING).
var demoPromos = []struct {
	slug, code, kind string
	value, minTotal  int64
}{
	{"neon-nusantara-night", "NEON25", "percent", 25, 0},
	{"kirana-orkestra-malam", "KIRANA50K", "fixed", 50000, 0},
	{"pesta-pop-anthem", "POP15", "percent", 15, 500000},
	{"malam-jazz-bandung", "JAZZ10", "percent", 10, 0},
	{"gelombang-hiphop", "BEAT20", "percent", 20, 0},
	{"vinyl-night-session", "VINYL15K", "fixed", 15000, 0},
}

func seedPromos(ctx context.Context, b *app.Built) error {
	for _, p := range demoPromos {
		if _, err := b.Svc.Pool.Exec(ctx, `INSERT INTO promo_codes (id,event_id,code,kind,value,max_uses,max_per_account,min_total)
			SELECT $1, e.id, $2, $3, $4, 500, 2, $5 FROM events e WHERE e.slug=$6 ON CONFLICT (event_id, code) DO NOTHING`,
			id.New(), p.code, p.kind, p.value, p.minTotal, p.slug); err != nil {
			return err
		}
	}
	return nil
}

// detRand: pembangkit deterministik kecil (splitmix64) agar denah demo selalu sama untuk slug yang sama.
// Sengaja bukan math/rand: ini data tampilan, bukan kebutuhan keamanan, dan hasilnya harus dapat diulang.
type detRand struct{ s uint64 }

func newDetRand(seed uint64) *detRand { return &detRand{s: seed} }

func (r *detRand) Float64() float64 {
	r.s += 0x9e3779b97f4a7c15
	z := r.s
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	z ^= z >> 31
	return float64(z>>11) / float64(1<<53)
}
