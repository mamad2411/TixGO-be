# Backend Docs — Index

Dokumen **khusus repo backend** (cara kerja kode di sini). Dokumen produk/kontrak lengkap ada di `../docs/` (BRD, PRD, FRD, API, DB, state machine, security).

| Dokumen | Isi |
|---|---|
| [01-ARCHITECTURE](01-ARCHITECTURE.md) | Lapisan, alur request, peta paket, apa yang sudah/belum ada |
| [02-CONVENTIONS](02-CONVENTIONS.md) | Aturan kode Go: error, context, transaksi, uang, ID, envelope |
| [03-ENV](03-ENV.md) | Registry env sesuai `internal/config/config.go` |
| [04-TESTING](04-TESTING.md) | Cara menulis & menjalankan test, tes regresi wajib |
| [05-RUNBOOK](05-RUNBOOK.md) | Setup lokal, port, migrasi, troubleshooting |
| [06-CONTRACT](06-CONTRACT.md) | Cara menjaga kontrak API/DB tetap sinkron |
| [07-TOOLING](07-TOOLING.md) | Tools, library ter-pin, perintah, hooks, CI |
| [08-FLOWS](08-FLOWS.md) | **Alur per fitur** (sequence/state diagram): beli, refund, ledger+payout, gate, transfer, resale, squad, batal event, realtime |
| [09-DATA-SECURITY-SLO](09-DATA-SECURITY-SLO.md) | **Retensi data per tabel, model ancaman BE, usulan SLO/RPO/RTO** |
| [10-GLOSSARY-ADR](10-GLOSSARY-ADR.md) | **Glosarium istilah + indeks keputusan arsitektur (ADR)** |
| [GAP-BE](GAP-BE.md) | **Audit celah backend**: temuan terverifikasi, status (tutup/terbuka), butuh keputusan siapa |
| [`../../docs/references/CATALOG.md`](../../docs/references/CATALOG.md) | Katalog dibangkitkan: kode error+status, loop worker, job, kejadian realtime, akses per route, enum status, knob waktu |

Urutan baca agent: `../AGENTS.md` → `../docs/14-AGENT-PLAYBOOK.md` → file ini sesuai tugas.

## Sumber
Kode di repo ini (dibaca langsung) dan `../docs/05-STRUKTUR.md`, `../docs/13-CONFIG-DEPLOY-TESTING.md`.
