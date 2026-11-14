# `internal/platform/`

Klien infrastruktur: Postgres (pgxpool + ping), Redis, helper transaksi `WithTx`, deteksi error PG (`IsUniqueViolation`, `IsRetryable`). `deps_pinned.go` (tag `tools`) menyimpan dependensi terpilih yang belum diimpor kode.

- **Status**: selesai
- **Dokumen**: 05-STRUKTUR (di `../../../docs/`)
