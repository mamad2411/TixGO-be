# `internal/apitest/`

Tes integrasi seluruh API: Postgres (`tixgo_test`) dan Redis (DB 15) nyata dari `make infra`; gateway, email, SMS, storage, dan jam diganti palsu. **Jangan jalankan dua `go test` apitest bersamaan** — satu DB dipakai bersama (deadlock/404 acak). Daftar tes: `docs/references/TEST-MATRIX.md`.

- **Status**: selesai
- **Dokumen**: 13, 04-TESTING (di `../../../docs/` atau `../../docs/`)
