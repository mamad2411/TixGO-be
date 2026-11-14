-- name: LatestPublishedLegal :one
SELECT * FROM legal_documents
WHERE kind = $1 AND locale = $2 AND published_at IS NOT NULL
ORDER BY version DESC LIMIT 1;

-- name: ListLegalVersions :many
SELECT id, kind, locale, version, title, published_at, created_at FROM legal_documents
WHERE kind = $1 AND locale = $2 ORDER BY version DESC;

-- name: GetLegalByID :one
SELECT * FROM legal_documents WHERE id = $1;

-- name: NextLegalVersion :one
SELECT COALESCE(max(version), 0) + 1 FROM legal_documents WHERE kind = $1 AND locale = $2;

-- name: InsertLegalDocument :one
INSERT INTO legal_documents (id, kind, locale, version, title, body, created_by)
VALUES ($1, $2, $3, $4, $5, $6, sqlc.narg('created_by'))
RETURNING *;

-- name: PublishLegalDocument :one
UPDATE legal_documents SET published_at = now() WHERE id = $1 AND published_at IS NULL RETURNING *;

-- name: UpdateLegalDraft :one
UPDATE legal_documents SET title = $2, body = $3 WHERE id = $1 AND published_at IS NULL RETURNING *;

-- name: RecordConsent :exec
INSERT INTO consents (id, user_id, document_id, ip_hash) VALUES ($1, $2, $3, sqlc.narg('ip_hash'))
ON CONFLICT (user_id, document_id) DO NOTHING;

-- name: ListUserConsents :many
SELECT c.id, c.document_id, c.accepted_at, d.kind, d.locale, d.version, d.title
FROM consents c JOIN legal_documents d ON d.id = c.document_id
WHERE c.user_id = $1 ORDER BY c.accepted_at DESC;

-- name: PendingConsentDocs :many
SELECT l.id, l.kind, l.version, l.title FROM (
  SELECT DISTINCT ON (kind) id, kind, version, title FROM legal_documents
  WHERE published_at IS NOT NULL AND kind IN ('terms','privacy') AND locale = 'id'
  ORDER BY kind, version DESC
) l
WHERE NOT EXISTS (
  SELECT 1 FROM consents c JOIN legal_documents d ON d.id = c.document_id
  WHERE c.user_id = $1 AND d.kind = l.kind AND d.version >= l.version
);

-- name: GetCookieConsent :one
SELECT * FROM cookie_consents WHERE subject_key = $1;

-- name: UpsertCookieConsent :one
INSERT INTO cookie_consents (subject_key, user_id, analytics, marketing, updated_at)
VALUES ($1, sqlc.narg('user_id'), $2, $3, now())
ON CONFLICT (subject_key) DO UPDATE SET analytics = EXCLUDED.analytics, marketing = EXCLUDED.marketing,
  user_id = COALESCE(EXCLUDED.user_id, cookie_consents.user_id), updated_at = now()
RETURNING *;
