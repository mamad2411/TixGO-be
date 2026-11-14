# 06 — Menjaga Kontrak

Kontrak adalah satu-satunya jembatan ke repo frontend.

| Artefak | Lokasi | Sifat |
|---|---|---|
| OpenAPI mesin-baca | `api/openapi.yaml` | **dibangkitkan dari router nyata** (`internal/apidoc`), dijaga tes `TestOpenAPIMatchesRouter` |
| Daftar route + akses | `../docs/references/API-ROUTES.md` | dibangkitkan dari openapi (`python3 scripts/gen_docs.py api`) |
| Konvensi & perilaku | `../docs/08-API-CONTRACT.md` | ditulis tangan |
| Skema DB | `migrations/` + `../docs/references/DB-SCHEMA-GENERATED.md` | migrasi = sumber; dokumen dibangkitkan (`gen_docs.py schema`) |
| WS | `../docs/08-API-CONTRACT.md` §4 | ditulis tangan |

## Aturan perubahan
1. Ubah kode + migrasi, lalu `OPENAPI_UPDATE=1 go test ./internal/apitest -run TestOpenAPIMatchesRouter` dan `python3 scripts/gen_docs.py` — semuanya di commit yang sama. CI gagal bila openapi tidak sinkron dengan router.
2. Skema request/response endpoint inti diturunkan dari **struct Go** yang didaftarkan di `internal/apidoc/apidoc.go` (`Docs`); tambahkan entri baru untuk endpoint baru agar FE mendapat tipe, bukan objek generik.
3. Perubahan merusak (hapus/ganti nama field, ubah makna) → versi path baru atau masa kompatibilitas; beri tahu FE sebelum merge.
4. Frontend membuat tipe dari `openapi.yaml` (`pnpm api:gen`); bila generate gagal, kontraknya yang salah.
5. Kode error baru ditambahkan di `internal/apperr` dan terdaftar otomatis di dokumen 08 saat dibangkitkan ulang.

## Sumber
`api/openapi.yaml`, `internal/apidoc`, `../docs/08-API-CONTRACT.md`.
