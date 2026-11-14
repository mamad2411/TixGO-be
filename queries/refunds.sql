-- name: InsertRefund :one
INSERT INTO refunds (id, order_id, payment_id, scope, amount, reason, status, method, initiated_by, refund_key, due_at)
VALUES (sqlc.arg('id')::text, sqlc.arg('order_id'), sqlc.narg('payment_id'), sqlc.arg('scope'), sqlc.arg('amount'), sqlc.narg('reason'), sqlc.arg('status'), sqlc.arg('method'), sqlc.arg('initiated_by'), sqlc.arg('id')::text, sqlc.narg('due_at')) RETURNING *;

-- name: GetRefund :one
SELECT * FROM refunds WHERE id = $1;

-- name: GetRefundForUpdate :one
SELECT * FROM refunds WHERE id = $1 FOR UPDATE;

-- name: OpenRefundForOrder :one
SELECT * FROM refunds WHERE order_id = $1 AND scope = 'order' AND status IN ('requested','approved','awaiting_account','refunding') LIMIT 1;

-- name: ListRefundsForOrder :many
SELECT * FROM refunds WHERE order_id = $1 ORDER BY created_at DESC;

-- name: StartRefundAttempt :execrows
UPDATE refunds SET status = 'refunding', attempts = attempts + 1, updated_at = now() WHERE id = $1 AND method = 'gateway' AND status IN ('approved','refunding');

-- name: SetRefundManual :exec
UPDATE refunds SET method = 'manual', status = 'awaiting_account', note = $2, updated_at = now(),
  due_at = COALESCE(due_at, $3) WHERE id = $1 AND status IN ('approved','refunding','requested');

-- name: SetRefundAccount :exec
UPDATE refunds SET refund_bank_code = $2, refund_account_name = $3, refund_account_enc = $4, status = 'refunding', updated_at = now() WHERE id = $1;

-- name: SetRefundDone :exec
UPDATE refunds SET status = 'refunded', provider_ref = COALESCE(sqlc.narg('provider_ref'), provider_ref), proof_key = COALESCE(sqlc.narg('proof_key'), proof_key),
  processed_by = sqlc.narg('processed_by'), note = COALESCE(sqlc.narg('note'), note), updated_at = now() WHERE id = $1;

-- name: SetRefundRejected :exec
UPDATE refunds SET status = 'rejected', note = $2, processed_by = sqlc.narg('processed_by'), updated_at = now() WHERE id = $1;

-- name: SetRefundFailedNote :exec
UPDATE refunds SET note = $2, updated_at = now() WHERE id = $1;

-- name: ListFinanceRefunds :many
SELECT r.*, o.user_id, o.event_id, o.total AS order_total FROM refunds r JOIN orders o ON o.id = r.order_id
WHERE (sqlc.narg('status')::text IS NULL OR r.status = sqlc.narg('status')) AND (sqlc.narg('method')::text IS NULL OR r.method = sqlc.narg('method'))
ORDER BY r.created_at DESC LIMIT @lim OFFSET @off;

-- name: CountFinanceRefunds :one
SELECT count(*) FROM refunds r WHERE (sqlc.narg('status')::text IS NULL OR r.status = sqlc.narg('status')) AND (sqlc.narg('method')::text IS NULL OR r.method = sqlc.narg('method'));

-- name: LedgerTxExists :one
SELECT EXISTS (SELECT 1 FROM ledger_transactions WHERE ref_type = $1 AND ref_id = $2);

-- name: PaidOrdersForEvent :many
SELECT id FROM orders WHERE event_id = $1 AND status = 'paid' ORDER BY paid_at LIMIT $2 OFFSET $3;

-- name: PaidOrdersWithoutRefund :many
SELECT o.id FROM orders o WHERE o.event_id = $1 AND o.status = 'paid' AND o.total > 0
  AND NOT EXISTS (SELECT 1 FROM refunds r WHERE r.order_id = o.id AND r.scope = 'order' AND r.status <> 'rejected')
ORDER BY o.paid_at LIMIT $2;

-- name: OrderPendingPaymentsSum :one
SELECT COALESCE(sum(amount), 0)::bigint FROM payments WHERE order_id = $1 AND status = 'settled';

-- name: OverdueAccountRefunds :many
SELECT r.*, o.user_id FROM refunds r JOIN orders o ON o.id = r.order_id
WHERE r.status = 'awaiting_account' AND r.due_at IS NOT NULL AND r.due_at < $1 ORDER BY r.due_at LIMIT 100;
