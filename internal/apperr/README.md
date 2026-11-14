# `internal/apperr/`

Error terstandar API: `*Error{Status, Code, Message, Details}`; handler memetakan ke `{"error":{code,message,details,request_id}}`, error lain jadi 500 tanpa bocor detail. Katalog kode+status: `docs/references/CATALOG.md` §1.

- **Status**: selesai
- **Dokumen**: 08 §2 (di `../../../docs/` atau `../../docs/`)
