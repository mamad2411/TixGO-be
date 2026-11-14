-- name: InsertCMSPage :one
INSERT INTO cms_pages (id, kind, slug, locale, title, body, category, seo, version, updated_by)
VALUES ($1, $2, $3, $4, $5, $6, sqlc.narg('category'), $7, $8, $9) RETURNING *;

-- name: GetCMSPage :one
SELECT * FROM cms_pages WHERE id = $1;

-- name: GetCMSPageForUpdate :one
SELECT * FROM cms_pages WHERE id = $1 FOR UPDATE;

-- name: LatestCMSVersion :one
SELECT * FROM cms_pages WHERE kind = $1 AND slug = $2 AND locale = $3 ORDER BY version DESC LIMIT 1;

-- name: PublishedCMSPage :one
SELECT * FROM cms_pages WHERE kind = $1 AND slug = $2 AND locale = $3 AND status = 'published' ORDER BY version DESC LIMIT 1;

-- name: UpdateCMSDraft :one
UPDATE cms_pages SET title = $2, body = $3, category = sqlc.narg('category'), seo = $4, updated_by = $5, updated_at = now() WHERE id = $1 RETURNING *;

-- name: SetCMSStatus :one
UPDATE cms_pages SET status = $2, publish_at = sqlc.narg('publish_at'), updated_by = $3, updated_at = now() WHERE id = $1 RETURNING *;

-- name: ArchiveOtherPublished :execrows
UPDATE cms_pages SET status = 'archived', updated_at = now() WHERE kind = $1 AND slug = $2 AND locale = $3 AND status = 'published' AND id <> $4;

-- name: ListCMSPages :many
SELECT * FROM cms_pages WHERE (sqlc.narg('kind')::text IS NULL OR kind = sqlc.narg('kind')) AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.narg('q')::text IS NULL OR title ILIKE '%' || sqlc.narg('q') || '%') ORDER BY updated_at DESC LIMIT @lim OFFSET @off;

-- name: CMSVersions :many
SELECT id, version, status, title, updated_by, updated_at FROM cms_pages WHERE kind = $1 AND slug = $2 AND locale = $3 ORDER BY version DESC;

-- name: ListPublishedCMS :many
SELECT id, slug, title, category, seo, updated_at FROM cms_pages
WHERE kind = @kind AND locale = @locale AND status = 'published' AND (sqlc.narg('category')::text IS NULL OR category = sqlc.narg('category')) ORDER BY title LIMIT 200;

-- name: SearchHelp :many
SELECT id, slug, title, category, left(body, 200) AS snippet FROM cms_pages
WHERE kind = 'help' AND locale = @locale AND status = 'published' AND (title ILIKE '%' || @q::text || '%' OR body ILIKE '%' || @q::text || '%')
ORDER BY (title ILIKE '%' || @q::text || '%') DESC, title LIMIT 20;

-- name: DueScheduledCMS :many
SELECT * FROM cms_pages WHERE status = 'in_review' AND publish_at IS NOT NULL AND publish_at <= $1 LIMIT 100;

-- name: InsertBanner :one
INSERT INTO banners (id, title, image_key, link, starts_at, ends_at, sort) VALUES ($1, $2, $3, sqlc.narg('link'), $4, sqlc.narg('ends_at'), $5) RETURNING *;

-- name: ListBanners :many
SELECT * FROM banners ORDER BY starts_at DESC LIMIT 100;

-- name: ActiveBanners :many
SELECT * FROM banners WHERE starts_at <= @now AND (ends_at IS NULL OR ends_at > @now) ORDER BY sort, starts_at DESC;

-- name: DeleteBanner :execrows
DELETE FROM banners WHERE id = $1;

-- name: UpsertCollection :one
INSERT INTO collections (id, slug, title, event_ids, starts_at, ends_at) VALUES ($1, $2, $3, $4, sqlc.narg('starts_at'), sqlc.narg('ends_at'))
ON CONFLICT (slug) DO UPDATE SET title = EXCLUDED.title, event_ids = EXCLUDED.event_ids, starts_at = EXCLUDED.starts_at, ends_at = EXCLUDED.ends_at RETURNING *;

-- name: GetCollectionBySlug :one
SELECT * FROM collections WHERE slug = $1;

-- name: ListCollections :many
SELECT * FROM collections ORDER BY slug;

-- name: DeleteCollection :execrows
DELETE FROM collections WHERE id = $1;
