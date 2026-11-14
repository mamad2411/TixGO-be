-- name: InsertOutbox :exec
INSERT INTO outbox (channel, topic, payload) VALUES ($1, $2, $3);

-- name: ClaimOutbox :many
SELECT id, channel, topic, payload FROM outbox
WHERE published_at IS NULL
ORDER BY id
LIMIT $1
FOR UPDATE SKIP LOCKED;

-- name: MarkOutboxPublished :exec
UPDATE outbox SET published_at = now() WHERE id = ANY(@ids::bigint[]);

-- name: DeleteOldOutbox :execrows
DELETE FROM outbox WHERE published_at < now() - interval '1 day';
