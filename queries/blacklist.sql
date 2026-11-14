-- name: IsBlacklisted :one
SELECT EXISTS (
  SELECT 1 FROM blacklist
  WHERE kind = $1 AND value_hash = $2 AND (expires_at IS NULL OR expires_at > now())
);

-- name: AddBlacklist :one
INSERT INTO blacklist (id, kind, value_hash, reason, expires_at, created_by)
VALUES ($1, $2, $3, $4, sqlc.narg('expires_at'), sqlc.narg('created_by'))
ON CONFLICT (kind, value_hash) DO UPDATE SET reason = EXCLUDED.reason, expires_at = EXCLUDED.expires_at
RETURNING *;

-- name: ListBlacklist :many
SELECT * FROM blacklist ORDER BY created_at DESC LIMIT $1 OFFSET $2;

-- name: DeleteBlacklist :exec
DELETE FROM blacklist WHERE id = $1;

-- name: InsertRiskSignal :exec
INSERT INTO risk_signals (id, user_id, event_id, kind, score, meta)
VALUES ($1, sqlc.narg('user_id'), sqlc.narg('event_id'), $2, $3, sqlc.narg('meta'));

-- name: SumRiskScore :one
SELECT COALESCE(sum(score), 0)::int FROM risk_signals
WHERE user_id = $1 AND created_at > $2;

-- name: ListRiskSignals :many
SELECT * FROM risk_signals
WHERE (sqlc.narg('user_id')::text IS NULL OR user_id = sqlc.narg('user_id'))
ORDER BY created_at DESC LIMIT @lim OFFSET @off;
