# 05 — Runbook

## Lokal
```
make -C .. infra                       # pg:5433, redis:6380, minio:9100
cp .env.example .env                   # isi JWT_SECRET & ENC_KEY: openssl rand -hex 32
make -C .. migrate-up
make -C .. seed                        # data demo (cetak rahasia TOTP admin/finance/organizer — simpan)
make -C .. dev-be                      # API :8081      |  make -C .. dev-worker  # worker
curl localhost:8081/healthz && curl localhost:8081/readyz
```
Tanpa kunci Midtrans: `PAYMENT_GATEWAY=fake`, lalu bayar dengan `POST /api/v1/dev/payments/settle {"order_id":…}`. Akun demo: `admin|finance|support|editor|organizer|rani|bimo|sari@tixgo.local`, kata sandi `Demo-Pass-123!`.

## Migrasi
`migrate -path migrations -database "$DATABASE_URL" up|down 1`. Pasangan `.up/.down` wajib; diuji up → down -all → up. Setelah mengubah `queries/*.sql`: `sqlc generate`.

## Backup & restore
`scripts/backup-restore-drill.sh` (dump → restore ke DB sementara → bandingkan baris semua tabel → invarian ledger → ukur RTO). Jadwalkan mingguan di staging; target RPO/RTO ditetapkan tim (T). Bila `pg_dump` lokal ≠ versi server: `PG_CONTAINER=tixgo-pg-1`.

## Insiden (M44 AC6)
| Gejala | Tindakan |
|---|---|
| **Onsale macet / lambat** | Control room `GET /admin/ops/events/{id}/control-room` (antrean, hold aktif, p95, 5xx). Turunkan `PUT /admin/ops/events/{id}/admit-rate`; bila perlu **jeda penjualan** `POST /admin/ops/events/{id}/pause` (efek instan; posisi antrean utuh). Periksa `outbox` menumpuk (`pending_outbox`) → worker hidup? |
| **Gateway turun** | Pesanan tetap pending; `payment_reconcile` (1 mnt) mengecek status ke gateway. Jeda penjualan + pengumuman `POST /admin/ops/announcements` (level `incident`) → muncul di `/status`. Jangan mengubah status order manual. |
| **Redis hilang** | Stok tetap aman (keputusan di DB). Antrean/posisi hilang → umumkan, buka ulang antrean. Peran istimewa mendapat 503 sampai Redis pulih (disengaja); pembeli & gate tetap jalan. |
| **Webhook tak masuk / selisih** | `GET /admin/finance/recon` + impor laporan gateway `POST /admin/finance/recon/import`; tiap pengecualian punya jenis (amount_mismatch, status_mismatch, paid_after_expire_*, duplicate_payment, resale_on_cancelled_event). |
| **Salah harga** | Jeda penjualan → perbaiki tier (harga lama pada order lama tidak berubah) → lanjutkan. Order yang sudah lunas: refund lewat finance. |
| **Kebocoran data** | Cabut sesi massal (blokir/blacklist + ganti `JWT_SECRET` = semua token tidak berlaku), putar `ENC_KEY` hanya dengan prosedur re-enkripsi terencana, ikuti prosedur notifikasi UU PDP (hukum), audit akses: tabel `audit_logs` (`admin.user_view`, `support.view`, `attendees.export`). |
| **Maintenance terencana** | `PUT /admin/ops/maintenance {"on":true,"message":"…","eta":"30 menit"}` — publik 503 `MAINTENANCE`; webhook, `/status`, login & admin tetap hidup. |
| **Event dibatalkan** | `POST /organizer/events/{id}/cancel` (owner+MFA): refund penuh otomatis massal (idempoten), notifikasi semua pemegang tiket; `cancel_sweep` menangkap bayar-setelah-batal. Refund jalur manual menunggu rekening pembeli (tenggat → eskalasi support). |

## Rotasi rahasia
Terjadwal (T): `JWT_SECRET` (memutus semua sesi; refresh cookie tak valid → login ulang), kunci Midtrans, token metrik, SMTP. `ENC_KEY` **tidak** diganti tanpa skrip re-enkripsi kolom `*_enc`.

## Troubleshooting
| Gejala | Penyebab & tindakan |
|---|---|
| `port is already allocated` saat `make infra` | proyek lain memakai port itu (voralens-v2 di 5432). Port TixGo sudah dibedakan; set `TIXGO_PG_PORT` dll. dan samakan `DATABASE_URL`. **Jangan** mematikan stack proyek lain. |
| API log `bind: address already in use` | port `HTTP_ADDR` dipakai proses lain; cek `lsof -nP -iTCP:8081 -sTCP:LISTEN` |
| Start gagal "env X wajib diisi" / "dilarang di production" | `.env` belum dimuat atau kombinasi berbahaya (fake gateway, rate limit mati, tanpa `METRICS_TOKEN`) |
| `/readyz` 503 | lihat `checks`; `down` = dependensi itu tak terjangkau |
| `panic: handlers are already registered for path` | route ganda di handler — `grep` path sebelum menambah |
| `pg_dump: server version mismatch` | `PG_CONTAINER=tixgo-pg-1` pada skrip drill |

## Sumber
`../Makefile`, `../docker-compose.dev.yml`, `internal/service/sched.go`, `../../docs/22-KEPUTUSAN-PEMBAYARAN-REFUND.md`.
