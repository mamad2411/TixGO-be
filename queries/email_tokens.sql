-- name: CreateEmailToken :exec
INSERT INTO email_tokens (id, user_id, kind, token_hash, new_value, expires_at)
VALUES ($1, $2, $3, $4, sqlc.narg('new_value'), $5);

-- name: GetEmailTokenByHashForUpdate :one
SELECT * FROM email_tokens WHERE token_hash = $1 AND kind = $2 FOR UPDATE;

-- name: UseEmailToken :exec
UPDATE email_tokens SET used_at = now() WHERE id = $1;

-- name: InvalidateEmailTokens :exec
UPDATE email_tokens SET used_at = now() WHERE user_id = $1 AND kind = $2 AND used_at IS NULL;
