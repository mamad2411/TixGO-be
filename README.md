# TixGo — Backend

Go + Gin. Repo ini TERPISAH dari `../frontend`; kontrak hanya `api/openapi.yaml`.
Dokumen: `../docs/` (mulai `00-INDEX.md`). Status: **fitur M1–M48 sudah ada di level API dan teruji** (lihat `../docs/references/STATUS-BE.md`; yang belum + risiko jujur di sana). Frontend belum.

## Versi (terverifikasi 2026-10-04)
Go 1.27.3 · gin v1.12.x · pgx/v5 v5.11.0 · go-redis/v9 · prometheus client_golang · golang-migrate v4.20.1 · sqlc v1.31.1 · golangci-lint 2.13.2
(versi persis: `go.mod`)

## Jalankan
```
cp .env.example .env     # isi JWT_SECRET & ENC_KEY: openssl rand -hex 32
make -C .. infra         # pg, redis, minio
make -C .. migrate-up && make -C .. seed     # skema + data demo (cetak rahasia TOTP)
set -a; source .env; set +a
PAYMENT_GATEWAY=fake go run ./cmd/api        # :8081 — bayar simulasi: POST /api/v1/dev/payments/settle
go run ./cmd/worker                          # antrean tick, expire, rekonsiliasi, outbox, dll.
```
## Cek
`go build ./... && go vet ./... && golangci-lint run && go test -race ./... && govulncheck ./...`
Migrasi: `migrate -path migrations -database "$DATABASE_URL" up|down 1` · Kontrak: `OPENAPI_UPDATE=1 go test ./internal/apitest -run TestOpenAPIMatchesRouter` · Dokumen turunan: `python3 scripts/gen_docs.py`
