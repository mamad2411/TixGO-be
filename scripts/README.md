# `scripts/`

Skrip bantu.

- `k6/smoke.js` — health & readiness.
- `k6/onsale.js` — uji beban onsale (hold → order → bayar simulasi). Prasyarat: `PAYMENT_GATEWAY=fake RATE_LIMIT_DISABLED=true` (keduanya dilarang di production), `go run ./cmd/seed` lalu `go run ./cmd/seed -load 2000`.
- `k6/verify-onsale.sh` — verifikasi pasca uji: tidak ada oversell, tiket konsisten dengan `sold`, ledger seimbang.

Dokumen: 13 (di `../../docs/`).
