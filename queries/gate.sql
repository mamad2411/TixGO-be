-- name: GetGateScanByScanID :one
SELECT * FROM gate_scans WHERE scan_id = $1;

-- name: InsertGateScan :one
INSERT INTO gate_scans (id, scan_id, ticket_id, event_id, gate_id, staff_user_id, result, detail, scanned_at)
VALUES ($1, sqlc.narg('scan_id'), sqlc.narg('ticket_id'), $2, sqlc.narg('gate_id'), $3, $4, sqlc.narg('detail'), $5) RETURNING *;

-- name: ClaimTicketEntry :execrows
UPDATE tickets SET status = 'used', used_at = @used_at WHERE id = @id AND status = 'valid';

-- name: OKScanForTicket :one
SELECT * FROM gate_scans WHERE ticket_id = $1 AND result = 'OK';

-- name: DemoteScan :exec
UPDATE gate_scans SET result = 'DUPLICATE_OFFLINE', detail = sqlc.narg('detail') WHERE id = @id;

-- name: SetTicketUsedAt :exec
UPDATE tickets SET used_at = $2 WHERE id = $1;

-- name: GateStats :many
SELECT coalesce(gate_id, '') AS gate_id, result, count(*)::int AS n FROM gate_scans WHERE event_id = $1 GROUP BY 1, 2;

-- name: EventTicketCounts :one
SELECT count(*) FILTER (WHERE status IN ('valid','used'))::int AS issued, count(*) FILTER (WHERE status = 'used')::int AS used FROM tickets WHERE event_id = $1;

-- name: GateManifest :many
SELECT id, status, seat_label, attendee_name, secret_enc, tier_id FROM tickets WHERE event_id = $1 AND status IN ('valid','used') ORDER BY id;

-- name: IsGateStaffFor :one
SELECT EXISTS (SELECT 1 FROM gate_staff WHERE event_id = $1 AND user_id = $2);

-- name: RecentFailedScans :many
SELECT * FROM gate_scans WHERE event_id = $1 AND result <> 'OK' ORDER BY scanned_at DESC LIMIT $2;
