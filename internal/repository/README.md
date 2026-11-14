# `internal/repository/`

`sqlcgen/` = keluaran `sqlc generate` dari `../../queries/*.sql` + `../../sqlc.yaml`. Jangan diedit; ubah query lalu regenerate. Filter tenant/user ada **di SQL/service**. Query dinamis memakai allowlist (`service/dynsql.go`).

- **Status**: selesai
- **Dokumen**: 09, 12 §2 (di `../../../docs/`)
