# 03 — Environment

Daftar lengkap & default (dibangkitkan dari `internal/config/config.go`): [`../../docs/references/ENV-GENERATED.md`](../../docs/references/ENV-GENERATED.md). Contoh nilai: `.env.example`. Wajib hilang → proses **gagal start** menyebut variabelnya (diuji). Salin `.env.example` → `.env` (git-ignore).

## Wajib
`DATABASE_URL`, `REDIS_ADDR`, `CORS_ORIGINS`, `JWT_SECRET` (≥32), `ENC_KEY` (64 hex), `S3_ENDPOINT/BUCKET/ACCESS_KEY/SECRET_KEY`. Production tambahan: `MIDTRANS_SERVER_KEY`, token metrik.

## Pembayaran
| Variabel | Catatan |
|---|---|
| `MIDTRANS_SERVER_KEY` `MIDTRANS_CLIENT_KEY` `MIDTRANS_ENV` `MIDTRANS_SNAP_URL` `MIDTRANS_CORE_URL` | Midtrans (sandbox/production; URL override untuk tes). Wajib di production **hanya bila** `PAYMENT_GATEWAY=midtrans` |
| `XENDIT_SECRET_KEY` `XENDIT_CALLBACK_TOKEN` `XENDIT_BASE_URL` | Xendit (Invoice API). Start gagal bila `PAYMENT_GATEWAY=xendit` tanpa secret dan callback token |
| `PAYMENT_GATEWAY` | `midtrans` (default), `xendit`, atau `fake` — simulasi, endpoint `POST /api/v1/dev/payments/settle`; **dilarang di production** |
| `PAYOUT_MODE` | `manual` (keputusan Q12) · `PAYOUT_HOLD_DAYS` · `PAYOUT_MIN_AMOUNT` |
| `RESALE_FEE_BP` | biaya layanan pembeli resale (bp, default 500) |

## Keamanan operasional
`RATE_LIMIT_DISABLED` (uji beban saja; dilarang di production) · `WEBHOOK_ALLOW_PRIVATE` (dev/tes saja; diabaikan di production) · `TRUSTED_PROXIES` · `METRICS_TOKEN` · `RETENTION_NOTIFICATIONS_DAYS` (≥30) · `PRIVACY_CONTACT_EMAIL`.

## Lain-lain
`VAPID_PUBLIC_KEY/PRIVATE_KEY/SUBJECT` (web push; kosong = push dimatikan), `SEO_SITE_URL`, `SEO_INDEX_HTML` (index.html hasil build FE untuk disuntik meta+JSON-LD), `PUBLIC_WEB_URL/PUBLIC_API_URL`, `WORKER_CONCURRENCY`, `TICKET_MAX_TRANSFERS`, `QUEUE_*`, `HOLD_TTL_SEC`, `TICKET_QR_STEP_SEC`, `SMTP_*`.

Generate secret: `openssl rand -hex 32`.

## Port dev TixGo
Sengaja bukan default: API 8081, Postgres 5433, Redis 6380, MinIO 9100/9101 (voralens-v2 memakai port default di mesin yang sama — jangan dimatikan).

## Daftar lengkap
Tabel di atas hanya yang perlu penjelasan. Seluruh variabel (jenis wajib/opsional + default) ada di [`../../docs/references/ENV-GENERATED.md`](../../docs/references/ENV-GENERATED.md) (dibangkitkan dari `config.go`); `.env.example` memuat semuanya (yang opsional dikomentari). Waktu/batas/retensi: [`CATALOG.md` §6](../../docs/references/CATALOG.md).

## Sumber
`internal/config/config.go`, `internal/config/config_test.go`, `.env.example`.
