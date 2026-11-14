# 04 — Testing Backend

## Perintah
```
make -C .. infra                 # Postgres :5433, Redis :6380, MinIO :9100
go test ./...                    # semua (≈ 1 menit; integrasi butuh infra)
go test -race ./...              # wajib untuk kode konkuren; seluruh suite lulus
TIXGO_SKIP_INTEGRATION=1 go test ./...   # hanya unit
OPENAPI_UPDATE=1 go test ./internal/apitest -run TestOpenAPIMatchesRouter   # perbarui api/openapi.yaml
golangci-lint run                # 0 temuan
govulncheck ./...                # 0 kerentanan terpanggil
```
Daftar tes lengkap: [`../../docs/references/TEST-MATRIX.md`](../../docs/references/TEST-MATRIX.md) (dibangkitkan: `python3 scripts/gen_docs.py tests`). Pemetaan T01–T49 → tes: `../../docs/13-CONFIG-DEPLOY-TESTING.md` §5.

## Harness integrasi (`internal/apitest`)
Satu paket, berjalan berurutan. Tiap tes memanggil `NewEnv(t)`: DB `tixgo_test` dibuat ulang dari migrasi saat paket mulai dan **di-TRUNCATE** tiap tes, Redis DB 15 di-flush, `Clock` palsu (`e.Clock.Advance`), gateway `payment.Fake` (`e.Gateway.Settle(ref, metode)` menghasilkan body webhook bertanda tangan benar), mailer/SMS/storage in-memory, job `Inline` (langsung dijalankan; kegagalan terkumpul di `e.Jobs.Errors`).

Helper yang sering dipakai: `e.Verified(nama)` (pembeli siap), `e.UserWithRole(role, nama)` (admin/finance/support/editor sudah ber-MFA), `e.NewOrganizer(true)` (organizer disetujui + MFA), `e.publishedGA(harga, kuota)` / `e.publishedSeated(baris, kursi)` (event terbit dengan fase publik), `e.buyPaid(user, slug, tier, qty, metode)` (hold→order→bayar→webhook), `e.ledgerBalanced()`, `e.Exec/QueryInt/QueryStr` (SQL langsung), `c.IdemPOST`, `c.Upload`.

## Jebakan yang pernah terjadi
- **Jangan menjalankan dua proses `go test` untuk `internal/apitest` bersamaan** (mis. satu di background): keduanya memakai DB `tixgo_test` + Redis DB 15 yang sama dan saling `TRUNCATE`. Gejalanya acak: `deadlock detected (SQLSTATE 40P01)`, `404 pesanan tidak ditemukan`, `no rows in result set`. Terukur 2026-11-10: dua proses paralel → 9 tes gagal; satu proses sendirian → 146 lulus, 0 gagal.
- Memajukan jam palsu melewati TTL JWT → `TOKEN_EXPIRED`: login ulang (`e.Login(email)`); jangan `EnableMFA` dua kali pada akun yang sama.
- Field JSON ber-underscore (`in_transit`, `discount_total`) tidak cocok dengan field Go tanpa tag `json:"…"` di struct tes.
- Kunci tick antrean memakai TTL waktu nyata: hapus `wr:<event>:tick` sebelum tick berikutnya (`e.tickQueue`).
- `statuscode 401 TOKEN_EXPIRED` setelah `Clock.Advance` pada klien lama bukan bug.

## Aturan
- Database dan Redis **nyata** untuk integrasi; fake hanya untuk gateway/mailer/jam. Nama tes = perilaku. Tulis tes gagal dulu sebelum memperbaiki bug.
- Fitur uang/stok tidak dinyatakan selesai sebelum tes konkurensi + `ledgerBalanced()` hijau di bawah `-race`.
- Lint G101 berlaku di tes: jangan menaruh `user:pass@` dalam string URL tes (gunakan env).

## Uji beban & latihan
`scripts/k6/onsale.js` + `scripts/k6/verify-onsale.sh` (tidak ada oversell; ledger seimbang) dan `scripts/backup-restore-drill.sh` — lihat `07-TOOLING.md`.

## Sumber
`internal/apitest/*`, `../../docs/13-CONFIG-DEPLOY-TESTING.md`.
