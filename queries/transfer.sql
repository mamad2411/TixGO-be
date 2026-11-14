-- name: InsertClaimLink :one
INSERT INTO claim_links (id, ticket_id, token_hash, target_email, created_by, expires_at)
VALUES ($1, $2, $3, sqlc.narg('target_email'), $4, $5) RETURNING *;

-- name: GetClaimLinkByHashForUpdate :one
SELECT * FROM claim_links WHERE token_hash = $1 FOR UPDATE;

-- name: GetClaimLinkByHash :one
SELECT * FROM claim_links WHERE token_hash = $1;

-- name: ActiveClaimLinkForTicket :one
SELECT * FROM claim_links WHERE ticket_id = $1 AND status = 'active' AND expires_at > $2 LIMIT 1;

-- name: SetClaimLinkClaimed :exec
UPDATE claim_links SET status = 'claimed', claimed_by = $2, claimed_at = $3 WHERE id = $1;

-- name: RevokeClaimLinksForTicket :execrows
UPDATE claim_links SET status = 'revoked' WHERE ticket_id = $1 AND status = 'active';

-- name: TransferTicketOwner :execrows
UPDATE tickets SET owner_user_id = @new_owner, secret_enc = @secret_enc, attendee_name = sqlc.narg('attendee_name'), origin = CASE WHEN origin = 'comp' THEN 'comp' ELSE 'transfer' END
WHERE id = @id AND owner_user_id = @old_owner AND status = 'valid';

-- name: InsertTicketTransfer :exec
INSERT INTO ticket_transfers (id, ticket_id, from_user_id, to_user_id, via, ref_id) VALUES ($1, $2, $3, $4, $5, sqlc.narg('ref_id'));

-- name: ListTicketTransfers :many
SELECT * FROM ticket_transfers WHERE ticket_id = $1 ORDER BY created_at;

-- name: CountTicketTransfers :one
SELECT count(*)::int FROM ticket_transfers WHERE ticket_id = $1;

-- name: SetEventTicketPolicy :exec
UPDATE events SET transfer_enabled = $2, resale_cap_pct = $3, updated_at = now() WHERE id = $1;

-- name: ActiveListingForTicket :one
SELECT * FROM resale_listings WHERE ticket_id = $1 AND status IN ('active','reserved') LIMIT 1;
