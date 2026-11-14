-- name: InsertAPIKey :one
INSERT INTO api_keys (id, organizer_id, name, key_hash, key_prefix, scopes) VALUES ($1, $2, $3, $4, $5, $6) RETURNING *;

-- name: ListAPIKeys :many
SELECT * FROM api_keys WHERE organizer_id = $1 ORDER BY created_at DESC;

-- name: GetAPIKeyByHash :one
SELECT k.*, o.status AS organizer_status FROM api_keys k JOIN organizers o ON o.id = k.organizer_id WHERE k.key_hash = $1 AND k.revoked_at IS NULL;

-- name: RevokeAPIKey :execrows
UPDATE api_keys SET revoked_at = now() WHERE id = $1 AND organizer_id = $2 AND revoked_at IS NULL;

-- name: TouchAPIKey :exec
UPDATE api_keys SET last_used_at = now() WHERE id = $1;

-- name: InsertWebhookEndpoint :one
INSERT INTO webhook_endpoints (id, organizer_id, url, secret_enc, events) VALUES ($1, $2, $3, $4, $5) RETURNING *;

-- name: ListWebhookEndpoints :many
SELECT * FROM webhook_endpoints WHERE organizer_id = $1 ORDER BY created_at;

-- name: GetWebhookEndpoint :one
SELECT * FROM webhook_endpoints WHERE id = $1;

-- name: DeleteWebhookEndpoint :execrows
DELETE FROM webhook_endpoints WHERE id = $1 AND organizer_id = $2;

-- name: SetWebhookActive :exec
UPDATE webhook_endpoints SET active = $2 WHERE id = $1;

-- name: ActiveEndpointsForEvent :many
SELECT * FROM webhook_endpoints WHERE organizer_id = @org AND active AND @event::text = ANY(events);

-- name: InsertWebhookDelivery :one
INSERT INTO webhook_deliveries (id, endpoint_id, event, payload, next_attempt_at) VALUES ($1, $2, $3, $4, $5) RETURNING *;

-- name: GetWebhookDelivery :one
SELECT * FROM webhook_deliveries WHERE id = $1;

-- name: RecordDeliveryAttempt :exec
UPDATE webhook_deliveries SET attempts = attempts + 1, status = sqlc.narg('status'),
  next_attempt_at = sqlc.narg('next_attempt_at'), delivered_at = sqlc.narg('delivered_at') WHERE id = @id;

-- name: ListWebhookDeliveries :many
SELECT d.* FROM webhook_deliveries d JOIN webhook_endpoints e ON e.id = d.endpoint_id WHERE e.organizer_id = $1 AND e.id = $2 ORDER BY d.created_at DESC LIMIT 50;

-- name: AddWidgetDomain :exec
INSERT INTO widget_domains (organizer_id, domain) VALUES ($1, $2) ON CONFLICT DO NOTHING;

-- name: ListWidgetDomains :many
SELECT domain FROM widget_domains WHERE organizer_id = $1 ORDER BY domain;

-- name: DeleteWidgetDomain :execrows
DELETE FROM widget_domains WHERE organizer_id = $1 AND domain = $2;

-- name: SetEventTracking :exec
UPDATE events SET tracking = $2, updated_at = now() WHERE id = $1;

-- name: PartnerEvents :many
SELECT id, slug, title, status, starts_at FROM events WHERE organizer_id = $1 ORDER BY starts_at DESC LIMIT 200;
