# GAP-BE — Audit celah backend (2026-11-10)

Hasil audit kelengkapan backend: dokumen (catatan MD, struktur, workflow, alur fitur), kode, tes, CI. Tiap temuan **dibuktikan** (perintah/berkas disebut). Status:
✅ ditutup di sesi ini · 🔴 butuh keputusan pemilik produk · 🟡 kerja BE yang masih terbuka · ⚪ di luar backend (pihak luar / infra / FE).

## Stack backend (terverifikasi dari `go.mod`, `Dockerfile`, `docker-compose.dev.yml`)
| Lapisan | Pilihan | Versi |
|---|---|---|
| Bahasa | Go | 1.27.3 (dinaikkan dari 1.27.0 karena kerentanan stdlib) |
| HTTP | Gin | v1.12.0 |
| Database | PostgreSQL (driver `pgx/v5` v5.11.0, query via **sqlc**, migrasi **golang-migrate**) | image `postgres:17-alpine` |
| Cache / antrean / bus | Redis (`go-redis/v9` v9.22.0, `redis_rate` GCRA) | image `redis:7-alpine` |
| Job latar | asynq v0.26.0 + 19 loop periodik (`service/sched.go`) | — |
| Realtime | WebSocket: melody v1.4.0 + gorilla/websocket v1.5.3 | — |
| Object storage | MinIO (`minio-go/v7` v7.3.0) | image `minio:latest` (tidak di-pin) |
| Auth | JWT (`golang-jwt/v5`), argon2id (`x/crypto`), TOTP (`pquerna/otp`), AES-GCM kolom `*_enc` | — |
| Pembayaran | `payment.Gateway`: Midtrans (`midtrans-go` v1.3.8), Xendit (HTTP sendiri), fake | — |
| Dokumen / media | `fpdf` (PDF), `excelize` v2.11 (XLSX), `golang-ical`, `x/image`, `bluemonday`, `goldmark` | — |
| Observabilitas | `slog` JSON, Prometheus `client_golang` v1.24.1 | OTel belum |
| Mutu | golangci-lint v2.15 (CI; v2.13.x tidak bisa membaca stdlib Go 1.27.3), govulncheck, lefthook, k6 | — |

