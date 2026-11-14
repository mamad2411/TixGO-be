-- name: InsertPromo :one
INSERT INTO promo_codes (id, event_id, code, kind, value, max_uses, max_per_account, starts_at, ends_at, min_total, tier_ids)
VALUES ($1, $2, $3, $4, $5, sqlc.narg('max_uses'), $6, sqlc.narg('starts_at'), sqlc.narg('ends_at'), $7, sqlc.narg('tier_ids')) RETURNING *;

-- name: GetPromoByCode :one
SELECT * FROM promo_codes WHERE event_id = $1 AND code = $2;

-- name: GetPromo :one
SELECT * FROM promo_codes WHERE id = $1;

-- name: ListPromos :many
SELECT * FROM promo_codes WHERE event_id = $1 ORDER BY created_at DESC;

-- name: UpdatePromo :one
UPDATE promo_codes SET active = COALESCE(sqlc.narg('active'), active), max_uses = COALESCE(sqlc.narg('max_uses'), max_uses),
  starts_at = COALESCE(sqlc.narg('starts_at'), starts_at), ends_at = COALESCE(sqlc.narg('ends_at'), ends_at)
WHERE id = @id RETURNING *;

-- name: ConsumePromo :execrows
UPDATE promo_codes SET used = used + 1 WHERE id = $1 AND active AND (max_uses IS NULL OR used < max_uses);

-- name: ReleasePromo :exec
UPDATE promo_codes SET used = GREATEST(used - 1, 0) WHERE id = $1;

-- name: InsertPromoRedemption :exec
INSERT INTO promo_redemptions (id, promo_id, order_id, user_id, discount) VALUES ($1, $2, $3, $4, $5);

-- name: DeletePromoRedemptionByOrder :one
DELETE FROM promo_redemptions WHERE order_id = $1 RETURNING promo_id;

-- name: PromoUsesByUser :one
SELECT count(*)::int FROM promo_redemptions pr JOIN orders o ON o.id = pr.order_id
WHERE pr.promo_id = $1 AND pr.user_id = $2 AND o.status IN ('pending','paid','refunding','partially_refunded');

-- name: PromoReport :one
SELECT count(*)::bigint AS redemptions, COALESCE(sum(pr.discount), 0)::bigint AS discount_total,
  COALESCE(sum(o.total), 0)::bigint AS revenue
FROM promo_redemptions pr JOIN orders o ON o.id = pr.order_id WHERE pr.promo_id = $1 AND o.status IN ('paid','refunding','partially_refunded');
