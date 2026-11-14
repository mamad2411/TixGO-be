-- name: InsertNotification :exec
INSERT INTO notifications (id, user_id, channel, template, to_addr, payload)
VALUES ($1, sqlc.narg('user_id'), $2, $3, $4, $5);

-- name: GetNotification :one
SELECT * FROM notifications WHERE id = $1;

-- name: MarkNotificationSent :exec
UPDATE notifications SET status = 'sent', attempts = attempts + 1, sent_at = now(), error = NULL WHERE id = $1;

-- name: MarkNotificationFailed :exec
UPDATE notifications SET status = $2, attempts = attempts + 1, error = $3 WHERE id = $1;

-- name: MarkNotificationSuppressed :exec
UPDATE notifications SET status = 'suppressed' WHERE id = $1;

-- name: ListNotificationsByUser :many
SELECT * FROM notifications WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3;

-- Kotak masuk in-app tidak menampilkan email berisi tautan rahasia (verifikasi/reset/ganti email) maupun push/suppressed.
-- name: CountUnreadNotifications :one
SELECT count(*) FROM notifications
WHERE user_id = $1 AND read_at IS NULL AND template <> 'push' AND status <> 'suppressed'
  AND template NOT IN ('verify_email','reset_password','email_change_confirm');

-- name: MarkNotificationsRead :execrows
UPDATE notifications SET read_at = now()
WHERE user_id = @uid AND read_at IS NULL AND (cardinality(@ids::text[]) = 0 OR id = ANY(@ids::text[]));

-- name: GetNotificationPref :one
SELECT enabled FROM notification_prefs WHERE user_id = $1 AND category = $2 AND channel = $3;

-- name: UpsertNotificationPref :exec
INSERT INTO notification_prefs (user_id, category, channel, enabled) VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id, category, channel) DO UPDATE SET enabled = EXCLUDED.enabled;

-- name: ListNotificationPrefs :many
SELECT * FROM notification_prefs WHERE user_id = $1;
