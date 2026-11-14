-- ===== venues =====
-- name: InsertVenue :one
INSERT INTO venues (id, name, city, address, lat, lng, tz, capacity, created_by, image_key, image_credit)
VALUES ($1, $2, $3, sqlc.narg('address'), sqlc.narg('lat'), sqlc.narg('lng'), $4, sqlc.narg('capacity'), sqlc.narg('created_by'), sqlc.narg('image_key'), sqlc.narg('image_credit'))
RETURNING *;

-- name: GetVenue :one
SELECT * FROM venues WHERE id = $1;

-- name: ListVenues :many
SELECT * FROM venues
WHERE (sqlc.narg('q')::text IS NULL OR name ILIKE '%' || sqlc.narg('q') || '%')
  AND (sqlc.narg('city')::text IS NULL OR lower(city) = lower(sqlc.narg('city')))
ORDER BY name LIMIT @lim OFFSET @off;

-- name: CountVenues :one
SELECT count(*) FROM venues
WHERE (sqlc.narg('q')::text IS NULL OR name ILIKE '%' || sqlc.narg('q') || '%')
  AND (sqlc.narg('city')::text IS NULL OR lower(city) = lower(sqlc.narg('city')));

-- name: UpdateVenue :one
UPDATE venues SET
  name = COALESCE(sqlc.narg('name'), name), city = COALESCE(sqlc.narg('city'), city),
  address = COALESCE(sqlc.narg('address'), address), lat = COALESCE(sqlc.narg('lat'), lat),
  lng = COALESCE(sqlc.narg('lng'), lng), tz = COALESCE(sqlc.narg('tz'), tz), capacity = COALESCE(sqlc.narg('capacity'), capacity),
  image_key = COALESCE(sqlc.narg('image_key'), image_key), image_credit = COALESCE(sqlc.narg('image_credit'), image_credit)
WHERE id = @id RETURNING *;

-- name: ListCities :many
SELECT DISTINCT v.city FROM venues v JOIN events e ON e.venue_id = v.id
WHERE e.status = 'published' AND e.visibility = 'public' ORDER BY v.city;

-- ===== artists =====
-- name: InsertArtist :one
INSERT INTO artists (id, slug, name, bio, image_key, socials)
VALUES ($1, $2, $3, sqlc.narg('bio'), sqlc.narg('image_key'), $4) RETURNING *;

-- name: GetArtistBySlug :one
SELECT * FROM artists WHERE slug = $1;

-- name: GetArtist :one
SELECT * FROM artists WHERE id = $1;

-- name: ListArtists :many
SELECT * FROM artists WHERE (sqlc.narg('q')::text IS NULL OR name ILIKE '%' || sqlc.narg('q') || '%')
ORDER BY name LIMIT @lim OFFSET @off;

-- name: CountArtists :one
SELECT count(*) FROM artists WHERE (sqlc.narg('q')::text IS NULL OR name ILIKE '%' || sqlc.narg('q') || '%');

-- name: UpdateArtist :one
UPDATE artists SET name = COALESCE(sqlc.narg('name'), name), bio = COALESCE(sqlc.narg('bio'), bio),
  image_key = COALESCE(sqlc.narg('image_key'), image_key), socials = COALESCE(sqlc.narg('socials'), socials)
WHERE id = @id RETURNING *;

-- name: ClearEventArtists :exec
DELETE FROM event_artists WHERE event_id = $1;

-- name: AddEventArtist :exec
INSERT INTO event_artists (event_id, artist_id, billing_order) VALUES ($1, $2, $3);

-- name: ListEventArtists :many
SELECT a.id, a.slug, a.name, a.bio, a.image_key, ea.billing_order
FROM event_artists ea JOIN artists a ON a.id = ea.artist_id
WHERE ea.event_id = $1 ORDER BY ea.billing_order, a.name;

