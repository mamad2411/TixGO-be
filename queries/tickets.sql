-- name: InsertTicket :one
INSERT INTO tickets (id, order_item_id, event_id, tier_id, owner_user_id, seat_id, seat_label, entry_number, attendee_name, secret_enc, origin)
VALUES ($1, sqlc.narg('order_item_id'), $2, $3, $4, sqlc.narg('seat_id'), sqlc.narg('seat_label'), sqlc.narg('entry_number'), sqlc.narg('attendee_name'), $5, $6)
RETURNING *;

-- name: NextEntryNumber :one
SELECT nextval('ticket_entry_seq')::bigint;

-- name: GetTicket :one
SELECT * FROM tickets WHERE id = $1;

-- name: GetTicketForUpdate :one
SELECT * FROM tickets WHERE id = $1 FOR UPDATE;

-- name: ListUserTickets :many
SELECT t.*, e.title AS event_title, e.slug AS event_slug, e.poster_key AS event_poster_key, e.starts_at, e.doors_at, v.name AS venue_name, v.tz AS venue_tz, v.address AS venue_address,
  ti.name AS tier_name, e.status AS event_status, e.schedule_state
FROM tickets t JOIN events e ON e.id = t.event_id JOIN venues v ON v.id = e.venue_id JOIN tiers ti ON ti.id = t.tier_id
WHERE t.owner_user_id = $1 AND (sqlc.narg('status')::text IS NULL OR t.status = sqlc.narg('status'))
ORDER BY e.starts_at DESC, t.issued_at DESC LIMIT @lim OFFSET @off;

-- name: CountUserTickets :one
SELECT count(*) FROM tickets WHERE owner_user_id = $1 AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'));

-- name: ListOrderTickets :many
SELECT t.* FROM tickets t JOIN order_items oi ON oi.id = t.order_item_id WHERE oi.order_id = $1 ORDER BY t.entry_number;

-- name: SetTicketStatus :exec
UPDATE tickets SET status = $2 WHERE id = $1;

-- name: VoidOrderTickets :execrows
UPDATE tickets SET status = $2 WHERE order_item_id IN (SELECT id FROM order_items WHERE order_id = $1) AND status = 'valid';

-- name: SetTicketOwner :exec
UPDATE tickets SET owner_user_id = $2, secret_enc = $3, attendee_name = COALESCE(sqlc.narg('attendee_name'), attendee_name) WHERE id = $1;

-- name: MarkTicketUsed :exec
UPDATE tickets SET status = 'used', used_at = $2 WHERE id = $1;

-- name: CountUsedOrTransferredForOrder :one
SELECT count(*)::int FROM tickets t JOIN order_items oi ON oi.id = t.order_item_id
WHERE oi.order_id = $1 AND (t.status IN ('used','transferred') OR t.owner_user_id <> $2);
