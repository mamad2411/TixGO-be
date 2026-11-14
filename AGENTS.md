# AGENTS.md — TixGo Backend

Go + Gin, PostgreSQL, Redis, asynq, melody, MinIO. Repo **terpisah** dari `../frontend`.

## Baca dulu
1. `docs/00-INDEX.md` (dokumen repo ini)
2. `../docs/14-AGENT-PLAYBOOK.md` (modul → dokumen) dan `../docs/06-FRD.md` (acceptance criteria)
3. Kontrak: `../docs/08-API-CONTRACT.md`, `../docs/09-DB-SCHEMA.md`, `../docs/11-STATE-MACHINES.md`

## Gerbang rilis publik
`../docs/19-GAP-AUDIT.md` §4 — alur beli saja **belum cukup** untuk rilis; Pro-Core (`../docs/20-MODUL-PRO.md`) wajib.

## Aturan inti
- Harga/limit dihitung server. Kursi/kuota hanya berubah dalam transaksi DB.
- POST order/bayar/refund wajib `Idempotency-Key`. Tenant/user difilter di SQL.
- Env baru → `config.go` + tes + `.env.example` + `docs/03-ENV.md` sekaligus. Route baru → perbarui `api/openapi.yaml` (`OPENAPI_UPDATE=1 …TestOpenAPIMatchesRouter`) dan `internal/apidoc` (skema inti). Modul baru → daftarkan di `service/services.go`, tambah tes apitest, jalankan `scripts/gen_docs.py`.
- Status modul & celah jujur: `../docs/references/STATUS-BE.md`; checklist go-live: `../docs/references/GO-LIVE.md`.
- Fail loud; tanpa fallback diam-diam. Tanpa secret/PII di log.
- Selesai = `make -C .. preflight` hijau + `make -C .. vuln` tanpa temuan yang dipanggil kode + AC FRD terpenuhi. Tools: `docs/07-TOOLING.md`.

## Skill (`.claude/skills` → `../skills`)
tixgo-gin-api · tixgo-seat-hold · tixgo-queue · tixgo-payment · tixgo-ticket-gate · tixgo-realtime · tixgo-rbac-security · tixgo-testing · tixgo-deploy · tixgo-finance · tixgo-privacy-legal · tixgo-seo-content · tixgo-qa-check
