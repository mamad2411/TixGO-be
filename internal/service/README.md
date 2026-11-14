# `internal/service/`

Aturan bisnis, otorisasi, transaksi (`InTx`/`InTxRaw`), audit, outbox (`emit`) & bus (`publish`). Harga dihitung di sini (`pricing.go`). `services.go` = container; urutan konstruktor penting (mis. `Invoice`/`Refund` sebelum `Order`/`Payment`). Satu berkas ≈ satu modul.

- **Status**: selesai
- **Dokumen**: 06-FRD, 11, 20, 22 (di `../../../docs/`)
