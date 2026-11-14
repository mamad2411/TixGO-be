# `internal/`

Kode aplikasi privat. Alur satu arah: **handler** (HTTP tipis) → **service** (bisnis, transaksi, audit, outbox) → **repository/sqlcgen** (SQL statis hasil sqlc).

| Paket | Isi |
|---|---|
| `handler/` | Route Gin per modul (`mountXxx`), binding & validasi input, tanpa logika bisnis |
| `service/` | Seluruh aturan bisnis (satu berkas ≈ satu modul): auth, katalog, antrean, hold, order, payment, refund, ticket, gate, finance, resale, squad, … ; `services.go` = container & urutan inisialisasi |
| `repository/sqlcgen/` | Kode hasil `sqlc generate` dari `../queries/*.sql` (jangan diedit) |
| `middleware/` | Auth, RBAC, MFA, rate limit, idempotensi, maintenance, akses log + metrik |
| `payment/` | Adapter gateway (Midtrans, Fake untuk tes/dev, Unconfigured) + verifikasi signature |
| `notify/` | Mailer/SMS/Push (SMTP, WebPush), katalog templat id/en |
| `jobs/` | Antrian kerja: asynq (produksi) & Inline (tes) |
| `ws/` | Hub WebSocket (melody) di atas bus Redis `tixgo:events` |
| `storage/`, `imgproc/`, `svgsafe/` | Object storage MinIO/Memory, pipeline gambar, sanitasi SVG denah |
| `auth/`, `id/`, `clock/`, `ratelimit/`, `reqmeta/`, `httpx/`, `apperr/`, `metrics/`, `platform/`, `config/` | Pustaka dasar lintas modul |
| `app/` | Perakitan dependensi produksi; `server/` = engine Gin; `apidoc/` = pembangkit OpenAPI dari router |
| `apitest/` | Tes integrasi (DB `tixgo_test`, Redis DB 15, jam/gateway/mailer palsu) |

Dokumen: `../docs/01-ARCHITECTURE.md`, `../../docs/` (dokumen produk root).
