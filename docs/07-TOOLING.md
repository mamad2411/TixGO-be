# 07 — Tooling Backend

Rincian lengkap & bukti: `../../docs/21-TOOLING.md`. Ringkas untuk repo ini:

| Kebutuhan | Perintah (dari root `tixGo/`) | Catatan |
|---|---|---|
| Hot reload | `make dev-be-watch` | `.air.toml` |
| Lint + vet + test | `make preflight` | golangci-lint 2.15 (2.13.x tidak kompatibel dengan Go 1.27.3); 0 temuan |
| Scan kerentanan | `make vuln` | govulncheck: 0 terpanggil. Satu temuan modul `x/crypto/openpgp` (GO-2026-5932, tanpa perbaikan) tidak diimpor kode kita |
| Seed demo | `make seed` / `go run ./cmd/seed -load 2000` | dilarang di production; `-load` membuat pengguna uji beban |
| Uji beban smoke | `make load-smoke` | `scripts/k6/smoke.js` |
| Uji beban onsale | `k6 run -e USERS=600 -e VUS=150 scripts/k6/onsale.js` lalu `DATABASE_URL=… scripts/k6/verify-onsale.sh` | butuh `PAYMENT_GATEWAY=fake RATE_LIMIT_DISABLED=true`; hasil 3× kuota: kuota 200 terjual tepat 200, hold p95 ≈ 63 ms, order p95 ≈ 360 ms, 0 error 5xx (mesin dev) |
| Latihan restore | `scripts/backup-restore-drill.sh` | lihat runbook |
| Dokumen turunan | `python3 scripts/gen_docs.py [api env schema tests]` | API-ROUTES, ENV, DB-SCHEMA, TEST-MATRIX di `../docs/references/` |
| Kontrak | `OPENAPI_UPDATE=1 go test ./internal/apitest -run TestOpenAPIMatchesRouter` | |
| Hooks | `make hooks` | `lefthook.yml` |
| Image | `make docker-build` | multi-stage, non-root uid 10001, ±166 MB (api + worker) |
| Lint kontrak FE | `cd ../frontend && pnpm api:lint` | `redocly.yaml` di repo ini |

## Pustaka penting (dipilih & terverifikasi)
gin, pgx/v5, sqlc, golang-migrate, go-redis (+redis_rate), asynq, minio-go, melody + gorilla/websocket, webpush-go, golang-ical, excelize, fpdf, goldmark + bluemonday, kin-openapi (hanya untuk pembangkit/validator OpenAPI), prometheus client, pquerna/otp, golang-jwt, argon2 (x/crypto). `internal/platform/deps_pinned.go` (tag `tools`) menyimpan dependensi terpilih yang belum diimpor.

## Aturan
- Pustaka baru: cek ada/terawat dulu (`go list -m -versions`), `go mod tidy` setelahnya.
- Temuan govulncheck yang dipanggil kode = blokir merge; yang tidak dipanggil dicatat di 21 §7.
- macOS: `sed -i ''` (BSD) dan tanpa `\b`; edit multi-baris pakai skrip.

## Sumber
`../../docs/21-TOOLING.md`, `.air.toml`, `lefthook.yml`, `scripts/`, `.github/workflows/ci.yml`.
