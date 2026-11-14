-- name: InsertOrder :one
INSERT INTO orders (id, user_id, event_id, hold_id, subtotal, addons_total, discount, fee, platform_fee, payment_fee, tax, total, promo_id, campaign_slug,
  age_confirmed, refund_policy_snapshot, expires_at)
VALUES ($1, $2, $3, sqlc.narg('hold_id'), $4, $5, $6, $7, $8, $9, $10, $11, sqlc.narg('promo_id'), sqlc.narg('campaign_slug'), $12, $13, $14)
RETURNING *;

-- name: GetOrder :one
SELECT * FROM orders WHERE id = $1;

-- name: GetOrderForUpdate :one
SELECT * FROM orders WHERE id = $1 FOR UPDATE;

-- name: GetOrderByHold :one
SELECT * FROM orders WHERE hold_id = $1 AND status IN ('pending','paid') LIMIT 1;

-- name: ListUserOrders :many
SELECT o.*, e.title AS event_title, e.slug AS event_slug, e.poster_key AS event_poster_key, e.starts_at AS event_starts_at FROM orders o JOIN events e ON e.id = o.event_id
WHERE o.user_id = $1 ORDER BY o.created_at DESC LIMIT $2 OFFSET $3;

-- name: CountUserOrders :one
SELECT count(*) FROM orders WHERE user_id = $1;

-- name: SetOrderStatus :exec
UPDATE orders SET status = $2, updated_at = now() WHERE id = $1;

-- name: MarkOrderPaid :exec
UPDATE orders SET status = 'paid', paid_at = $2, payment_method = sqlc.narg('payment_method'), updated_at = now() WHERE id = $1;

-- name: InsertOrderItem :exec
INSERT INTO order_items (id, order_id, seat_id, tier_id, price, attendee_name, attendee_email) VALUES ($1, $2, sqlc.narg('seat_id'), $3, $4, $5, $6);

-- name: ListOrderItems :many
SELECT oi.*, s.label AS seat_label, t.name AS tier_name FROM order_items oi
LEFT JOIN seats s ON s.id = oi.seat_id JOIN tiers t ON t.id = oi.tier_id
WHERE oi.order_id = $1 ORDER BY oi.id;

-- name: InsertOrderAddon :exec
INSERT INTO order_addons (order_id, addon_id, qty, price) VALUES ($1, $2, $3, $4);

-- name: ListOrderAddons :many
SELECT oa.order_id, oa.addon_id, oa.qty, oa.price, a.name FROM order_addons oa JOIN addons a ON a.id = oa.addon_id WHERE oa.order_id = $1;

-- name: DueExpiredOrders :many
SELECT * FROM orders WHERE status = 'pending' AND expires_at <= $1 ORDER BY expires_at LIMIT $2 FOR UPDATE SKIP LOCKED;

-- name: PendingOrdersWithPayments :many
SELECT o.id, o.expires_at, p.id AS payment_id, p.provider_ref
FROM orders o JOIN payments p ON p.order_id = o.id
WHERE o.status = 'pending' AND p.status IN ('created','pending') AND p.created_at < $1
ORDER BY p.created_at LIMIT $2;

-- name: OrderOwnerEvent :one
SELECT o.user_id, o.event_id, e.organizer_id FROM orders o JOIN events e ON e.id = o.event_id WHERE o.id = $1;

-- name: WaitlistJoin :one
INSERT INTO waitlists (id, event_id, tier_id, user_id, qty) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (event_id, tier_id, user_id) DO UPDATE SET qty = EXCLUDED.qty,
  status = CASE WHEN waitlists.status IN ('expired','cancelled') THEN 'waiting' ELSE waitlists.status END,
  created_at = CASE WHEN waitlists.status IN ('expired','cancelled') THEN now() ELSE waitlists.created_at END
RETURNING *;

-- name: WaitlistLeave :exec
UPDATE waitlists SET status = 'cancelled' WHERE event_id = $1 AND tier_id = $2 AND user_id = $3 AND status IN ('waiting','offered');

-- name: WaitlistForUser :many
SELECT w.*, t.name AS tier_name, e.title AS event_title, e.slug AS event_slug
FROM waitlists w JOIN tiers t ON t.id = w.tier_id JOIN events e ON e.id = w.event_id
WHERE w.user_id = $1 AND w.status IN ('waiting','offered') ORDER BY w.created_at DESC;

-- name: WaitlistTiersWaiting :many
SELECT DISTINCT tier_id FROM waitlists WHERE event_id = $1 AND status = 'waiting';

-- name: WaitlistNext :one
SELECT * FROM waitlists WHERE event_id = $1 AND tier_id = $2 AND status = 'waiting' ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED;

-- name: WaitlistOffer :exec
UPDATE waitlists SET status = 'offered', offered_until = $2, hold_id = $3 WHERE id = $1;

-- name: WaitlistByHold :one
SELECT * FROM waitlists WHERE hold_id = $1 AND status = 'offered' LIMIT 1;

-- name: WaitlistSetStatus :exec
UPDATE waitlists SET status = $2 WHERE id = $1;

-- name: AvailableSeatsInTier :many
SELECT id FROM seats WHERE event_id = $1 AND tier_id = $2 AND status = 'available'
ORDER BY section_id, row_label, length(seat_no), seat_no LIMIT $3;

-- name: WaitlistPosition :one
SELECT count(*)::int FROM waitlists w2 WHERE w2.event_id = $1 AND w2.tier_id = $2 AND w2.status = 'waiting'
  AND w2.created_at <= (SELECT w3.created_at FROM waitlists w3 WHERE w3.id = $3);

-- name: ReserveAddon :execrows
UPDATE addons SET sold = sold + @qty::int WHERE id = @id AND active AND sold + @qty::int <= quota;

-- name: ReleaseAddon :exec
UPDATE addons SET sold = GREATEST(sold - @qty::int, 0) WHERE id = @id;

-- name: ExtendOrderExpiry :exec
UPDATE orders SET expires_at = $2, updated_at = now() WHERE id = $1 AND status = 'pending';

-- name: SeatsForAssist :many
SELECT id, section_id, row_label, seat_no, label, x, y, accessible, status FROM seats
WHERE event_id = $1 AND tier_id = $2 ORDER BY section_id, row_label, length(seat_no), seat_no;

-- name: GetSeatPrefs :one
SELECT prefs FROM seat_preferences WHERE user_id = $1 AND event_id = $2;

-- name: UpsertSeatPrefs :exec
INSERT INTO seat_preferences (user_id, event_id, prefs) VALUES ($1, $2, $3)
ON CONFLICT (user_id, event_id) DO UPDATE SET prefs = EXCLUDED.prefs, updated_at = now();

-- name: AdminSearchOrders :many
SELECT o.id, o.status, o.total, o.created_at, o.paid_at, u.email AS user_email, e.title AS event_title
FROM orders o JOIN users u ON u.id = o.user_id JOIN events e ON e.id = o.event_id
WHERE (sqlc.narg('q')::text IS NULL OR o.id = sqlc.narg('q') OR u.email = lower(sqlc.narg('q')))
  AND (sqlc.narg('status')::text IS NULL OR o.status = sqlc.narg('status'))
ORDER BY o.created_at DESC LIMIT @lim OFFSET @off;

-- name: ListPaymentsForOrder :many
SELECT * FROM payments WHERE order_id = $1 ORDER BY created_at;

-- name: UsedTicketsInOrder :one
SELECT count(*)::int FROM tickets t JOIN order_items oi ON oi.id = t.order_item_id WHERE oi.order_id = $1 AND t.status = 'used';
