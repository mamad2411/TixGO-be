-- name: CreateUser :one
INSERT INTO users (id, email, password_hash, name, locale)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: GetUserByIDForUpdate :one
SELECT * FROM users WHERE id = $1 FOR UPDATE;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = $1;

-- name: GetUserByPhone :one
SELECT * FROM users WHERE phone = $1;

-- name: SetUserPhoneVerified :exec
UPDATE users SET phone = $2, phone_verified_at = now(), updated_at = now() WHERE id = $1;

-- name: SetUserEmailVerified :exec
UPDATE users SET email_verified_at = now(), updated_at = now() WHERE id = $1;

-- name: UpdateUserProfile :one
UPDATE users SET
  name = COALESCE(sqlc.narg('name'), name),
  locale = COALESCE(sqlc.narg('locale'), locale),
  avatar_key = COALESCE(sqlc.narg('avatar_key'), avatar_key),
  updated_at = now()
WHERE id = @id
RETURNING *;

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = $2, failed_logins = 0, locked_until = NULL, updated_at = now() WHERE id = $1;

-- name: UpdateUserEmail :exec
UPDATE users SET email = $2, email_verified_at = now(), updated_at = now() WHERE id = $1;

-- name: RecordLoginFailure :one
UPDATE users SET failed_logins = failed_logins + 1, updated_at = now() WHERE id = $1 RETURNING failed_logins;

-- name: LockUserUntil :exec
UPDATE users SET locked_until = $2, failed_logins = 0, updated_at = now() WHERE id = $1;

-- name: RecordLoginSuccess :exec
UPDATE users SET failed_logins = 0, locked_until = NULL, last_login_at = now() WHERE id = $1;

-- name: SetUserStatus :exec
UPDATE users SET status = $2, updated_at = now() WHERE id = $1;

-- name: SetUserRole :exec
UPDATE users SET role = $2, updated_at = now() WHERE id = $1;

-- name: SetUserMFA :exec
UPDATE users SET mfa_enabled = $2, updated_at = now() WHERE id = $1;

-- name: AnonymizeUser :exec
UPDATE users SET
  email = 'deleted+' || id || '@anon.invalid',
  name = 'Pengguna Terhapus',
  phone = NULL, phone_verified_at = NULL,
  password_hash = '!', avatar_key = NULL, mfa_enabled = false,
  status = 'deleted', deleted_at = now(), anonymized_at = now(), updated_at = now()
WHERE id = $1;

-- name: SearchUsers :many
SELECT * FROM users
WHERE (sqlc.narg('q')::text IS NULL OR email ILIKE '%' || sqlc.narg('q') || '%' OR name ILIKE '%' || sqlc.narg('q') || '%')
  AND (sqlc.narg('role')::text IS NULL OR role = sqlc.narg('role'))
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
ORDER BY created_at DESC
LIMIT @lim OFFSET @off;

-- name: CountUsers :one
SELECT count(*) FROM users
WHERE (sqlc.narg('q')::text IS NULL OR email ILIKE '%' || sqlc.narg('q') || '%' OR name ILIKE '%' || sqlc.narg('q') || '%')
  AND (sqlc.narg('role')::text IS NULL OR role = sqlc.narg('role'))
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'));

-- name: AddPasswordHistory :exec
INSERT INTO password_history (user_id, password_hash) VALUES ($1, $2);

-- name: RecentPasswordHashes :many
SELECT password_hash FROM password_history WHERE user_id = $1 ORDER BY id DESC LIMIT $2;

-- name: ClearUserAvatar :one
UPDATE users SET avatar_key = NULL, updated_at = now() WHERE id = $1 RETURNING *;
