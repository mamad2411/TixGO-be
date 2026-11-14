-- name: ListMediaByOwner :many
SELECT * FROM media_assets WHERE owner_user_id = $1 ORDER BY created_at DESC LIMIT $2;
