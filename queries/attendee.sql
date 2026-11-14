-- name: ListAttendees :many
SELECT t.id, t.status, t.origin, t.entry_number, t.seat_label, t.attendee_name, t.used_at, t.issued_at, t.tier_id, tr.name AS tier_name,
  u.email AS owner_email, u.name AS owner_name
FROM tickets t JOIN tiers tr ON tr.id = t.tier_id JOIN users u ON u.id = t.owner_user_id
WHERE t.event_id = @event_id::text
  AND (sqlc.narg('status')::text IS NULL OR t.status = sqlc.narg('status'))
  AND (sqlc.narg('tier_id')::text IS NULL OR t.tier_id = sqlc.narg('tier_id'))
  AND (sqlc.narg('q')::text IS NULL OR t.attendee_name ILIKE '%' || sqlc.narg('q') || '%' OR u.email ILIKE '%' || sqlc.narg('q') || '%'
       OR t.id = sqlc.narg('q') OR t.entry_number = sqlc.narg('q'))
ORDER BY t.issued_at, t.id LIMIT @lim OFFSET @off;

-- name: CountAttendees :one
SELECT count(*) FROM tickets t JOIN users u ON u.id = t.owner_user_id
WHERE t.event_id = @event_id::text
  AND (sqlc.narg('status')::text IS NULL OR t.status = sqlc.narg('status'))
  AND (sqlc.narg('tier_id')::text IS NULL OR t.tier_id = sqlc.narg('tier_id'))
  AND (sqlc.narg('q')::text IS NULL OR t.attendee_name ILIKE '%' || sqlc.narg('q') || '%' OR u.email ILIKE '%' || sqlc.narg('q') || '%'
       OR t.id = sqlc.narg('q') OR t.entry_number = sqlc.narg('q'));

-- name: ReserveComp :execrows
UPDATE tiers SET comp_issued = comp_issued + 1 WHERE id = @id AND event_id = @event_id AND comp_issued < comp_quota;

-- name: InsertCompInvitation :one
INSERT INTO comp_invitations (id, event_id, tier_id, name, email, reason, issued_by, ticket_id) VALUES ($1, $2, $3, $4, $5, sqlc.narg('reason'), $6, $7) RETURNING *;

-- name: ListComps :many
SELECT * FROM comp_invitations WHERE event_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3;

-- name: SetTicketStatusForEvent :execrows
UPDATE tickets SET status = @status WHERE id = @id AND event_id = @event_id AND status = 'valid';

-- name: RotateTicketSecret :exec
UPDATE tickets SET secret_enc = $2 WHERE id = $1;

-- name: RenameTicketAttendee :exec
UPDATE tickets SET attendee_name = $2 WHERE id = $1;

-- name: InsertBroadcast :one
INSERT INTO broadcasts (id, event_id, tier_id, subject, body, recipients, created_by) VALUES ($1, $2, sqlc.narg('tier_id'), $3, $4, $5, $6) RETURNING *;

-- name: GetBroadcast :one
SELECT * FROM broadcasts WHERE id = $1;

-- name: ListBroadcasts :many
SELECT * FROM broadcasts WHERE event_id = $1 ORDER BY created_at DESC LIMIT 50;

-- name: BroadcastRecipients :many
SELECT DISTINCT t.owner_user_id FROM tickets t WHERE t.event_id = @event_id::text AND t.status IN ('valid','used')
  AND (sqlc.narg('tier_id')::text IS NULL OR t.tier_id = sqlc.narg('tier_id'));

-- name: SetBroadcastProgress :exec
UPDATE broadcasts SET status = $2, sent_count = $3 WHERE id = $1;

-- name: CountBroadcastsToday :one
SELECT count(*)::int FROM broadcasts WHERE event_id = @event_id::text AND created_at >= @since;
