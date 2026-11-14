-- name: SetTicketFacePrice :exec
UPDATE tickets SET face_price = $2 WHERE id = $1;

-- name: TicketFacePrice :one
SELECT COALESCE(t.face_price, oi.price, tr.price)::bigint AS face FROM tickets t
LEFT JOIN order_items oi ON oi.id = t.order_item_id JOIN tiers tr ON tr.id = t.tier_id WHERE t.id = $1;

-- name: InsertListing :one
INSERT INTO resale_listings (id, ticket_id, event_id, seller_id, face_price, price) VALUES ($1, $2, $3, $4, $5, $6) RETURNING *;

-- name: GetListing :one
SELECT * FROM resale_listings WHERE id = $1;

-- name: GetListingForUpdate :one
SELECT * FROM resale_listings WHERE id = $1 FOR UPDATE;

-- name: ListEventListings :many
SELECT l.id, l.price, l.face_price, t.seat_label, tr.name AS tier_name, tr.id AS tier_id, l.created_at
FROM resale_listings l JOIN tickets t ON t.id = l.ticket_id JOIN tiers tr ON tr.id = t.tier_id
WHERE l.event_id = $1 AND l.status = 'active' ORDER BY l.price, l.created_at LIMIT 100;

-- name: ListSellerListings :many
SELECT * FROM resale_listings WHERE seller_id = $1 ORDER BY created_at DESC LIMIT 100;

-- name: CancelListing :execrows
UPDATE resale_listings SET status = 'cancelled' WHERE id = $1 AND seller_id = $2 AND status = 'active';

-- name: ReserveListing :execrows
UPDATE resale_listings SET status = 'reserved', buyer_id = @buyer, reserved_until = @until, order_id = @order_id
WHERE id = @id AND status = 'active' AND seller_id <> @buyer;

-- name: ReleaseListingByOrder :execrows
UPDATE resale_listings SET status = 'active', buyer_id = NULL, reserved_until = NULL, order_id = NULL WHERE order_id = $1 AND status = 'reserved';

-- name: SetListingSold :exec
UPDATE resale_listings SET status = 'sold', sold_at = $2 WHERE id = $1;

-- name: StaleReservations :many
SELECT l.id, l.order_id FROM resale_listings l LEFT JOIN orders o ON o.id = l.order_id
WHERE l.status = 'reserved' AND l.reserved_until < $1 AND (o.id IS NULL OR o.status <> 'pending') LIMIT 100;

-- name: InsertResaleOrder :one
INSERT INTO orders (id, user_id, event_id, subtotal, fee, platform_fee, total, age_confirmed, refund_policy_snapshot, expires_at, resale_listing_id)
VALUES ($1, $2, $3, $4, $5, $5, $6, true, $7, $8, $9) RETURNING *;

-- name: InsertResalePayout :one
INSERT INTO resale_payouts (id, listing_id, seller_id, amount) VALUES ($1, $2, $3, $4) RETURNING *;

-- name: GetResalePayoutForUpdate :one
SELECT * FROM resale_payouts WHERE id = $1 FOR UPDATE;

-- name: GetResalePayout :one
SELECT * FROM resale_payouts WHERE id = $1;

-- name: ListSellerPayouts :many
SELECT id, listing_id, amount, status, bank_code, account_name, created_at, paid_at FROM resale_payouts WHERE seller_id = $1 ORDER BY created_at DESC;

-- name: SetResalePayoutAccount :execrows
UPDATE resale_payouts SET bank_code = $2, account_name = $3, account_enc = $4, status = 'ready' WHERE id = $1 AND seller_id = $5 AND status IN ('awaiting_account','ready');

-- name: ListResalePayoutsFinance :many
SELECT * FROM resale_payouts WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')) ORDER BY created_at LIMIT @lim OFFSET @off;

-- name: MarkResalePayoutPaid :exec
UPDATE resale_payouts SET status = 'paid', proof_key = $2, processed_by = $3, paid_at = $4 WHERE id = $1;

-- name: SoldListingsForEvent :one
SELECT count(*)::int FROM resale_listings WHERE event_id = $1 AND status = 'sold';
