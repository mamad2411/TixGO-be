-- name: InsertHold :one
INSERT INTO holds (id, event_id, user_id, expires_at, squad_id) VALUES ($1, $2, $3, $4, sqlc.narg('squad_id')) RETURNING *;

-- name: GetHold :one
SELECT * FROM holds WHERE id = $1;

-- name: GetHoldForUpdate :one
SELECT * FROM holds WHERE id = $1 FOR UPDATE;

-- name: ActiveHoldsForUserEvent :many
SELECT * FROM holds WHERE user_id = $1 AND event_id = $2 AND status = 'active' AND squad_id IS NULL FOR UPDATE;

-- name: SetHoldStatus :exec
UPDATE holds SET status = $2, order_id = COALESCE(sqlc.narg('order_id'), order_id) WHERE id = $1;

-- name: ExtendHold :exec
UPDATE holds SET expires_at = $2, extended_at = $3 WHERE id = $1;

-- name: DueActiveHolds :many
SELECT * FROM holds WHERE status = 'active' AND expires_at <= $1 ORDER BY expires_at LIMIT $2 FOR UPDATE SKIP LOCKED;

-- name: InsertHoldItem :exec
INSERT INTO hold_items (hold_id, seat_id, tier_id, qty) VALUES ($1, sqlc.narg('seat_id'), $2, $3);

-- name: ListHoldItems :many
SELECT hi.id, hi.hold_id, hi.seat_id, hi.tier_id, hi.qty, s.label, s.row_label, s.seat_no, t.name AS tier_name, t.price
FROM hold_items hi
LEFT JOIN seats s ON s.id = hi.seat_id
JOIN tiers t ON t.id = hi.tier_id
WHERE hi.hold_id = $1 ORDER BY s.label, hi.id;

-- name: AcquireSeats :many
UPDATE seats SET status = 'held'
WHERE event_id = @event_id AND id = ANY(@ids::text[]) AND status = 'available'
RETURNING id, tier_id, label;

-- name: SeatStatusesByIDs :many
SELECT id, status, event_id, tier_id, label FROM seats WHERE id = ANY(@ids::text[]);

-- name: ReleaseSeats :execrows
UPDATE seats SET status = 'available' WHERE id = ANY(@ids::text[]) AND status = 'held';

-- name: SellSeats :execrows
UPDATE seats SET status = 'sold' WHERE id = ANY(@ids::text[]) AND status = 'held';

-- name: ReSellFreeSeats :execrows
UPDATE seats SET status = 'sold' WHERE id = ANY(@ids::text[]) AND status = 'available';

-- name: HoldTierQty :execrows
UPDATE tiers SET held = held + @qty::int WHERE id = @id AND event_id = @event_id AND quota > 0 AND sold + held + @qty::int <= quota;

-- name: ReleaseTierHeld :exec
UPDATE tiers SET held = GREATEST(held - @qty::int, 0) WHERE id = @id;

-- name: SellTierHeld :execrows
UPDATE tiers SET held = held - @qty::int, sold = sold + @qty::int WHERE id = @id AND held >= @qty::int;

-- name: SellTierFree :execrows
UPDATE tiers SET sold = sold + @qty::int WHERE id = @id AND quota > 0 AND sold + held + @qty::int <= quota;

-- name: ReleaseTierSold :exec
UPDATE tiers SET sold = GREATEST(sold - @qty::int, 0) WHERE id = @id;

-- name: LockUser :exec
SELECT id FROM users WHERE id = $1 FOR UPDATE;

-- name: UserEventPurchased :one
SELECT count(*)::int FROM order_items oi JOIN orders o ON o.id = oi.order_id
WHERE o.user_id = $1 AND o.event_id = $2 AND o.status IN ('pending','paid','refunding','partially_refunded');

-- name: UserEventHeld :one
SELECT COALESCE(sum(hi.qty), 0)::int FROM hold_items hi JOIN holds h ON h.id = hi.hold_id
WHERE h.user_id = $1 AND h.event_id = $2 AND h.status = 'active';

-- name: LockSeatsOrdered :many
SELECT id, status, tier_id, label FROM seats WHERE event_id = @event_id AND id = ANY(@ids::text[]) ORDER BY id FOR UPDATE;

-- name: HoldItemTotals :many
SELECT hi.tier_id, sum(hi.qty)::int AS qty, bool_or(hi.seat_id IS NOT NULL) AS seated FROM hold_items hi WHERE hi.hold_id = $1 GROUP BY hi.tier_id;

-- name: UnsellSeats :execrows
UPDATE seats SET status = 'available' WHERE id = ANY(@ids::text[]) AND status = 'sold';
