# `internal/notify/`

Mailer (SMTP/LogMailer/Memory), SMS, Web Push (VAPID), katalog templat id/en (`templates.go`). Pengiriman lewat job `notify:send` dengan retry; preferensi per kategori dihormati di `service/notifier.go`.

- **Status**: selesai
- **Dokumen**: 06 M13, 20 M42 (di `../../../docs/`)
