#!/usr/bin/env bash
# Verifikasi pasca uji beban: tidak ada oversell & invarian inventori/ledger terjaga. Keluar non-nol bila ada pelanggaran.
# Pakai: DATABASE_URL=postgres://... scripts/k6/verify-onsale.sh
set -euo pipefail
: "${DATABASE_URL:?set DATABASE_URL}"
q() { psql "$DATABASE_URL" -Atc "$1"; }
fail=0
echo "== tier melebihi kuota (harus 0 baris) =="; r=$(q "SELECT id||' quota='||quota||' sold='||sold||' held='||held FROM tiers WHERE quota>0 AND sold+held>quota"); echo "${r:-OK}"; [ -z "$r" ] || fail=1
echo "== sold ≠ tiket terbit (harus 0 baris) =="; r=$(q "SELECT t.id||' sold='||t.sold||' tiket='||c.n FROM tiers t JOIN (SELECT tier_id, count(*) n FROM tickets WHERE status IN ('valid','used','transferred','refunded','void') AND origin='sale' GROUP BY tier_id) c ON c.tier_id=t.id WHERE t.sold < (SELECT count(*) FROM tickets x WHERE x.tier_id=t.id AND x.origin='sale' AND x.status IN ('valid','used'))"); echo "${r:-OK}"; [ -z "$r" ] || fail=1
echo "== kursi ganda terjual (harus 0 baris) =="; r=$(q "SELECT seat_id FROM tickets WHERE seat_id IS NOT NULL AND status IN ('valid','used') GROUP BY seat_id HAVING count(*)>1"); echo "${r:-OK}"; [ -z "$r" ] || fail=1
echo "== ledger seimbang =="; r=$(q "SELECT coalesce(sum(debit)-sum(credit),0) FROM ledger_entries"); echo "selisih=$r"; [ "$r" = "0" ] || fail=1
echo "== order lunas tanpa tiket (harus 0 baris) =="; r=$(q "SELECT o.id FROM orders o WHERE o.status='paid' AND o.total>0 AND NOT EXISTS (SELECT 1 FROM order_items i JOIN tickets t ON t.order_item_id=i.id WHERE i.order_id=o.id)"); echo "${r:-OK}"; [ -z "$r" ] || fail=1
exit $fail