-- ===== events =====
-- name: InsertEvent :one
INSERT INTO events (id, organizer_id, venue_id, slug, title, description, genre, poster_key, starts_at, doors_at, ends_at, mode,
  queue_required, admit_window_sec, admit_rate, max_concurrent, queue_pre_open_min, hold_ttl_sec, max_per_order, max_per_account,
  age_min, refund_policy, service_fee_bp, service_fee_fixed, fee_payer, tax_bp, visibility, password_hash, publish_at, created_by)
VALUES ($1,$2,$3,$4,$5,sqlc.narg('description'),sqlc.narg('genre'),sqlc.narg('poster_key'),$6,sqlc.narg('doors_at'),sqlc.narg('ends_at'),$7,
  $8,$9,$10,$11,$12,$13,$14,$15,sqlc.narg('age_min'),$16,$17,$18,$19,$20,$21,sqlc.narg('password_hash'),sqlc.narg('publish_at'),sqlc.narg('created_by'))
RETURNING *;

-- name: GetEvent :one
SELECT * FROM events WHERE id = $1;

-- name: GetEventForUpdate :one
SELECT * FROM events WHERE id = $1 FOR UPDATE;

-- name: GetEventBySlug :one
SELECT * FROM events WHERE slug = $1;

-- name: SlugExists :one
SELECT EXISTS (SELECT 1 FROM events WHERE slug = $1);

-- name: ListOrganizerEvents :many
SELECT * FROM events
WHERE organizer_id = @org
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.narg('q')::text IS NULL OR title ILIKE '%' || sqlc.narg('q') || '%')
ORDER BY starts_at DESC LIMIT @lim OFFSET @off;

-- name: CountOrganizerEvents :one
SELECT count(*) FROM events
WHERE organizer_id = @org
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.narg('q')::text IS NULL OR title ILIKE '%' || sqlc.narg('q') || '%');

-- name: SetEventStatus :one
UPDATE events SET status = $2, review_note = sqlc.narg('note'), updated_at = now(),
  published_at = CASE WHEN $2 = 'published' AND published_at IS NULL THEN now() ELSE published_at END
WHERE id = $1 RETURNING *;

-- name: DeleteEvent :exec
DELETE FROM events WHERE id = $1;

-- name: ListEventsForReview :many
SELECT * FROM events WHERE status = COALESCE(sqlc.narg('status'), 'in_review') ORDER BY updated_at LIMIT @lim OFFSET @off;

-- name: CountEventsForReview :one
SELECT count(*) FROM events WHERE status = COALESCE(sqlc.narg('status'), 'in_review');

-- name: ListScheduledPublish :many
SELECT * FROM events WHERE status = 'approved' AND publish_at IS NOT NULL AND publish_at <= $1;

-- name: ListEventsToFinish :many
SELECT id FROM events WHERE status IN ('published','paused') AND COALESCE(ends_at, starts_at + interval '6 hours') < $1;

-- ===== tiers =====
-- name: InsertTier :one
INSERT INTO tiers (id, event_id, name, price, quota, comp_quota, color, sort)
VALUES ($1, $2, $3, $4, $5, $6, sqlc.narg('color'), $7) RETURNING *;

-- name: GetTier :one
SELECT * FROM tiers WHERE id = $1;

-- name: GetTierForUpdate :one
SELECT * FROM tiers WHERE id = $1 FOR UPDATE;

-- name: ListTiers :many
SELECT * FROM tiers WHERE event_id = $1 ORDER BY sort, name;

-- name: UpdateTier :one
UPDATE tiers SET name = COALESCE(sqlc.narg('name'), name), price = COALESCE(sqlc.narg('price'), price),
  quota = COALESCE(sqlc.narg('quota'), quota), comp_quota = COALESCE(sqlc.narg('comp_quota'), comp_quota),
  color = COALESCE(sqlc.narg('color'), color), sort = COALESCE(sqlc.narg('sort'), sort)
WHERE id = @id RETURNING *;

-- name: DeleteTier :execrows
DELETE FROM tiers t WHERE t.id = $1 AND t.sold = 0 AND t.held = 0
  AND NOT EXISTS (SELECT 1 FROM seats s WHERE s.tier_id = t.id AND s.status IN ('held','sold'))
  AND NOT EXISTS (SELECT 1 FROM order_items oi WHERE oi.tier_id = t.id);

