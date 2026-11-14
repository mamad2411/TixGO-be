-- name: UpsertMFA :exec
INSERT INTO mfa_totp (user_id, secret_enc, confirmed_at) VALUES ($1, $2, NULL)
ON CONFLICT (user_id) DO UPDATE SET secret_enc = EXCLUDED.secret_enc, confirmed_at = NULL;

-- name: GetMFA :one
SELECT * FROM mfa_totp WHERE user_id = $1;

-- name: ConfirmMFA :exec
UPDATE mfa_totp SET confirmed_at = now() WHERE user_id = $1;

-- name: DeleteMFA :exec
DELETE FROM mfa_totp WHERE user_id = $1;

-- name: InsertRecoveryCode :exec
INSERT INTO mfa_recovery_codes (id, user_id, code_hash) VALUES ($1, $2, $3);

-- name: DeleteRecoveryCodes :exec
DELETE FROM mfa_recovery_codes WHERE user_id = $1;

-- name: UseRecoveryCode :one
UPDATE mfa_recovery_codes SET used_at = now()
WHERE user_id = $1 AND code_hash = $2 AND used_at IS NULL
RETURNING id;

-- name: CountRecoveryCodes :one
SELECT count(*) FROM mfa_recovery_codes WHERE user_id = $1 AND used_at IS NULL;

-- name: SetUserMFAEmail :exec
UPDATE users SET mfa_email = $2, updated_at = now() WHERE id = $1;

-- name: SetUserMFASMS :exec
UPDATE users SET mfa_sms = $2, updated_at = now() WHERE id = $1;
