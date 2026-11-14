# 02 — Konvensi Kode Go

| Topik | Aturan |
|---|---|
| Error | bungkus konteks (`fmt.Errorf("…: %w", err)`); error domain = `apperr` (kode + status) agar handler memetakan ke API; jangan telan error |
| Context | `ctx` parameter pertama, diteruskan sampai query/Redis |
| Transaksi | dibuka di service (`InTx`/`InTxRaw`); **dilarang** ubah `seats.status`/`tiers.sold|held` di luar transaksi; efek samping setelah commit; kejadian pasti → `emit` (outbox) dalam tx |
| Uang | `int64` **rupiah bulat**; tanpa float; mata uang `IDR`; fee per tiket dibulatkan half-up; pajak 0 sampai Q13 |
| ID | ULID 26 karakter (`internal/id`) |
| Waktu | UTC di DB/JSON (RFC3339); waktu "sekarang" selalu `s.now()` (jam bisa dipalsukan di tes) |
| Respons | sukses `{"data","meta"}`; error `{"error":{"code","message","details","request_id"}}` |
| Idempotensi | POST pembuat order/bayar/refund/pencairan wajib `Idempotency-Key` |
| Validasi | tag `binding` di DTO; panjang/format/tipe diperiksa sebelum dipakai; SQL hanya sqlc/berparameter |
| SQL | `queries/*.sql` → `sqlc generate`. Jangan campur `$n` dan `sqlc.arg()` dalam satu query; parameter yang dipakai dua kali dengan tipe berbeda → `sqlc.arg(x)::text` |
| Log | `log/slog` terstruktur; tanpa secret/PII (email, HP, token, nomor rekening) |
| Rahasia | kolom `*_enc` (AES-GCM, `ENC_KEY`); token disimpan sebagai hash; bandingkan secret constant-time |
| Peran | `RequireRole` + `RequireMFA` untuk admin/finance; cek kepemilikan/tenant di service |
| Audit | aksi sensitif/akses data pribadi oleh staf → `Audit.Log` (nilai mentah rahasia tidak dicatat) |
| Metrik | counter bisnis di `internal/metrics` (label berkardinalitas rendah) |
| Lint | `golangci-lint run` = 0 temuan (errcheck, gocritic, revive, gosec, nestif ≤ 15). Pengecualian terdokumentasi di `.golangci.yml` |
| Konfigurasi | semua env lewat `internal/config`; kombinasi berbahaya di production ditolak saat start |

## Menambah modul (ringkas)
`migrasi → queries/*.sql (sqlc generate) → service/<modul>.go (+ daftar di services.go) → handler (mountXxx + panggil di handler.go) → tes apitest → OPENAPI_UPDATE=1 …TestOpenAPIMatchesRouter → docs`. Sebelum menambah route: `grep` path-nya — path ganda = panic saat start.

## Pelajaran dari voralens-v2
Gin default mempercayai semua proxy → `X-Forwarded-For` bisa dispoof; `SetTrustedProxies(cfg.TrustedProxies)` (kosong = tidak percaya proxy mana pun).

## Sumber
`../docs/11-STATE-MACHINES.md`, `../../docs/12-RBAC-SECURITY.md`, `.golangci.yml`, kode `internal/service`.
