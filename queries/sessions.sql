-- name: CreateRefreshToken :exec
INSERT INTO refresh_tokens (id, user_id, family_id, token_hash, device_label, device_id, ip, user_agent, mfa, expires_at, last_seen_at)
VALUES ($1, $2, $3, $4, $5, $6, sqlc.narg('ip'), $7, $8, $9, now());

-- name: GetRefreshTokenByHash :one
SELECT * FROM refresh_tokens WHERE token_hash = $1;

-- name: GetRefreshTokenByHashForUpdate :one
SELECT * FROM refresh_tokens WHERE token_hash = $1 FOR UPDATE;

-- name: MarkRefreshTokenUsed :exec
UPDATE refresh_tokens SET used_at = now() WHERE id = $1;

-- name: RevokeSessionFamily :exec
UPDATE refresh_tokens SET revoked_at = now() WHERE family_id = $1 AND revoked_at IS NULL;

-- name: RevokeUserSessions :many
UPDATE refresh_tokens SET revoked_at = now()
WHERE user_id = $1 AND revoked_at IS NULL
RETURNING family_id;

-- name: RevokeOtherUserSessions :many
UPDATE refresh_tokens SET revoked_at = now()
WHERE user_id = $1 AND family_id <> $2 AND revoked_at IS NULL
RETURNING family_id;

-- name: ListUserSessions :many
SELECT DISTINCT ON (family_id) family_id, device_label, device_id, ip, user_agent, created_at, last_seen_at, expires_at
FROM refresh_tokens
WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > now()
ORDER BY family_id, id DESC;

-- name: GetSessionOwner :one
SELECT user_id FROM refresh_tokens WHERE family_id = $1 LIMIT 1;

-- name: DeleteExpiredRefreshTokens :execrows
DELETE FROM refresh_tokens WHERE expires_at < now() - interval '30 days';
