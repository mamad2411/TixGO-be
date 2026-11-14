-- name: InsertReview :one
INSERT INTO reviews (id, ticket_id, event_id, user_id, rating, body, status) VALUES ($1, $2, $3, $4, $5, sqlc.narg('body'), $6) RETURNING *;

-- name: GetReview :one
SELECT * FROM reviews WHERE id = $1;

-- name: ListEventReviews :many
SELECT r.id, r.rating, r.body, r.organizer_reply, r.created_at, u.name AS author FROM reviews r JOIN users u ON u.id = r.user_id
WHERE r.event_id = $1 AND r.status = 'published' ORDER BY r.created_at DESC LIMIT $2 OFFSET $3;

-- name: EventRatingAgg :one
SELECT count(*)::int AS n, COALESCE(avg(rating), 0)::float8 AS avg FROM reviews WHERE event_id = $1 AND status = 'published';

-- name: SetReviewStatus :execrows
UPDATE reviews SET status = $2 WHERE id = $1;

-- name: SetReviewReply :execrows
UPDATE reviews SET organizer_reply = $3 WHERE id = $1 AND event_id = $2 AND status = 'published';

-- name: InsertReviewReport :execrows
INSERT INTO review_reports (id, review_id, reporter_id, reason) VALUES ($1, $2, $3, $4) ON CONFLICT (review_id, reporter_id) DO NOTHING;

-- name: CountReviewReports :one
SELECT count(*)::int FROM review_reports WHERE review_id = $1;

-- name: ModerationReviews :many
SELECT r.*, (SELECT count(*) FROM review_reports rr WHERE rr.review_id = r.id)::int AS reports FROM reviews r
WHERE r.status = COALESCE(sqlc.narg('status'), 'pending') ORDER BY r.created_at LIMIT @lim OFFSET @off;

-- name: InsertEventPhoto :one
INSERT INTO event_photos (id, event_id, user_id, object_key, rights_confirmed) VALUES ($1, $2, $3, $4, true) RETURNING *;

-- name: ListEventPhotos :many
SELECT id, object_key, created_at FROM event_photos WHERE event_id = $1 AND status = 'published' ORDER BY created_at DESC LIMIT 100;

-- name: ModerationPhotos :many
SELECT * FROM event_photos WHERE status = COALESCE(sqlc.narg('status'), 'pending') ORDER BY created_at LIMIT @lim OFFSET @off;

-- name: SetPhotoStatus :execrows
UPDATE event_photos SET status = $2 WHERE id = $1;

-- name: UserUsedTicketForEvent :one
SELECT * FROM tickets WHERE owner_user_id = $1 AND event_id = $2 AND status = 'used' ORDER BY used_at LIMIT 1;

-- name: PhotoIsPublished :one
SELECT EXISTS (SELECT 1 FROM event_photos WHERE object_key = $1 AND status = 'published');
