# `internal/handler/`

Route Gin per modul (`mountXxx` di `handler.go`): bind/validasi → service → envelope `{data,meta}` / `{error}`. Tanpa logika bisnis. Menambah route: pastikan tidak bentrok (cek `grep` — path ganda = panic saat start) lalu jalankan `OPENAPI_UPDATE=1 go test ./internal/apitest -run TestOpenAPIMatchesRouter`.

- **Status**: selesai (seluruh modul M1–M48 di level API)
- **Dokumen**: `../../api/openapi.yaml` (dibangkitkan), 08 (di `../../../docs/`)
