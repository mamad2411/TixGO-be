-- name: UpsertPushSub :exec
INSERT INTO push_subscriptions (id, user_id, endpoint, keys_enc) VALUES ($1, $2, $3, $4)
ON CONFLICT (endpoint) DO UPDATE SET user_id = EXCLUDED.user_id, keys_enc = EXCLUDED.keys_enc;

-- name: DeletePushSub :execrows
DELETE FROM push_subscriptions WHERE user_id = $1 AND endpoint = $2;

-- name: ListPushSubs :many
SELECT * FROM push_subscriptions WHERE user_id = $1;

-- name: InsertDSR :one
INSERT INTO dsr_requests (id, user_id, kind, reason, detail, due_at) VALUES ($1, $2, $3, sqlc.narg('reason'), $4, $5) RETURNING *;

-- name: GetDSRForUpdate :one
SELECT * FROM dsr_requests WHERE id = $1 FOR UPDATE;

-- name: ListUserDSR :many
SELECT * FROM dsr_requests WHERE user_id = $1 ORDER BY created_at DESC;

-- name: OpenDSRForUser :one
SELECT EXISTS (SELECT 1 FROM dsr_requests WHERE user_id = @user_id AND kind = @kind AND status IN ('received','identity_verified','in_review'));

-- name: ListDSR :many
SELECT d.*, u.email AS user_email FROM dsr_requests d LEFT JOIN users u ON u.id = d.user_id
WHERE (sqlc.narg('status')::text IS NULL OR d.status = sqlc.narg('status')) ORDER BY d.due_at NULLS LAST, d.created_at LIMIT @lim OFFSET @off;

-- name: CountDSR :one
SELECT count(*) FROM dsr_requests WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'));

-- name: SetDSRStatus :exec
UPDATE dsr_requests SET status = @status, reason = COALESCE(sqlc.narg('reason'), reason),
  closed_at = CASE WHEN @status IN ('executed','rejected_with_reason') THEN now() ELSE closed_at END WHERE id = @id;

-- name: InsertComplaint :one
INSERT INTO privacy_complaints (id, name, email, subject, body) VALUES ($1, sqlc.narg('name'), $2, $3, $4) RETURNING *;

-- name: ListComplaints :many
SELECT * FROM privacy_complaints WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')) ORDER BY created_at DESC LIMIT @lim OFFSET @off;

-- name: CloseComplaint :execrows
UPDATE privacy_complaints SET status = 'closed' WHERE id = $1 AND status = 'open';

-- name: InsertExportJob :one
INSERT INTO export_jobs (id, user_id, kind, params) VALUES ($1, $2, $3, $4) RETURNING *;

-- name: GetExportJob :one
SELECT * FROM export_jobs WHERE id = $1;

-- name: ListUserExports :many
SELECT * FROM export_jobs WHERE user_id = $1 AND kind = 'privacy' ORDER BY created_at DESC LIMIT 20;

-- name: SetExportRunning :execrows
UPDATE export_jobs SET status = 'running' WHERE id = $1 AND status = 'queued';

-- name: SetExportDone :exec
UPDATE export_jobs SET status = 'done', object_key = $2, expires_at = $3, finished_at = now() WHERE id = $1;

-- name: SetExportFailed :exec
UPDATE export_jobs SET status = 'failed', error = $2, finished_at = now() WHERE id = $1;

-- name: ExpiredExports :many
SELECT * FROM export_jobs WHERE status = 'done' AND expires_at < $1 AND object_key IS NOT NULL LIMIT 200;

-- name: ClearExportObject :exec
UPDATE export_jobs SET object_key = NULL WHERE id = $1;

-- name: DeletionBlockers :one
SELECT
  (SELECT count(*) FROM tickets t JOIN events e ON e.id = t.event_id WHERE t.owner_user_id = @uid::text AND t.status = 'valid' AND e.status IN ('published','paused') AND COALESCE(e.ends_at, e.starts_at + interval '12 hours') > now())::int AS upcoming_tickets,
  (SELECT count(*) FROM orders WHERE user_id = @uid::text AND status = 'pending')::int AS pending_orders,
  (SELECT count(*) FROM refunds r JOIN orders o ON o.id = r.order_id WHERE o.user_id = @uid::text AND r.status IN ('requested','approved','awaiting_account','refunding'))::int AS open_refunds,
  (SELECT count(*) FROM organizers WHERE owner_user_id = @uid::text AND status = 'active')::int AS owned_organizers,
  (SELECT count(*) FROM payouts p JOIN organizers o ON o.id = p.organizer_id WHERE o.owner_user_id = @uid::text AND p.status IN ('requested','approved','processing'))::int AS open_payouts;

-- name: AnonymizeOrderItemsForUser :exec
UPDATE order_items SET attendee_name = 'Anonim', attendee_email = '' WHERE order_id IN (SELECT id FROM orders WHERE user_id = $1);

-- name: ClearTicketNamesForUser :exec
UPDATE tickets SET attendee_name = NULL WHERE owner_user_id = $1;

-- name: AnonymizeSupportForUser :exec
UPDATE support_messages SET body = '[dihapus atas permintaan pengguna]' WHERE author_id = $1;

-- name: AnonymizeSupportTicketsForUser :exec
UPDATE support_tickets SET subject = '[dihapus]' WHERE user_id = $1;

-- name: WipeUserMfaRecovery :exec
DELETE FROM mfa_recovery_codes WHERE user_id = $1;

-- name: WipeUserMfa :exec
DELETE FROM mfa_totp WHERE user_id = $1;

-- name: WipeUserSocial :exec
DELETE FROM social_identities WHERE user_id = $1;

-- name: WipeUserPushSubs :exec
DELETE FROM push_subscriptions WHERE user_id = $1;

-- name: WipeUserEmailTokens :exec
DELETE FROM email_tokens WHERE user_id = $1;

-- name: WipeUserNotifPrefs :exec
DELETE FROM notification_prefs WHERE user_id = $1;

-- name: WipeUserFavorites :exec
DELETE FROM favorites WHERE user_id = $1;

-- name: WipeUserFollows :exec
DELETE FROM follows WHERE user_id = $1;

-- name: WipeUserReminders :exec
DELETE FROM reminders WHERE user_id = $1;

-- name: RevokeAllRefreshForUser :exec
UPDATE refresh_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL;

-- name: PurgeOldNotifications :execrows
DELETE FROM notifications WHERE created_at < $1;

-- name: PurgeOldTokens :execrows
DELETE FROM email_tokens WHERE expires_at < $1;

-- name: PurgeOldRefreshTokens :execrows
DELETE FROM refresh_tokens WHERE expires_at < $1;
