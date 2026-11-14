-- name: ClaimIdempotency :execrows
INSERT INTO idempotency_keys (key, user_id, route, request_hash) VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id, route, key) DO NOTHING;

-- name: GetIdempotency :one
SELECT * FROM idempotency_keys WHERE user_id = $1 AND route = $2 AND key = $3;

-- name: CompleteIdempotency :exec
UPDATE idempotency_keys SET response = $4, status = $5 WHERE user_id = $1 AND route = $2 AND key = $3;

-- name: DeleteIdempotency :exec
DELETE FROM idempotency_keys WHERE user_id = $1 AND route = $2 AND key = $3;

-- name: PurgeIdempotency :execrows
DELETE FROM idempotency_keys WHERE created_at < $1;