-- name: TierAvailability :many
SELECT t.id, t.name, t.price, t.sort, t.color, t.comp_quota, t.comp_issued,
  (CASE WHEN e.mode = 'general' THEN t.quota ELSE COALESCE(s.total, 0) END)::bigint AS capacity,
  (CASE WHEN e.mode = 'general' THEN t.sold ELSE COALESCE(s.sold, 0) END)::bigint AS sold,
  (CASE WHEN e.mode = 'general' THEN t.held ELSE COALESCE(s.held, 0) END)::bigint AS held
FROM tiers t JOIN events e ON e.id = t.event_id
LEFT JOIN LATERAL (
  SELECT count(*) FILTER (WHERE status <> 'blocked') AS total, count(*) FILTER (WHERE status = 'sold') AS sold,
         count(*) FILTER (WHERE status = 'held') AS held
  FROM seats WHERE tier_id = t.id) s ON true
WHERE t.event_id = $1 ORDER BY t.sort, t.name;

-- ===== sections & seats =====
-- name: InsertSection :one
INSERT INTO sections (id, event_id, tier_id, name, svg_ref, sort) VALUES ($1, $2, sqlc.narg('tier_id'), $3, sqlc.narg('svg_ref'), $4) RETURNING *;

-- name: ListSections :many
SELECT * FROM sections WHERE event_id = $1 ORDER BY sort, name;

-- name: DeleteEventSeatmap :exec
DELETE FROM sections WHERE event_id = $1;

-- name: CountEventSeats :one
SELECT count(*) FROM seats WHERE event_id = $1;

-- name: CountEventSeatsInUse :one
SELECT count(*) FROM seats WHERE event_id = $1 AND status IN ('held','sold');

-- name: ListSeatsGeometry :many
SELECT id, section_id, tier_id, row_label, seat_no, label, x, y, accessible FROM seats WHERE event_id = $1 ORDER BY section_id, row_label, seat_no;

-- name: ListSeatStatuses :many
SELECT id, status FROM seats WHERE event_id = $1 AND status <> 'available';

-- name: SetSeatsBlocked :execrows
UPDATE seats SET status = @new_status::text WHERE event_id = @event_id AND id = ANY(@ids::text[])
  AND ((status = 'available' AND @new_status::text = 'blocked') OR (status = 'blocked' AND @new_status::text = 'available'));

-- name: SeatStatusCounts :many
SELECT status, count(*) FROM seats WHERE event_id = $1 GROUP BY status;

-- ===== phases =====
-- name: InsertPhase :one
INSERT INTO sale_phases (id, event_id, name, audience, starts_at, ends_at, queue_open_at)
VALUES ($1, $2, $3, $4, $5, $6, sqlc.narg('queue_open_at')) RETURNING *;

-- name: ListPhases :many
SELECT * FROM sale_phases WHERE event_id = $1 ORDER BY starts_at;

-- name: GetPhase :one
SELECT * FROM sale_phases WHERE id = $1;

-- name: UpdatePhase :one
UPDATE sale_phases SET name = COALESCE(sqlc.narg('name'), name), starts_at = COALESCE(sqlc.narg('starts_at'), starts_at),
  ends_at = COALESCE(sqlc.narg('ends_at'), ends_at), queue_open_at = COALESCE(sqlc.narg('queue_open_at'), queue_open_at)
WHERE id = @id RETURNING *;

-- name: DeletePhase :exec
DELETE FROM sale_phases WHERE id = $1 AND event_id = $2;

-- name: InsertPresaleCode :exec
INSERT INTO presale_codes (id, phase_id, code_hash, max_uses) VALUES ($1, $2, $3, sqlc.narg('max_uses'));

-- name: GetPresaleCodeByHash :one
SELECT pc.*, sp.event_id, sp.starts_at, sp.ends_at FROM presale_codes pc JOIN sale_phases sp ON sp.id = pc.phase_id WHERE pc.code_hash = $1;

