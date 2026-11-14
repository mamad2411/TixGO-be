-- name: InsertPayment :one
INSERT INTO payments (id, order_id, provider, provider_ref, method, amount, status, snap_token, redirect_url)
VALUES ($1, $2, $3, $4, sqlc.narg('method'), $5, $6, sqlc.narg('snap_token'), sqlc.narg('redirect_url')) RETURNING *;

-- name: GetPayment :one
SELECT * FROM payments WHERE id = $1;

-- name: LatestPaymentForOrder :one
SELECT * FROM payments WHERE order_id = $1 ORDER BY created_at DESC LIMIT 1;

-- name: GetPaymentByRef :one
SELECT * FROM payments WHERE provider = $1 AND provider_ref = $2;

-- name: GetPaymentByRefForUpdate :one
SELECT * FROM payments WHERE provider = $1 AND provider_ref = $2 FOR UPDATE;

-- name: UpdatePaymentStatus :exec
UPDATE payments SET status = $2, method = COALESCE(sqlc.narg('method'), method),
  settled_at = CASE WHEN $2 = 'settled' THEN $3 ELSE settled_at END WHERE id = $1;

-- name: InsertPaymentEvent :execrows
INSERT INTO payment_events (id, payment_id, provider_event_id, payload) VALUES ($1, sqlc.narg('payment_id'), $2, $3)
ON CONFLICT (provider_event_id) DO NOTHING;

-- name: MarkPaymentEventProcessed :exec
UPDATE payment_events SET processed_at = now() WHERE provider_event_id = $1;

-- name: GetPaymentEvent :one
SELECT * FROM payment_events WHERE provider_event_id = $1;

-- name: SettledPaymentForOrder :one
SELECT * FROM payments WHERE order_id = $1 AND status = 'settled' ORDER BY created_at DESC LIMIT 1;

-- name: StalePendingPayments :many
SELECT * FROM payments WHERE status IN ('created','pending') AND created_at < @before AND created_at > @after ORDER BY created_at LIMIT @lim;

-- name: InsertReconException :exec
INSERT INTO recon_exceptions (id, kind, payment_id, order_id, detail) VALUES ($1, $2, sqlc.narg('payment_id'), sqlc.narg('order_id'), $3);

-- name: ListReconExceptions :many
SELECT * FROM recon_exceptions WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')) ORDER BY created_at DESC LIMIT @lim OFFSET @off;

-- name: CountReconExceptions :one
SELECT count(*) FROM recon_exceptions WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'));

-- name: GetReconException :one
SELECT * FROM recon_exceptions WHERE id = $1;

-- name: ResolveReconException :exec
UPDATE recon_exceptions SET status = $2, resolution = sqlc.narg('resolution'), resolved_at = CASE WHEN $2 = 'resolved' THEN now() ELSE resolved_at END WHERE id = $1;

-- name: SetPaymentIntent :exec
-- Mengisi token/redirect pada percobaan bayar yang sudah di-claim (baris dibuat sebelum memanggil gateway).
UPDATE payments SET snap_token = $2, redirect_url = $3 WHERE id = $1;
