# `internal/app/`

Merakit dependensi produksi (Postgres, Redis, MinIO, asynq, SMTP, gateway pembayaran) dari `config.Config` menjadi `service.Services`. Dipakai `cmd/api`, `cmd/worker`, `cmd/seed`.

- **Status**: selesai
- **Dokumen**: 01-ARCHITECTURE (di `../../../docs/` atau `../../docs/`)