## ✅ Ditutup di sesi ini
| ID | Temuan | Bukti | Perbaikan |
|---|---|---|---|
| G-00 | **`POST /orders/{id}/pay` paralel (klik ganda) kadang 500** `duplicate key … payments_provider_provider_ref_key`; gateway bisa terpanggil dua kali. Tes `TestConcurrentPayCallsForSameOrderNeverError5xxOrDuplicateCharge` gagal 6 dari 15 run di bawah `-race` (3 dari 15 tanpa `-race`) | `go test -race -run … -count=15` | `order.go` `Pay`: percobaan bayar di-**claim** (baris `payments` dibuat sebelum gateway dipanggil) di dalam transaksi singkat dengan `pg_advisory_xact_lock` per order; pemanggil kalah → pakai hasil pemenang (`Reused`) atau 409 `CONFLICT` "sedang disiapkan"; claim yatim >2 mnt ditandai `failed`; gagal gateway menandai claim `failed`. Query baru `SetPaymentIntent`. Setelah: 30 dari 30 lulus (`-race -count=30`) |
| G-02 | **Tabel `outbox` tumbuh tanpa batas**: `DeleteOldOutbox` ada di `queries/outbox.sql` tetapi tidak dipanggil siapa pun | `grep -rn DeleteOldOutbox` hanya di sqlcgen | ditambahkan ke `PrivacyService.Purge`; tes baru `TestPurgeRemovesOldPublishedOutboxOnly` (merah dulu, lalu hijau) |
| G-03 | 16 paket `internal/*` tanpa README (AGENTS.md: "README.md di tiap folder") | `ls internal/*/README.md` | README ditulis dari komentar paket |
| G-04 | `.env.example` kekurangan 35 dari 71 variabel `config.go`; `03-ENV.md` tidak memuat Xendit/Midtrans URL/dll. | skrip pembanding | blok komentar di `.env.example`; `03-ENV.md` diperbarui + tautan ke `ENV-GENERATED.md` |
| G-05 | Dokumen menyebut 17 loop; kode punya **19** (`refund_overdue`, `event_reminders` tak tercatat); daftar job asynq (7) tidak ada | `sched.go` | `01-ARCHITECTURE` dan `CATALOG.md` (dibangkitkan) |
| G-06 | `docs/08 §2` dan `docs/13 T30` menyebut kode error yang tidak pernah dipancarkan: `DSR_PENDING`, `INSUFFICIENT_BALANCE`, `KYB_NOT_APPROVED`, `TICKET_USED` | `CATALOG.md` §1 (pemakaian 0) | **dokumen diselaraskan ke kode** (catatan di 08, T30 di 13). Opsi lain: ubah kode — hanya bila FE butuh kode spesifik |
| G-10 | Tidak ada alur per fitur, katalog, retensi, model ancaman, SLO, glosarium, indeks keputusan | — | `08-FLOWS.md`, `references/CATALOG.md` (dibangkitkan: `gen_docs.py catalog`), `09-DATA-SECURITY-SLO.md`, `10-GLOSSARY-ADR.md` |
| G-12 | Nilai enum `refunds.status` = `requested` dan `refund_failed` ada di CHECK tetapi tidak pernah ditulis kode | `grep` di `queries/` dan `service/` | didokumentasikan di `08-FLOWS §2`; **tidak** diubah (butuh migrasi; lihat G-17) |
| G-27 | **govulncheck: 18 kerentanan terpanggil** (stdlib `net/http`, `crypto/tls`, `crypto/x509`, `net/textproto`, `html/template` di Go 1.27.0; `golang.org/x/net` v0.58.0; `excelize` v2.11.0) padahal docs menulis "vuln 0" | `govulncheck ./...` exit 3 | `go 1.27.3`, `x/net` v0.62.0, `excelize` v2.11.1, `x/crypto` v0.58.0; ulang: **0 terpanggil** (2 modul tersisa tidak dipanggil kode). CI: golangci-lint dinaikkan v2.13 → v2.15 |
| G-09 | Tidak ada batas body global; server hanya `ReadHeaderTimeout` | `cmd/api/main.go` | middleware `BodyLimit` (JSON 1 MiB, multipart 12 MiB → 413 `PAYLOAD_TOO_LARGE`), server: `ReadTimeout 30s`, `WriteTimeout 60s`, `IdleTimeout 120s`, `MaxHeaderBytes 64 KiB`; tes `TestOversizedJSONBodyRejectedWith413` (merah dulu: 400, lalu hijau) |
| G-28 | `golangci-lint`: `countPayments` tak terpakai setelah perbaikan G-00 | `golangci-lint run` | fungsi dihapus; lint 0 issue (v2.15.0) |
| G-24 | `gen_docs.py` belum membangkitkan katalog; default env `oneOf` tampil rusak (`"midtrans", "midtrans", …`) | `ENV-GENERATED.md` | sub-perintah `catalog` ditambah; default env dirapikan |
| G-25 | Tes `apitest` diam-diam gagal acak bila dijalankan dua proses bersamaan (DB `tixgo_test` dipakai bersama): 9 tes gagal, lalu 146 lulus saat sendirian | dua run berurutan | peringatan di `04-TESTING.md` dan README `internal/apitest` |

## 🔴 Butuh keputusan Anda
| ID | Masalah | Rekomendasi saya |
|---|---|---|
| G-01 | **Gateway utama tidak konsisten.** `docs/14` Q1 dan `docs/22` (2026-10-04) = Midtrans, Xendit "tidak dipakai di 1.0". Tetapi kode sudah punya Xendit, `GO-LIVE.md` menulis "Xendit aktif; Midtrans cadangan", `STATUS-BE` M7 menyebut Xendit diuji ke sandbox nyata. Default `config.go` = `midtrans`. Konstanta jendela refund per metode "dari Midtrans, belum diverifikasi untuk Xendit" | pilih satu untuk 1.0, perbarui Q1 + `docs/22`, set default `PAYMENT_GATEWAY`, dan catat gateway kedua sebagai cadangan teruji atau dibuang. Saya tidak mengubah keputusan Anda tanpa diminta |
| G-13 | Masa simpan data (audit log, scan gate, webhook log, pesan support, dsb.) belum diputuskan; butuh tinjauan hukum (Q9) | pakai usulan (T) di `09-DATA-SECURITY-SLO §1b`, lalu konfirmasi hukum |
| G-26 | `ADR-05` pajak/PPN (Q13) | menunggu konsultan pajak — blocker go-live |

