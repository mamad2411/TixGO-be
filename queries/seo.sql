-- name: SitemapEvents :many
SELECT e.slug, e.updated_at FROM events e WHERE e.status IN ('published','paused','finished') AND e.visibility = 'public' AND EXISTS (SELECT 1 FROM organizers org WHERE org.id = e.organizer_id AND org.status = 'active') ORDER BY e.updated_at DESC LIMIT 5000;

-- name: SitemapArtists :many
SELECT a.slug, a.created_at AS updated_at FROM artists a WHERE EXISTS (SELECT 1 FROM event_artists ea JOIN events e ON e.id = ea.event_id WHERE ea.artist_id = a.id AND e.status IN ('published','paused') AND e.visibility = 'public') ORDER BY a.slug LIMIT 5000;
