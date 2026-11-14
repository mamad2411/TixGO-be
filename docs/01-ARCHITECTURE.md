# 01 — Arsitektur Backend

## Lapisan (impor satu arah)
```
cmd/api ─► server (Gin: middleware global) ─► handler (HTTP tipis, mountXxx per modul)
                                                  │
                                                  ▼
                                    service  (aturan bisnis, otorisasi, transaksi, audit, outbox)
                                                  │
                      ┌───────────────────────────┼─────────────────────────────┐
                      ▼                           ▼                             ▼
          repository/sqlcgen (SQL statis)   payment / notify / storage     Redis (cache, antrean, bus)
                      │
                      ▼
              platform (pgx pool, WithTx)
```
- `handler`: bind/validasi → service → envelope. Tanpa SQL/aturan bisnis.
- `service`: satu berkas ≈ satu modul; container `Services` (`services.go`) merakit semuanya. Transaksi lewat `InTx/InTxRaw`; efek samping (email, cache, webhook, job) dikerjakan **setelah commit**; kejadian yang harus pasti ditulis ke **outbox** dalam transaksi yang sama.
- `repository/sqlcgen`: hasil `sqlc generate` dari `queries/*.sql`. Query dinamis hanya lewat allowlist (`dynsql.go`) atau SQL mentah berparameter.

## Proses
| Binary | Fungsi |
|---|---|
| `cmd/api` | HTTP + WebSocket (hub dijalankan di proses ini) |
| `cmd/worker` | server asynq (7 job: `webhook:deliver`, `notify:send`, `refund:process`, `event:cancel_fanout`, `broadcast:send`, `follow:notify`, `privacy:export`) + 19 loop periodik (`service/sched.go`) dengan kunci Redis per loop sehingga aman dijalankan >1 replika |
| `cmd/seed` | data demo (`-load N` = N pengguna uji beban); ditolak di production |
| migrasi | CLI `migrate` (`make migrate-up`) |

Loop periodik (daftar otoritatif + fungsi: [CATALOG §2](../../docs/references/CATALOG.md)): antrean tick (2 dtk), relay outbox (0,5 dtk), expire hold (5 dtk) & order (10 dtk), flush funnel (10 dtk), publish terjadwal (30 dtk), event selesai/CMS/pengingat/rekonsiliasi pembayaran/proses refund/sweep batal-event/squad/resale (1 mnt), rilis settlement (5 mnt), sweep support (10 mnt), purge privasi + eskalasi refund telat + pengingat hari-H (1 jam). Purge juga membuang outbox terbit >1 hari.

## Invarian yang tidak boleh dilanggar
1. **Stok hanya diputuskan DB.** Hold kursi: `SELECT … ORDER BY id FOR UPDATE` lalu `AcquireSeats` (tanpa deadlock, tanpa hold parsial); GA: `UPDATE tiers … WHERE sold+held+qty <= quota`. Cache Redis (availability 1 dtk) hanya untuk tampilan.
2. **Uang = ledger berganda seimbang** (constraint trigger), posting idempoten `(ref_type, ref_id)`; saldo selalu hasil agregasi.
3. **Idempotensi di tepi**: `Idempotency-Key` pada POST uang; webhook gateway idempoten per `provider_event_id`; scan gate idempoten per `scan_id`.
4. **Tenant isolation**: data milik organizer lain → 404; filter di service/SQL, bukan di UI.
5. **Gagal keras, bukan diam-diam**: gateway tak terkonfigurasi, jalur refund beralih ke manual, dsb. selalu eksplisit & tercatat (audit/recon).

## Gateway pembayaran
Antarmuka `payment.Gateway` (CreateTransaction, Status, Refund, VerifyNotification) dengan tiga implementasi: `midtrans`, `xendit` (Invoice API), `fake` (non-production). Dipilih lewat `PAYMENT_GATEWAY`; keputusan produk mana yang dipakai di 1.0: lihat `GAP-BE.md` G-01.

## Alur beli (ringkas)
```
join antrean ─► admit (token) ─► hold (DB) ─► order (harga server, kebijakan refund di-snapshot) ─► pay (Snap)
      ─► webhook (signature+nominal+Status API) ─► settleTx: stok held→sold, tiket, invoice, ledger, outbox ─► afterSettle (email, cache, webhook organizer)
```
Cabang: bayar-setelah-expire → terbitkan bila stok masih ada, jika tidak refund otomatis; pembayaran ganda → refund otomatis; resale & squad memakai pipeline order/payment yang sama.

## Alur request
```
Client ─► Gin ─► request-id ─► recover ─► access-log+metrik ─► secure ─► CORS ─► maintenance ─► [rate limit] ─► auth ─► role ─► [MFA] ─► [idempotency] ─► handler
```

## Readiness
`/readyz` memanggil `Ping` tiap dependensi (2 dtk): semua `up` → 200 else 503. `/healthz` = liveness. `/metrics` (token opsional): metrik HTTP + bisnis (`tixgo_*`). `GET /api/v1/status` = status layanan publik.

## Sumber
`internal/service/services.go`, `internal/service/sched.go`, `internal/handler/handler.go`, `../docs/04-WORKFLOW.md`, `../../docs/22-KEPUTUSAN-PEMBAYARAN-REFUND.md`.
