#!/usr/bin/env bash
# Latihan backup & restore (docs/13 T43, docs/20 M44 AC5): dump → pulihkan ke DB sementara → bandingkan jumlah baris semua tabel → ukur durasi (RTO).
# Pakai: SOURCE_DATABASE_URL=postgres://u:p@host:port/db ADMIN_DATABASE_URL=postgres://u:p@host:port/postgres scripts/backup-restore-drill.sh
# Tidak menyentuh DB sumber (hanya baca). DB sementara dihapus di akhir.
set -euo pipefail
: "${SOURCE_DATABASE_URL:?set SOURCE_DATABASE_URL}"; : "${ADMIN_DATABASE_URL:?set ADMIN_DATABASE_URL}"
# Bila versi pg_dump lokal ≠ server, jalankan alat PG di dalam kontainer: PG_CONTAINER=tixgo-pg-1 (URL host:port otomatis diganti localhost:5432).
PG_CONTAINER="${PG_CONTAINER:-}"
inside() { echo "$1" | sed -E 's#@[^/]+/#@localhost:5432/#'; }
pgx() { local tool="$1"; shift; if [ -n "$PG_CONTAINER" ]; then docker exec -i "$PG_CONTAINER" "$tool" "$@"; else "$tool" "$@"; fi; }
url() { if [ -n "$PG_CONTAINER" ]; then inside "$1"; else echo "$1"; fi; }
TMPDB="tixgo_restore_drill_$$"; DUMP="$(mktemp -t tixgo-drill.XXXXXX)"
base="${SOURCE_DATABASE_URL%/*}"; q="${SOURCE_DATABASE_URL#*\?}"; [ "$q" = "$SOURCE_DATABASE_URL" ] && q="" || q="?$q"
cleanup() { pgx psql "$(url "$ADMIN_DATABASE_URL")" -qc "DROP DATABASE IF EXISTS $TMPDB" >/dev/null 2>&1 || true; rm -f "$DUMP"; }
trap cleanup EXIT
t0=$(date +%s)
echo "1) dump (format custom)…"; pgx pg_dump -Fc --no-owner --no-privileges "$(url "$SOURCE_DATABASE_URL")" > "$DUMP"
echo "   ukuran dump: $(du -h "$DUMP" | cut -f1)"
echo "2) restore ke ${TMPDB} ..."; pgx psql "$(url "$ADMIN_DATABASE_URL")" -qc "CREATE DATABASE $TMPDB"
pgx pg_restore --no-owner --no-privileges -d "$(url "${base}/${TMPDB}${q}")" < "$DUMP"
t1=$(date +%s)
echo "3) bandingkan jumlah baris…"
tables=$(pgx psql "$(url "$SOURCE_DATABASE_URL")" -Atc "select tablename from pg_tables where schemaname='public' order by 1")
bad=0; total=0
for t in $tables; do
  a=$(pgx psql "$(url "$SOURCE_DATABASE_URL")" -Atc "select count(*) from \"$t\""); b=$(pgx psql "$(url "${base}/${TMPDB}${q}")" -Atc "select count(*) from \"$t\"")
  total=$((total+a)); [ "$a" = "$b" ] || { echo "   BEDA $t: sumber=$a pulih=$b"; bad=1; }
done
echo "   total baris: $total"
echo "4) invarian: ledger seimbang di hasil restore…"
d=$(pgx psql "$(url "${base}/${TMPDB}${q}")" -Atc "select coalesce(sum(debit)-sum(credit),0) from ledger_entries" 2>/dev/null || echo 0); echo "   selisih=$d"; [ "$d" = "0" ] || bad=1
echo "RTO terukur (dump+restore): $((t1-t0)) detik"
[ $bad -eq 0 ] && echo "HASIL: LULUS" || { echo "HASIL: GAGAL"; exit 1; }
