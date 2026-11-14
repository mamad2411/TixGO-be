-- name: EnsureQueueRun :exec
INSERT INTO queue_runs (event_id, seed_hash, seed) VALUES ($1, $2, $3) ON CONFLICT (event_id) DO NOTHING;

-- name: GetQueueRun :one
SELECT * FROM queue_runs WHERE event_id = $1;

-- name: StartQueueRun :exec
UPDATE queue_runs SET started_at = $2, participants = $3, participants_digest = $4 WHERE event_id = $1 AND started_at IS NULL;
