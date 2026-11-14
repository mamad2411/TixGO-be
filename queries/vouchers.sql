-- name: FindPublicPromosByCode :many
SELECT p.* FROM promo_codes p JOIN events e ON e.id = p.event_id
WHERE p.code = $1 AND e.status IN ('published','paused') AND e.visibility = 'public';

-- name: SaveUserVoucher :exec
INSERT INTO user_vouchers (user_id, promo_id) VALUES ($1, $2) ON CONFLICT DO NOTHING;

-- name: DeleteUserVoucher :execrows
DELETE FROM user_vouchers WHERE user_id = $1 AND promo_id = $2;

-- name: ListUserVouchers :many
SELECT p.id AS promo_id, p.code, p.kind, p.value, p.min_total, p.tier_ids, p.starts_at, p.ends_at,
       p.active, p.max_uses, p.used, p.max_per_account, uv.saved_at,
       e.id AS event_id, e.slug AS event_slug, e.title AS event_title,
       (SELECT count(*) FROM promo_redemptions r WHERE r.promo_id = p.id AND r.user_id = uv.user_id)::int AS my_uses
FROM user_vouchers uv
JOIN promo_codes p ON p.id = uv.promo_id
JOIN events e ON e.id = p.event_id
WHERE uv.user_id = $1
ORDER BY uv.saved_at DESC;
