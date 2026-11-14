# `internal/clock/`

Abstraksi waktu (`Clock`; implementasi `clock.Real` dan `clock.NewFake`) agar TTL, T0 antrean, dan jendela refund bisa diuji. Kode bisnis memakai `s.now()`, bukan `time.Now()`.

- **Status**: selesai
- **Dokumen**: 02-CONVENTIONS (di `../../../docs/` atau `../../docs/`)
