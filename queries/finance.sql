-- name: InsertPayout :one
INSERT INTO payouts (id, organizer_id, bank_account_id, amount, requested_by) VALUES ($1, $2, $3, $4, $5) RETURNING *;

-- name: GetPayoutForUpdate :one
SELECT * FROM payouts WHERE id = $1 FOR UPDATE;

-- name: GetPayout :one
SELECT * FROM payouts WHERE id = $1;

-- name: ListOrgPayouts :many
SELECT * FROM payouts WHERE organizer_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3;

-- name: CountOrgPayouts :one
SELECT count(*) FROM payouts WHERE organizer_id = $1;

-- name: ListAllPayouts :many
SELECT p.*, o.name AS organizer_name FROM payouts p JOIN organizers o ON o.id = p.organizer_id
WHERE (sqlc.narg('status')::text IS NULL OR p.status = sqlc.narg('status'))
ORDER BY p.created_at DESC LIMIT @lim OFFSET @off;

-- name: CountAllPayouts :one
SELECT count(*) FROM payouts WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'));

-- name: SetPayoutStatus :exec
UPDATE payouts SET status = @status, approved_by = COALESCE(sqlc.narg('approved_by'), approved_by), note = COALESCE(sqlc.narg('note'), note),
  proof_key = COALESCE(sqlc.narg('proof_key'), proof_key), paid_at = CASE WHEN @status = 'paid' THEN @at::timestamptz ELSE paid_at END, updated_at = now()
WHERE id = @id;

-- name: PaidPayoutsTotal :one
SELECT COALESCE(sum(amount), 0)::bigint FROM payouts WHERE organizer_id = $1 AND status = 'paid';

-- name: EventPendingNet :one
-- saldo 'organizer_pending' yang masih melekat pada event (penjualan + refund − rilis sebelumnya)
SELECT COALESCE(sum(le.credit - le.debit), 0)::bigint FROM ledger_entries le
JOIN ledger_accounts la ON la.id = le.account_id AND la.kind = 'organizer_pending'
JOIN ledger_transactions lt ON lt.id = le.tx_id
WHERE (lt.ref_type = 'order_paid' AND lt.ref_id IN (SELECT o.id FROM orders o WHERE o.event_id = @event_id::text))
   OR (lt.ref_type = 'refund' AND lt.ref_id IN (SELECT r.id FROM refunds r JOIN orders o ON o.id = r.order_id WHERE o.event_id = @event_id::text))
   OR (lt.ref_type = 'settlement' AND lt.ref_id = @event_id::text);

-- name: EventsReadyForSettlement :many
SELECT e.id, e.organizer_id FROM events e
WHERE e.status = 'finished' AND COALESCE(e.ends_at, e.starts_at + interval '12 hours') + make_interval(days => @hold_days::int) <= @now::timestamptz
  AND NOT EXISTS (SELECT 1 FROM ledger_transactions lt WHERE lt.ref_type = 'settlement' AND lt.ref_id = e.id)
LIMIT 100;

-- name: EventSettlementSummary :one
SELECT count(*) FILTER (WHERE o.status IN ('paid','refunded'))::int AS orders,
  COALESCE(sum(o.subtotal - o.discount + o.addons_total) FILTER (WHERE o.status IN ('paid','refunded')), 0)::bigint AS gross,
  COALESCE(sum(o.discount) FILTER (WHERE o.status IN ('paid','refunded')), 0)::bigint AS discount,
  COALESCE(sum(o.fee) FILTER (WHERE o.status IN ('paid','refunded')), 0)::bigint AS buyer_fee,
  COALESCE(sum(o.platform_fee - o.fee) FILTER (WHERE o.status IN ('paid','refunded')), 0)::bigint AS organizer_fee,
  COALESCE(sum(o.total) FILTER (WHERE o.status IN ('paid','refunded')), 0)::bigint AS collected
FROM orders o WHERE o.event_id = @event_id::text;

-- name: EventRefundTotals :one
SELECT COALESCE(sum(r.amount) FILTER (WHERE r.status = 'refunded'), 0)::bigint AS refunded,
  COALESCE(sum(r.amount) FILTER (WHERE r.status NOT IN ('refunded','rejected')), 0)::bigint AS refunding
FROM refunds r JOIN orders o ON o.id = r.order_id WHERE o.event_id = @event_id::text AND r.scope = 'order';

-- name: EventTierSales :many
SELECT t.name, count(oi.id)::int AS qty, COALESCE(sum(oi.price), 0)::bigint AS revenue
FROM tiers t LEFT JOIN order_items oi ON oi.tier_id = t.id AND EXISTS (SELECT 1 FROM orders o WHERE o.id = oi.order_id AND o.status IN ('paid','refunded'))
WHERE t.event_id = @event_id::text GROUP BY t.id, t.name, t.sort ORDER BY t.sort, t.name;

-- name: PaymentsSettledBetween :many
SELECT id, order_id, provider_ref, amount, method, settled_at FROM payments WHERE status = 'settled' AND settled_at >= @from_ts AND settled_at < @to_ts ORDER BY settled_at;

-- name: OpenReconExists :one
SELECT EXISTS (SELECT 1 FROM recon_exceptions WHERE kind = $1 AND payment_id = $2 AND status <> 'resolved');

-- name: GetPaymentByProviderRef :one
SELECT * FROM payments WHERE provider_ref = $1;

-- name: GetOrganizerForUpdate :one
SELECT * FROM organizers WHERE id = $1 FOR UPDATE;

-- name: InsertDispute :one
INSERT INTO recon_exceptions (id, kind, payment_id, order_id, detail, status) VALUES ($1, 'chargeback', $2, $3, $4, 'investigating') RETURNING *;

-- name: SetReconDetail :exec
UPDATE recon_exceptions SET detail = $2 WHERE id = $1;

-- name: GetReconExceptionForUpdate :one
SELECT * FROM recon_exceptions WHERE id = $1 FOR UPDATE;

-- name: OpenDisputeForPayment :one
SELECT EXISTS (SELECT 1 FROM recon_exceptions WHERE kind = 'chargeback' AND payment_id = $1 AND status <> 'resolved');
