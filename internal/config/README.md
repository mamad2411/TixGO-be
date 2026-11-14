# `internal/config/`

Loader env + validasi. Wajib hilang / secret lemah / kombinasi berbahaya di production (`PAYMENT_GATEWAY=fake`, `RATE_LIMIT_DISABLED`) = gagal start. Env baru: tambah ke `config.go`, `.env.example`, `docs/13 §2`, dan tes.

- **Status**: selesai
- **Dokumen**: 13 §2 (di `../../../docs/`)