## 🟡 Terbuka — kerja backend
| ID | Temuan | Bukti | Usulan |
|---|---|---|---|
| G-07 | **Repo `backend/` belum punya satu commit pun** (semua berkas `??`), tanpa remote; cabang lokal `master` sedangkan `.github/workflows/ci.yml` hanya memicu `main` ⇒ CI belum pernah berjalan | `git log` kosong, `git branch --show-current` = master | commit awal + ganti nama cabang ke `main` + buat remote; baru jalankan alur push-hijau. Saya tidak commit/push tanpa diminta |
| G-08 | Keputusan Q15 ("retensi support ikut `privacy.purge`") belum diimplementasikan: `Support.Sweep` hanya menutup tiket; `support_messages`/`support_tickets` tidak pernah dihapus/anonimkan berdasar umur | `support.go:330`, daftar tabel tanpa DELETE | tambah purge/anonimisasi berumur di `Purge` setelah G-13 diputuskan |
| G-11 | CI (`ci.yml`) menjalankan build, vet, golangci-lint, `test -race`, govulncheck, docker build. **Tidak ada**: `sqlc diff`, pengecekan docs dibangkitkan (`gen_docs.py` drift), lint OpenAPI (`redocly.yaml` ada, `redocly` tidak terpasang), uji migrasi up/down/up, atau cek `go mod tidy` | baca `ci.yml` | tambah langkah `sqlc diff`, `go mod tidy -diff`, `gen_docs.py` + `git diff --exit-code` |
| G-14 | 165 dari 346 operasi OpenAPI tanpa `description` (semua punya `summary`, respons 4xx, dan requestBody) | `api/openapi.yaml` | isi deskripsi perilaku (idempotensi, rate limit, role) — bisa dibangkitkan dari anotasi handler |
| G-15 | Rotasi `ENC_KEY` butuh skrip re-enkripsi kolom `*_enc` yang **belum ada** (runbook melarang rotasi tanpa skrip) | `05-RUNBOOK` | tulis `cmd/rotate-enc` + tes |
| G-17 | Hapus nilai enum mati (`refunds.requested`, `refund_failed`) lewat migrasi, atau mulai dipakai (`refund_failed` untuk kegagalan permanen) | G-12 | keputusan kecil; tidak mendesak |
| G-18 | `DeleteOldOutbox` memakai `now()` DB, bukan `s.now()` (konvensi: waktu selalu lewat jam yang bisa dipalsukan) | `queries/outbox.sql` | ubah ke parameter bila perlu tes berbasis jam |
| G-19 | `PayResult` selalu mengisi `ClientKey`/`Env` dari config Midtrans walau gateway = xendit/fake | `order.go` `Pay` | isi sesuai gateway aktif (FE membaca `gateway`) |
| G-20 | Image dev `minio:latest` tidak di-pin | `docker-compose.dev.yml:28` | pin ke tag rilis |
| G-21 | Tes XLSX settlement belum diperiksa terhadap formula injection (CSV peserta sudah `csvSafe`) | `finance.go` `XLSX` | verifikasi perilaku excelize untuk string berawalan `=`; tambah tes |
| G-22 | Model ancaman: tidak ada tes otomatis yang memindai log dari PII/secret | `09 §2` | tes yang menjalankan alur auth/bayar lalu memeriksa log tertangkap |

## ⚪ Di luar backend / menunggu pihak luar
Pajak (Q13), tinjauan hukum UU PDP, uji gateway sandbox → produksi (callback nyata, refund API nyata), penyedia SMS/WhatsApp, SMTP produksi + VAPID, pen-test independen, pilot 1 organizer, uji beban ≥ 10× + WS, RPO/RTO + PITR, alert/dasbor, tracing OTel, login sosial, PDF/ekspor async, multi-sesi event, promo lanjut (early-bird/bundle/afiliasi), templat notifikasi yang bisa diedit, laporan keuangan per periode. Daftar rinci: `references/STATUS-BE.md`, `references/GO-LIVE.md`.

## Bukti verifikasi sesi ini
Lihat bagian "Hasil verifikasi" di bawah (diisi dari run nyata).

## Hasil verifikasi
Dijalankan 2026-11-10 setelah semua perubahan, satu proses sendirian, infra dev nyata (`make infra`):
| Pemeriksaan | Hasil |
|---|---|
| `go build ./... && go vet ./...` | lulus |
| `go test -race -count=1 ./...` | **semua paket `ok`** (apitest 101 dtk); 185 tes terdaftar (`TEST-MATRIX.md`) |
| Coverage statement (`-coverpkg=./internal/...`) | **72,6 %** |
| `golangci-lint run` (v2.15.0, Go 1.27.3) | **0 issues** |
| `govulncheck ./...` | **No vulnerabilities found** (sebelumnya 18 terpanggil) |
| Regresi G-00 `-race -count=30` | 30/30 lulus (sebelumnya 6/15 gagal) |
| Dokumen dibangkitkan ulang | `gen_docs.py api env tests catalog` (schema tidak diulang) |

## Sumber
`go.mod`, `Dockerfile`, `docker-compose.dev.yml`, `.github/workflows/ci.yml`, `cmd/api/main.go`, `internal/service/{order,privacy,support,finance,sched}.go`, `queries/{outbox,payments}.sql`, `internal/apitest/*`, `references/{STATUS-BE,GO-LIVE,CATALOG}.md`, `../../docs/14-AGENT-PLAYBOOK.md`, `../../docs/22-KEPUTUSAN-PEMBAYARAN-REFUND.md`.
