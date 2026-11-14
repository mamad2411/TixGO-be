-- name: InsertAudit :exec
INSERT INTO audit_logs (id, actor_id, actor_role, action, entity, entity_id, before, after, ip, request_id)
VALUES ($1, sqlc.narg('actor_id'), sqlc.narg('actor_role'), $2, $3, sqlc.narg('entity_id'), sqlc.narg('before'), sqlc.narg('after'), sqlc.narg('ip'), sqlc.narg('request_id'));

-- name: ListAudit :many
SELECT * FROM audit_logs
WHERE (sqlc.narg('entity')::text IS NULL OR entity = sqlc.narg('entity'))
  AND (sqlc.narg('entity_id')::text IS NULL OR entity_id = sqlc.narg('entity_id'))
  AND (sqlc.narg('actor_id')::text IS NULL OR actor_id = sqlc.narg('actor_id'))
  AND (sqlc.narg('action')::text IS NULL OR action = sqlc.narg('action'))
ORDER BY created_at DESC, id DESC
LIMIT @lim OFFSET @off;

-- name: CountAudit :one
SELECT count(*) FROM audit_logs
WHERE (sqlc.narg('entity')::text IS NULL OR entity = sqlc.narg('entity'))
  AND (sqlc.narg('entity_id')::text IS NULL OR entity_id = sqlc.narg('entity_id'))
  AND (sqlc.narg('actor_id')::text IS NULL OR actor_id = sqlc.narg('actor_id'))
  AND (sqlc.narg('action')::text IS NULL OR action = sqlc.narg('action'));
