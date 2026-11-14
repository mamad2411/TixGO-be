-- name: ListFlags :many
SELECT * FROM feature_flags ORDER BY key;

-- name: UpsertFlag :one
INSERT INTO feature_flags (key, enabled, rollout_pct, updated_by) VALUES ($1, $2, $3, $4)
ON CONFLICT (key) DO UPDATE SET enabled = EXCLUDED.enabled, rollout_pct = EXCLUDED.rollout_pct, updated_by = EXCLUDED.updated_by, updated_at = now()
RETURNING *;

-- name: InsertAnnouncement :one
INSERT INTO announcements (id, level, body_id, body_en, starts_at, ends_at, created_by) VALUES ($1, $2, $3, sqlc.narg('body_en'), $4, sqlc.narg('ends_at'), $5) RETURNING *;

-- name: ListAnnouncements :many
SELECT * FROM announcements ORDER BY starts_at DESC LIMIT $1;

-- name: ActiveAnnouncements :many
SELECT * FROM announcements WHERE starts_at <= @now AND (ends_at IS NULL OR ends_at > @now) ORDER BY starts_at DESC;

-- name: EndAnnouncement :execrows
UPDATE announcements SET ends_at = @now WHERE id = @id AND (ends_at IS NULL OR ends_at > @now);

-- name: SetSalesPaused :one
UPDATE events SET sales_paused = @paused, sales_paused_reason = sqlc.narg('reason'), updated_at = now() WHERE id = @id RETURNING *;

-- name: SetEventAdmitRate :one
UPDATE events SET admit_rate = @rate, updated_at = now() WHERE id = @id RETURNING *;

-- name: EventLiveCounts :one
SELECT
  (SELECT count(*) FROM holds WHERE event_id = @event_id::text AND status = 'active')::int AS active_holds,
  (SELECT count(*) FROM orders WHERE event_id = @event_id::text AND status = 'pending')::int AS pending_orders,
  (SELECT count(*) FROM orders WHERE event_id = @event_id::text AND status = 'paid')::int AS paid_orders,
  (SELECT count(*) FROM tickets WHERE event_id = @event_id::text AND status IN ('valid','used'))::int AS tickets_issued;

-- name: OpenReconCount :one
SELECT count(*)::int FROM recon_exceptions WHERE status <> 'resolved';

-- name: PendingOutboxCount :one
SELECT count(*)::int FROM outbox WHERE published_at IS NULL;