-- name: ConsumePresaleCode :execrows
UPDATE presale_codes SET used = used + 1 WHERE id = $1 AND (max_uses IS NULL OR used < max_uses);

-- name: ListPresaleCodes :many
SELECT id, phase_id, max_uses, used FROM presale_codes WHERE phase_id = $1 ORDER BY id;

-- name: GrantPhaseAccess :execrows
INSERT INTO phase_access (phase_id, user_id, via) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING;

-- name: HasPhaseAccess :one
SELECT EXISTS (SELECT 1 FROM phase_access WHERE phase_id = $1 AND user_id = $2);

-- name: UserPhaseAccessForEvent :many
SELECT pa.phase_id FROM phase_access pa JOIN sale_phases sp ON sp.id = pa.phase_id WHERE sp.event_id = $1 AND pa.user_id = $2;

-- name: RegisterFan :exec
INSERT INTO fan_registrations (event_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING;

-- name: ListFanRegistrations :many
SELECT fr.event_id, fr.user_id, fr.status, fr.created_at, u.name, u.email
FROM fan_registrations fr JOIN users u ON u.id = fr.user_id
WHERE fr.event_id = $1 AND (sqlc.narg('status')::text IS NULL OR fr.status = sqlc.narg('status'))
ORDER BY fr.created_at LIMIT @lim OFFSET @off;

-- name: CountFanRegistrations :one
SELECT count(*) FROM fan_registrations WHERE event_id = $1 AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'));

-- name: SetFanStatus :execrows
UPDATE fan_registrations SET status = @new_status::text WHERE event_id = @event_id AND user_id = ANY(@user_ids::text[]);

-- name: RandomFansToSelect :many
SELECT user_id FROM fan_registrations
WHERE event_id = @event_id AND status = 'registered' AND user_id <> ALL(@exclude::text[])
ORDER BY random() LIMIT @lim;

-- name: GetFanRegistration :one
SELECT * FROM fan_registrations WHERE event_id = $1 AND user_id = $2;

-- ===== gates =====
-- name: InsertGate :one
INSERT INTO gates (id, event_id, name) VALUES ($1, $2, $3) RETURNING *;

-- name: ListGates :many
SELECT * FROM gates WHERE event_id = $1 ORDER BY name;

-- name: DeleteGate :exec
DELETE FROM gates WHERE id = $1 AND event_id = $2;

-- name: AssignGateStaff :exec
INSERT INTO gate_staff (event_id, user_id, gate_id) VALUES ($1, $2, sqlc.narg('gate_id'))
ON CONFLICT (event_id, user_id) DO UPDATE SET gate_id = EXCLUDED.gate_id;

-- name: RemoveGateStaff :exec
DELETE FROM gate_staff WHERE event_id = $1 AND user_id = $2;

-- name: ListGateStaff :many
SELECT gs.event_id, gs.user_id, gs.gate_id, u.name, u.email FROM gate_staff gs JOIN users u ON u.id = gs.user_id WHERE gs.event_id = $1;

-- name: GetGateStaff :one
SELECT * FROM gate_staff WHERE event_id = $1 AND user_id = $2;

-- ===== addons =====
-- name: InsertAddon :one
INSERT INTO addons (id, event_id, name, price, quota) VALUES ($1, $2, $3, $4, $5) RETURNING *;

-- name: ListAddons :many
SELECT * FROM addons WHERE event_id = $1 AND (NOT @only_active::boolean OR active) ORDER BY name;

-- name: GetAddon :one
SELECT * FROM addons WHERE id = $1;

-- name: UpdateAddon :one
UPDATE addons SET name = COALESCE(sqlc.narg('name'), name), price = COALESCE(sqlc.narg('price'), price),
  quota = COALESCE(sqlc.narg('quota'), quota), active = COALESCE(sqlc.narg('active'), active)
WHERE id = @id RETURNING *;

-- ===== media =====
-- name: InsertMedia :one
INSERT INTO media_assets (id, owner_user_id, kind, object_key, content_type, width, height, variants)
VALUES ($1, sqlc.narg('owner_user_id'), $2, $3, $4, sqlc.narg('width'), sqlc.narg('height'), $5) RETURNING *;

-- name: GetMedia :one
SELECT * FROM media_assets WHERE id = $1;

-- ===== favorit, pengingat, follow =====
-- name: AddFavorite :exec
INSERT INTO favorites (user_id, event_id) VALUES ($1, $2) ON CONFLICT DO NOTHING;

-- name: RemoveFavorite :exec
DELETE FROM favorites WHERE user_id = $1 AND event_id = $2;

-- name: ListFavoriteEventIDs :many
SELECT event_id FROM favorites WHERE user_id = $1 ORDER BY created_at DESC;

-- name: UpsertReminder :exec
INSERT INTO reminders (user_id, event_id, remind_at) VALUES ($1, $2, $3)
ON CONFLICT (user_id, event_id) DO UPDATE SET remind_at = EXCLUDED.remind_at, sent_at = NULL;

-- name: DeleteReminder :exec
DELETE FROM reminders WHERE user_id = $1 AND event_id = $2;

-- name: DueReminders :many
SELECT r.user_id, r.event_id FROM reminders r WHERE r.sent_at IS NULL AND r.remind_at <= $1 ORDER BY r.remind_at LIMIT $2;

-- name: MarkReminderSent :exec
UPDATE reminders SET sent_at = now() WHERE user_id = $1 AND event_id = $2;

-- name: AddFollow :exec
INSERT INTO follows (user_id, kind, target) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING;

-- name: RemoveFollow :exec
DELETE FROM follows WHERE user_id = $1 AND kind = $2 AND target = $3;

-- name: ListFollows :many
SELECT kind, target, created_at FROM follows WHERE user_id = $1 ORDER BY created_at DESC;

-- name: FollowersOf :many
SELECT DISTINCT user_id FROM follows WHERE (kind = 'artist' AND target = ANY(@artist_ids::text[]))
   OR (kind = 'venue' AND target = @venue_id) OR (kind = 'city' AND lower(target) = lower(@city::text))
   OR (kind = 'genre' AND lower(target) = lower(@genre::text));

-- name: RecommendedEvents :many
SELECT e.id,
  EXISTS (SELECT 1 FROM follows f JOIN event_artists ea ON ea.artist_id = f.target WHERE f.user_id = @uid::text AND f.kind = 'artist' AND ea.event_id = e.id) AS by_artist,
  EXISTS (SELECT 1 FROM follows f WHERE f.user_id = @uid::text AND f.kind = 'venue' AND f.target = e.venue_id) AS by_venue,
  EXISTS (SELECT 1 FROM follows f WHERE f.user_id = @uid::text AND f.kind = 'city' AND lower(f.target) = lower(v.city)) AS by_city,
  EXISTS (SELECT 1 FROM follows f WHERE f.user_id = @uid::text AND f.kind = 'genre' AND lower(f.target) = lower(e.genre)) AS by_genre,
  EXISTS (SELECT 1 FROM tickets t JOIN events pe ON pe.id = t.event_id WHERE t.owner_user_id = @uid::text AND t.status = 'used' AND pe.genre IS NOT NULL AND lower(pe.genre) = lower(e.genre)) AS by_history
FROM events e JOIN venues v ON v.id = e.venue_id
WHERE e.status = 'published' AND e.visibility = 'public' AND e.starts_at > now()
  AND NOT EXISTS (SELECT 1 FROM tickets t WHERE t.owner_user_id = @uid::text AND t.event_id = e.id AND t.status IN ('valid','used'))
ORDER BY e.starts_at LIMIT 300;

-- name: UpcomingTicketEvents :many
SELECT DISTINCT e.id FROM tickets t JOIN events e ON e.id = t.event_id
WHERE t.owner_user_id = $1 AND t.status = 'valid' AND COALESCE(e.ends_at, e.starts_at + interval '6 hours') > now() AND e.status IN ('published','paused');
