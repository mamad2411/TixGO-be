# 09 — Retensi Data, Model Ancaman, dan SLO (backend)

Status kejujuran: **fakta** = dibaca dari kode/migrasi (sumber disebut). **(T)** = usulan, belum disetujui; angka waktu retensi menunggu tinjauan hukum (docs/14 Q9, docs/20 M29, GO-LIVE).

## 1. Retensi data per tabel

### 1a. Yang sudah dibersihkan job (`privacy_purge`, tiap 1 jam — `PrivacyService.Purge`)
| Data | Aturan | Sumber |
|---|---|---|
| Berkas ekspor privasi | dihapus dari storage setelah kedaluwarsa (`PRIVACY_EXPORT_TTL`) | `privacy.go` Purge |
| `notifications` | > `RETENTION_NOTIFICATIONS_DAYS` (default 180, minimum 30 divalidasi saat start) | `config.go`, `queries/account.sql` |
| `email_tokens` | kedaluwarsa > 7 hari | `PurgeOldTokens` |
| `refresh_tokens` | kedaluwarsa > 30 hari | `PurgeOldRefreshTokens` |
| `idempotency_keys` | > 48 jam | `PurgeIdempotency` |
| `outbox` | terbit > 1 hari (interval di SQL, jam DB) | `DeleteOldOutbox` — **ditambahkan 2026-11-10**, sebelumnya kueri ada tetapi tidak pernah dipanggil |

Tiap purge yang menghapus baris menulis audit `privacy.purge`.

### 1b. Tidak ada job pembersih (tumbuh tanpa batas) — butuh keputusan masa simpan
| Tabel | Isi | Catatan risiko | Usulan (T) |
|---|---|---|---|
| `audit_logs` | jejak aksi staf/sistem | sengaja panjang; wajib untuk investigasi | simpan ≥ 2 tahun, lalu arsip dingin; keputusan Q9 |
| `gate_scans` | tiap scan, termasuk offline | tumbuh per tiket × percobaan | simpan sampai event selesai + 90 hari, lalu agregasi |
| `webhook_deliveries` | log pengiriman webhook organizer (bisa memuat payload) | berisi data peserta | 30 hari |
| `funnel_daily` | agregat harian | kecil | simpan |
| `payment_events` | event mentah gateway | bukti sengketa | simpan selama masa sengketa/pajak (konsultan pajak Q13) |
| `support_messages`, `support_tickets` | pesan pelanggan, bisa PII | **keputusan Q15 bilang "ikut kebijakan privasi, dijalankan `privacy.purge`" tetapi kode hanya MENUTUP tiket (`Support.Sweep`), tidak menghapus/anonimkan berdasarkan umur** | hapus/anonimkan 12 bulan setelah ditutup |
| `export_jobs` (baris) | berkas dihapus, baris tetap | kecil | 90 hari |
| `event_sessions`, `event_photos` | konten event | milik organizer | ikut event |
| `consents`, `cookie_consents`, `dsr_requests`, `privacy_complaints` | bukti kepatuhan | wajib bisa dibuktikan | simpan sesuai saran hukum |
| `users` terhapus | anonimisasi (bukan DELETE) | transaksi tetap utuh untuk akuntansi | sudah sesuai `TestAccountDeletionAnonymizesButKeepsTransactions` |

## 2. Model ancaman backend (ringkas)
Hanya kontrol yang **terverifikasi di kode/tes**. Kolom "Belum" = celah/uji yang belum ada bukti.

