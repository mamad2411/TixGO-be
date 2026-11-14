package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5"

	"tixgo/backend/internal/app"
	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/config"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/platform"
)

// Seed data demo (hanya dev/staging). Idempoten: berhenti bila admin demo sudah ada. DITOLAK di production.
// Kata sandi demo semua akun: Demo-Pass-123!  •  Rahasia TOTP admin/finance/organizer dicetak sekali untuk didaftarkan ke aplikasi autentikator.
const demoPassword = "Demo-Pass-123!"

func main() {
	if err := run(); err != nil {
		slog.Error("seed gagal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	loadN := 0
	for i, a := range os.Args {
		if a == "-load" && i+1 < len(os.Args) {
			if _, err := fmt.Sscan(os.Args[i+1], &loadN); err != nil {
				return fmt.Errorf("-load butuh angka: %w", err)
			}
		}
	}
	cfg, err := config.FromOS()
	if err != nil {
		return err
	}
	if cfg.IsProd() {
		return fmt.Errorf("seed ditolak di production")
	}
	ctx := context.Background()
	log := app.NewLogger(cfg)
	b, err := app.Build(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer b.Close()
	pool := b.Svc.Pool
	if loadN > 0 {
		return seedLoadUsers(ctx, b, cfg, loadN)
	}
	var exists bool
	if err := pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE email='admin@tixgo.local')").Scan(&exists); err != nil {
		return err
	}
	if exists {
		fmt.Println("seed: data demo sudah ada — dilewati")
		return nil
	}
	hasher := auth.NewPasswordHasher(auth.PasswordParams{MemoryKB: cfg.ArgonMemoryKB, Time: cfg.ArgonTime, Threads: cfg.ArgonThreads})
	hash := hasher.Hash(demoPassword)
	now := time.Now().UTC()

	secrets := map[string]string{}
	phone := int(now.Unix() % 1000000)
	nextPhone := func() int { phone++; return phone }
	err = platform.WithTx(ctx, pool, func(tx pgx.Tx) error {
		user := func(email, name, role string, mfa bool) (string, error) {
			uid := id.New()
			if _, err := tx.Exec(ctx, `INSERT INTO users (id,email,password_hash,name,role,email_verified_at,phone,phone_verified_at,mfa_enabled) VALUES ($1,$2,$3,$4,$5,now(),$6,now(),$7)`,
				uid, email, hash, name, role, fmt.Sprintf("+62812%08d", nextPhone()), mfa); err != nil {
				return "", err
			}
			if mfa {
				sec := auth.NewTOTPSecret()
				enc, err := b.Svc.Box.Encrypt([]byte(sec))
				if err != nil {
					return "", err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO mfa_totp (user_id,secret_enc,confirmed_at) VALUES ($1,$2,now())`, uid, enc); err != nil {
					return "", err
				}
				secrets[email] = sec
			}
			return uid, nil
		}
		admin, err := user("admin@tixgo.local", "Admin TixGo", "admin", true)
		if err != nil {
			return err
		}
		for _, u := range []struct{ e, n, r string }{{"finance@tixgo.local", "Finance TixGo", "finance"}, {"support@tixgo.local", "Support TixGo", "support"}, {"editor@tixgo.local", "Editor TixGo", "editor"}} {
			if _, err := user(u.e, u.n, u.r, u.r == "finance"); err != nil {
				return err
			}
		}
		owner, err := user("organizer@tixgo.local", "Panggung Kita", "organizer", true)
		if err != nil {
			return err
		}
		for i, n := range []string{"Rani", "Bimo", "Sari"} {
			if _, err := user(fmt.Sprintf("%s@tixgo.local", []string{"rani", "bimo", "sari"}[i]), n+" Demo", "customer", false); err != nil {
				return err
			}
		}
		for _, d := range []struct{ kind, title string }{{"terms", "Syarat & Ketentuan"}, {"privacy", "Kebijakan Privasi"}, {"refund", "Kebijakan Refund"}, {"organizer_terms", "Ketentuan Organizer"}} {
			if _, err := tx.Exec(ctx, `INSERT INTO legal_documents (id,kind,locale,version,title,body,published_at,created_by) VALUES ($1,$2,'id',1,$3,$4,now(),$5)`,
				id.New(), d.kind, d.title, "# "+d.title+"\n\nTeks demo untuk pengembangan. WAJIB diganti dokumen yang ditinjau pihak hukum sebelum go-live.", admin); err != nil {
				return err
			}
		}
		org := id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO organizers (id,name,owner_user_id,type,legal_name,contact_email,kyb_status,kyb_reviewed_by,kyb_reviewed_at,contract_accepted_at) VALUES ($1,'Panggung Kita',$2,'company','PT Panggung Kita','organizer@tixgo.local','approved',$3,now(),now())`, org, owner, admin); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO organizer_members (organizer_id,user_id,role) VALUES ($1,$2,'owner')`, org, owner); err != nil {
			return err
		}
		acct, err := b.Svc.Box.Encrypt([]byte("1234567890"))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO bank_accounts (id,organizer_id,bank_code,account_no_enc,account_no_last4,account_name,verified_at,active_from) VALUES ($1,$2,'BCA',$3,'7890','PT Panggung Kita',now(),now()-interval '30 days')`, id.New(), org, acct); err != nil {
			return err
		}
		venues := map[string]string{}
		for _, v := range []struct {
			key, name, city, addr string
			lat, lng              float64
			cap                   int
		}{
			{"gbk", "Istora Senayan", "Jakarta", "Jl. Pintu Satu Senayan", -6.2185, 106.8023, 7000},
			{"sabuga", "Sasana Budaya Ganesha", "Bandung", "Jl. Tamansari 73", -6.8938, 107.6105, 2500},
		} {
			vid := id.New()
			venues[v.key] = vid
			if _, err := tx.Exec(ctx, `INSERT INTO venues (id,name,city,address,lat,lng,capacity,created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, vid, v.name, v.city, v.addr, v.lat, v.lng, v.cap, admin); err != nil {
				return err
			}
		}
		artists := map[string]string{}
		for _, a := range []struct{ slug, name string }{{"senja-pagi", "Senja Pagi"}, {"kota-jazz-trio", "Kota Jazz Trio"}, {"nada-malam", "Nada Malam"}} {
			aid := id.New()
			artists[a.slug] = aid
			if _, err := tx.Exec(ctx, `INSERT INTO artists (id,slug,name) VALUES ($1,$2,$3)`, aid, a.slug, a.name); err != nil {
				return err
			}
		}
		type ev struct {
			slug, title, genre, mode, venue, artist string
			days                                    int
			queue                                   bool
			tiers                                   [][3]any // nama, harga, kuota
		}
		for _, e := range []ev{
			{"senja-indie-fest", "Senja Indie Fest", "Indie", "general", "gbk", "senja-pagi", 30, false, [][3]any{{"Early Bird", 150000, 200}, {"Reguler", 250000, 800}}},
			{"malam-jazz-bandung", "Malam Jazz Bandung", "Jazz", "seated", "sabuga", "kota-jazz-trio", 45, false, [][3]any{{"CAT1", 450000, 0}}},
			{"nada-malam-live", "Nada Malam Live 2027", "Pop", "general", "gbk", "nada-malam", 60, true, [][3]any{{"Festival", 350000, 3000}, {"VIP", 900000, 300}}},
		} {
			eid := id.New()
			if _, err := tx.Exec(ctx, `INSERT INTO events (id,organizer_id,venue_id,slug,title,description,genre,starts_at,doors_at,ends_at,mode,status,published_at,queue_required,refund_policy,service_fee_bp,created_by)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'published',now(),$12,$13,500,$14)`,
				eid, org, venues[e.venue], e.slug, e.title, "Acara demo untuk pengembangan TixGo.", e.genre, now.AddDate(0, 0, e.days).Truncate(time.Hour), now.AddDate(0, 0, e.days).Add(-90*time.Minute).Truncate(time.Hour),
				now.AddDate(0, 0, e.days).Add(4*time.Hour).Truncate(time.Hour), e.mode, e.queue,
				fmt.Sprintf(`{"type":"refundable","refundable_until":"%s","deduction_pct":10,"fee_refundable":false,"on_reschedule":{"window_days":14}}`, now.AddDate(0, 0, e.days-7).Format(time.RFC3339)), owner); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO event_artists (event_id,artist_id) VALUES ($1,$2)`, eid, artists[e.artist]); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO sale_phases (id,event_id,name,audience,starts_at,ends_at) VALUES ($1,$2,'Penjualan Umum','public',now()-interval '1 day',$3)`, id.New(), eid, now.AddDate(0, 0, e.days)); err != nil {
				return err
			}
			tierIDs := []string{}
			for i, t := range e.tiers {
				tid := id.New()
				tierIDs = append(tierIDs, tid)
				quota := t[2].(int)
				if e.mode == "seated" {
					quota = 0
				}
				if _, err := tx.Exec(ctx, `INSERT INTO tiers (id,event_id,name,price,quota,sort,comp_quota) VALUES ($1,$2,$3,$4,$5,$6,10)`, tid, eid, t[0], t[1], quota, i); err != nil {
					return err
				}
			}
			if e.mode == "seated" {
				sec := id.New()
				if _, err := tx.Exec(ctx, `INSERT INTO sections (id,event_id,tier_id,name,sort) VALUES ($1,$2,$3,'A',0)`, sec, eid, tierIDs[0]); err != nil {
					return err
				}
				rows := [][]any{}
				for r := 1; r <= 8; r++ {
					for s := 1; s <= 12; s++ {
						rows = append(rows, []any{id.New(), eid, sec, tierIDs[0], fmt.Sprint(r), fmt.Sprint(s), fmt.Sprintf("A-%d-%d", r, s), float64(s * 30), float64(r * 30), false, "available"})
					}
				}
				if _, err := tx.CopyFrom(ctx, pgx.Identifier{"seats"}, []string{"id", "event_id", "section_id", "tier_id", "row_label", "seat_no", "label", "x", "y", "accessible", "status"}, pgx.CopyFromRows(rows)); err != nil {
					return err
				}
			}
			if e.slug == "senja-indie-fest" {
				if _, err := tx.Exec(ctx, `INSERT INTO promo_codes (id,event_id,code,kind,value,max_uses,max_per_account) VALUES ($1,$2,'HEMAT10','percent',10,100,1)`, id.New(), eid); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE events SET resale_cap_pct=120 WHERE id=$1`, eid); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Println("seed: selesai. Akun (kata sandi: " + demoPassword + "):")
	for _, e := range []string{"admin", "finance", "support", "editor", "organizer", "rani", "bimo", "sari"} {
		fmt.Printf("  %s@tixgo.local\n", e)
	}
	fmt.Println("Rahasia TOTP (daftarkan ke aplikasi autentikator; hanya ditampilkan sekali):")
	for email, sec := range secrets {
		fmt.Printf("  %-24s %s\n", email, sec)
	}
	fmt.Println("Catatan: dokumen legal demo (v1) sudah diterbitkan; ganti dengan dokumen resmi sebelum go-live. Pengguna demo perlu menyetujuinya saat checkout.")
	return nil
}

// seedLoadUsers membuat N pengguna terverifikasi (load0001@tixgo.local …) yang sudah menyetujui dokumen legal — untuk uji beban (scripts/k6/onsale.js).
func seedLoadUsers(ctx context.Context, b *app.Built, cfg *config.Config, n int) error {
	hash := auth.NewPasswordHasher(auth.PasswordParams{MemoryKB: cfg.ArgonMemoryKB, Time: cfg.ArgonTime, Threads: cfg.ArgonThreads}).Hash(demoPassword)
	_, err := b.Svc.Pool.Exec(ctx, `
		WITH ins AS (
		  INSERT INTO users (id, email, password_hash, name, role, email_verified_at, phone, phone_verified_at)
		  SELECT lpad(upper(to_hex((extract(epoch from clock_timestamp())*1000000)::bigint + g)), 26, '0'), 'load' || lpad(g::text, 5, '0') || '@tixgo.local', $1, 'Load ' || g, 'customer', now(), '+6285' || lpad(g::text, 9, '0'), now()
		  FROM generate_series(1, $2::int) g
		  ON CONFLICT (email) DO NOTHING RETURNING id)
		INSERT INTO consents (id, user_id, document_id)
		SELECT lpad(upper(to_hex((extract(epoch from clock_timestamp())*1000000)::bigint + row_number() over () + 100000000)), 26, '0'), ins.id, d.id FROM ins, legal_documents d WHERE d.published_at IS NOT NULL AND d.kind IN ('terms','privacy')`, hash, n)
	if err != nil {
		return err
	}
	fmt.Printf("seed: %d pengguna beban (load00001@tixgo.local …, kata sandi %s)\n", n, demoPassword)
	return nil
}
