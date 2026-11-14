# 10 — Glosarium dan Indeks Keputusan Arsitektur (ADR)

## 1. Glosarium (istilah yang dipakai di kode dan dokumen)
| Istilah | Arti | Di mana |
|---|---|---|
| Antrean / waiting room | urutan adil sebelum jual dibuka; urutan = sort(HMAC-SHA256(seed, userID)), seed di-commit lalu diungkap | `service/queue.go`, `GET /events/{slug}/queue/proof` |
| Admission token | izin sementara hasil admit; wajib untuk membuat hold | `QUEUE_ADMIT_WINDOW_SEC` |
| Hold | penahanan kursi/kuota sementara (`HOLD_TTL_SEC`, perpanjang sekali `HOLD_EXTEND_SEC`) | `service/hold.go` |
| GA | general admission: tier berkuota tanpa kursi bernomor | `tiers.sold/held/quota` |
| Tier | kategori tiket dengan harga dan kuota | `tiers` |
| Order / payment | pesanan pembeli / percobaan bayar ke gateway (satu order bisa >1 payment) | `orders`, `payments` |
| Settle | pelunasan: stok held→sold, tiket terbit, invoice, ledger, outbox (`settleTx`) | `service/payment.go` |
| Outbox | tabel kejadian yang ditulis dalam transaksi lalu disiarkan relay | `outbox`, `RelayOutbox` |
| Ledger | pembukuan double-entry; saldo selalu hasil agregasi | `service/ledger.go` |
| `org_pending` / `org_available` | saldo organizer sebelum / sesudah rilis settlement | `AcctOrgPending`, `AcctOrgAvailable` |
| `payment_clearing` | akun perantara uang masuk dari gateway | `PostOrderPaid` |
| `resale_payable` | utang platform ke penjual resale | `service/resale.go` |
| Settlement | rilis saldo event selesai + `PAYOUT_HOLD_DAYS` dari pending ke available | loop `settlement_release` |
| Payout | pencairan organizer: manual terkontrol (Q12) | `service/finance.go` |
| Recon (exception) | selisih antara gateway dan sistem, tiap jenis punya penanganan | `recon_exceptions` |
| DSR | data subject request: hak data pengguna (UU PDP) | `service/privacy.go` |
| KYB | verifikasi usaha organizer (dokumen + rekening) sebelum boleh jual berbayar | `service/organizer.go` |
| Gate | pintu masuk; staf memindai QR tiket | `service/gate.go` |
| QR dinamis | QR berputar (TOTP per tiket, langkah `TICKET_QR_STEP_SEC`) | `service/ticket.go` |
| Manifest | paket data offline untuk gate (secret TOTP per tiket), diaudit | `GateService.Manifest` |
| Squad | pembelian rombongan: ketua menahan N slot, tiap anggota bayar bagiannya | `service/squad.go` |
| Passport | gamifikasi poin dan lencana pembeli | `service/passport.go` |
| Kill switch / control room | jeda penjualan, laju admit, maintenance, pengumuman | `service/ops.go` |
| (T) | angka usulan, belum diputuskan atau diukur | dokumen |

## 2. Indeks keputusan (ADR ringkas)
Keputusan lengkap beserta alasan ada di dokumen sumber; tabel ini hanya indeks supaya mudah ditemukan. Status "terbuka" = butuh keputusan.

| ID | Keputusan | Tanggal | Status | Sumber |
|---|---|---|---|---|
| ADR-01 | Gateway utama: **Midtrans** (Snap) di balik `payment.Gateway`; Xendit tidak dipakai di 1.0 | 2026-10-04 | **bertentangan dengan kode/GO-LIVE** (Xendit sudah diimplementasi dan ditulis "aktif"); butuh konfirmasi — GAP-BE G-01 | docs/14 Q1, docs/22 §1 |
| ADR-02 | Refund default tidak dapat dikembalikan kecuali event batal; organizer boleh refundable per event; jalur refund ganda (gateway + manual) | 2026-10-04 | final | docs/14 Q3, docs/22 §2 |
| ADR-03 | SEO: handler Go menyuntik meta+JSON-LD, SPA Vue tetap | 2026-10-04 | final | docs/14 Q11, docs/19 §3 |
| ADR-04 | Pencairan manual terkontrol (pemohon ≠ penyetuju, MFA, bukti transfer), ledger siap otomasi | 2026-10-04 | final | docs/14 Q12 |
| ADR-05 | Pajak/PPN: kolom pajak ada, nilai selalu 0 sampai ada konsultan pajak | — | **terbuka (blocker go-live)** | docs/14 Q13 |
| ADR-06 | Login sosial ditunda (P1) | 2026-10-04 | final | docs/14 Q14 |
| ADR-07 | Retensi support mengikuti kebijakan privasi (`privacy.purge`) | 2026-10-04 | final di dokumen, **belum diimplementasi** — GAP-BE G-08 | docs/14 Q15 |
| ADR-08 | Stok hanya diputuskan DB; Redis hanya cache tampilan | — | final (invarian #1) | backend/docs/01 |
| ADR-09 | Uang = ledger berganda seimbang, posting idempoten | — | final (invarian #2) | backend/docs/01 |
| ADR-10 | Redis mati: peran istimewa 503, pembeli dan gate lanjut | — | final | backend/docs/05 |
| ADR-11 | Payout penjual resale manual oleh finance (`resale_payable`) | — | final | `service/resale.go` |
| ADR-12 | `backend/` dan `frontend/` repo terpisah; kontrak lewat OpenAPI yang dibangkitkan dari router | — | final | AGENTS.md |
| ADR-13 | Retensi audit log dan data pribadi | — | **terbuka (butuh hukum)** | docs/14 Q9 |
| ADR-14 | Struktur service fee, KYC KTP, wallet pass, provider WA/CAPTCHA, hosting/vendor, brand/domain | — | **terbuka** (Q2, Q4–Q6, Q8, Q10) | docs/14 §6 |

## Sumber
`../../docs/14-AGENT-PLAYBOOK.md` §6, `../../docs/19-GAP-AUDIT.md`, `../../docs/22-KEPUTUSAN-PEMBAYARAN-REFUND.md`, `internal/service/*.go`, `docs/01-ARCHITECTURE.md`.