| Ancaman | Kontrol terverifikasi | Bukti | Belum |
|---|---|---|---|
| Oversell / race kursi | stok diputuskan DB (`FOR UPDATE`, `UPDATE … WHERE sold+held+qty<=quota`) | k6 onsale 3× kuota: 0 oversell; tes konkurensi | uji ≥10× kapasitas di infra mirip produksi |
| Pembayaran ganda / webhook palsu | idempotensi per `provider_event_id`; signature+nominal+Status API; refund otomatis bila ganda | `apitest` webhook ganda/palsu | callback Xendit nyata (`XENDIT_CALLBACK_TOKEN`) belum diuji |
| Uang tidak seimbang | ledger double-entry, constraint trigger, posting idempoten | `TestLedgerClearingAgreesWithOrdersAndRefunds` | — |
| Pencairan curang | pemohon ≠ penyetuju; MFA; min payout; rekening baru ada masa tunggu | `finance.go` | pen-test independen |
| Akses lintas tenant | data organizer lain → 404, filter di service/SQL | `TestAuditCustomerCannotReachPrivilegedRoutes` | — |
| Pencurian sesi | access JWT pendek (default 15 m), refresh berotasi (reuse = cabut keluarga), cookie HttpOnly | `TestRefreshCookieAttributes` | — |
| Brute force | lockout akun (`ACCOUNT_LOCKED` 429), OTP maks percobaan, rate limit GCRA per rute | `auth.go`, `middleware` | tanpa CAPTCHA (Q6 = tidak) |
| SSRF lewat webhook organizer | alamat privat/http ditolak kecuali `WEBHOOK_ALLOW_PRIVATE` (diabaikan di production) | `integration.go`, 03-ENV | — |
| SVG berbahaya (denah) | allowlist `svgsafe` (tanpa script/handler/foreignObject) | `svgsafe_test.go` | — |
| Formula injection CSV | `csvSafe` pada ekspor peserta | `attendee.go:73` | ekspor XLSX settlement belum diperiksa |
| Konfigurasi berbahaya | start ditolak: tanpa `METRICS_TOKEN`, `PAYMENT_GATEWAY=fake`, `RATE_LIMIT_DISABLED`, secret lemah, Midtrans tanpa kunci | `config.go:248-262` | — |
| Kebocoran PII di log | konvensi tanpa secret/PII | `02-CONVENTIONS` | tidak ada tes otomatis yang memindai log |
| XFF spoof | `SetTrustedProxies(cfg.TrustedProxies)` | `02-CONVENTIONS` | `TRUSTED_PROXIES` produksi belum ditetapkan (GO-LIVE) |
| Body raksasa / slowloris | `BodyLimit` global (JSON 1 MiB, multipart 12 MiB → 413) + `ReadTimeout 30s`, `WriteTimeout 60s`, `IdleTimeout 120s`, `MaxHeaderBytes 64 KiB` | `TestOversizedJSONBodyRejectedWith413` | tes beban koneksi lambat (slowloris) belum ada |
| Enkripsi kolom | AES-GCM `ENC_KEY` 64 hex, kolom `*_enc` | `auth/crypto.go` | rotasi `ENC_KEY` butuh skrip re-enkripsi — **skrip belum ada** |

## 3. SLO / RPO / RTO — usulan (T)
Belum ada target tertulis (GO-LIVE: "target RPO/RTO (T) ditetapkan"). Usulan berdasar pengukuran yang ada:

| Indikator | Usulan (T) | Dasar pengukuran |
|---|---|---|
| Ketersediaan API saat onsale | 99,9 % dalam jendela onsale | belum diukur |
| `POST /holds` p95 | ≤ 250 ms (ukuran dev: ≈ 63 ms) | k6 `onsale.js`, GO-LIVE |
| Error 5xx onsale | < 0,1 % (ukuran dev: 0) | k6 |
| Oversell | 0 (mutlak) | invarian #1 |
| Antrean: dari admit ke token terkirim | ≤ 5 dtk | loop `queue_tick` 2 dtk |
| Outbox → WS | p95 ≤ 2 dtk (relay 0,5 dtk) | metrik `pending_outbox` |
| Webhook pembayaran → tiket terbit | p95 ≤ 10 dtk; fallback `payment_reconcile` ≤ 1 mnt | loop 1 mnt |
| RPO | ≤ 5 menit (PITR/WAL archiving) — **belum dikonfigurasi**; drill sekarang hanya dump | `backup-restore-drill.sh` |
| RTO | ≤ 60 menit (ukur lewat drill; angka dev belum dicatat di dokumen) | drill |

## Sumber
`internal/service/privacy.go`, `queries/*.sql`, `migrations/*.up.sql`, `internal/config/config.go`, `internal/service/{finance,integration,attendee,support}.go`, `internal/auth/crypto.go`, `../docs/references/GO-LIVE.md`, `../../docs/14-AGENT-PLAYBOOK.md` §6, `../../docs/19-GAP-AUDIT.md` §3.
