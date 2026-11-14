-- name: PassportOptedOut :one
SELECT EXISTS (SELECT 1 FROM passport_badges WHERE user_id = $1 AND badge = '_optout');

-- name: AwardPoints :execrows
INSERT INTO passport_points (user_id, event_id, ticket_id, points, reason) VALUES ($1, $2, $3, $4, $5) ON CONFLICT (user_id, ticket_id, reason) DO NOTHING;

-- name: SpendPoints :exec
INSERT INTO passport_points (user_id, event_id, points, reason) VALUES ($1, $2, $3, $4);

-- name: PointsBalance :one
SELECT COALESCE(sum(points), 0)::int FROM passport_points WHERE user_id = $1;

-- name: AwardBadge :execrows
INSERT INTO passport_badges (user_id, badge) VALUES ($1, $2) ON CONFLICT DO NOTHING;

-- name: ListBadges :many
SELECT badge, awarded_at FROM passport_badges WHERE user_id = $1 AND badge <> '_optout' ORDER BY awarded_at;

-- name: PassportStamps :many
SELECT t.id AS ticket_id, e.id AS event_id, e.title, e.slug, e.genre, v.name AS venue, v.city, t.used_at
FROM tickets t JOIN events e ON e.id = t.event_id JOIN venues v ON v.id = e.venue_id
WHERE t.owner_user_id = $1 AND t.status = 'used' ORDER BY t.used_at DESC LIMIT 200;

-- name: PassportPrior :one
SELECT count(*)::int AS shows,
  count(*) FILTER (WHERE lower(v.city) = lower(@city::text))::int AS same_city,
  count(*) FILTER (WHERE e.genre IS NOT NULL AND lower(e.genre) = lower(@genre::text))::int AS same_genre,
  count(DISTINCT v.id)::int AS venues
FROM tickets t JOIN events e ON e.id = t.event_id JOIN venues v ON v.id = e.venue_id
WHERE t.owner_user_id = @user_id::text AND t.status = 'used' AND t.id <> @ticket_id::text;

-- name: TicketWasPresale :one
SELECT EXISTS (
  SELECT 1 FROM order_items oi JOIN tickets t ON t.order_item_id = oi.id JOIN orders o ON o.id = oi.order_id
  JOIN sale_phases sp ON sp.event_id = o.event_id AND sp.audience <> 'public' AND o.created_at >= sp.starts_at AND o.created_at < sp.ends_at
  WHERE t.id = $1);

-- name: DeletePassportData :exec
DELETE FROM passport_points WHERE user_id = $1;

-- name: DeletePassportBadges :exec
DELETE FROM passport_badges WHERE user_id = $1;

-- name: SetPassportOptOut :exec
INSERT INTO passport_badges (user_id, badge) VALUES ($1, '_optout') ON CONFLICT DO NOTHING;

-- name: ClearPassportOptOut :exec
DELETE FROM passport_badges WHERE user_id = $1 AND badge = '_optout';

-- name: FanPhaseForEvent :one
SELECT * FROM sale_phases WHERE event_id = $1 AND audience = 'fan' AND ends_at > $2 ORDER BY starts_at LIMIT 1;

-- name: GrantPassportAccess :execrows
INSERT INTO phase_access (phase_id, user_id, via) VALUES ($1, $2, 'passport') ON CONFLICT DO NOTHING;
