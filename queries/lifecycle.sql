-- name: SetEventSchedule :one
UPDATE events SET starts_at = @starts_at, ends_at = sqlc.narg('ends_at'), doors_at = sqlc.narg('doors_at'),
  previous_starts_at = CASE WHEN schedule_state = 'scheduled' THEN starts_at ELSE previous_starts_at END,
  schedule_state = @state, schedule_changed_at = @changed_at, updated_at = now()
WHERE id = @id RETURNING *;

-- name: SetEventScheduleState :one
UPDATE events SET schedule_state = @state, schedule_changed_at = @changed_at, updated_at = now() WHERE id = @id RETURNING *;

-- name: MarkEventCancelled :one
UPDATE events SET status = 'cancelled', schedule_state = 'cancelled', schedule_changed_at = @changed_at,
  sales_paused = true, sales_paused_reason = 'event dibatalkan', updated_at = now() WHERE id = @id RETURNING *;

-- name: TicketHolderIDs :many
SELECT DISTINCT owner_user_id FROM tickets WHERE event_id = $1 AND status IN ('valid','used');

-- name: VoidUnrefundableTickets :execrows
UPDATE tickets t SET status = 'void' WHERE t.event_id = $1 AND t.status = 'valid'
  AND NOT EXISTS (SELECT 1 FROM order_items oi JOIN refunds r ON r.order_id = oi.order_id AND r.scope = 'order' AND r.status <> 'rejected' WHERE oi.id = t.order_item_id)
  AND NOT EXISTS (SELECT 1 FROM order_items oi JOIN orders o ON o.id = oi.order_id WHERE oi.id = t.order_item_id AND o.status = 'paid' AND o.total > 0);

-- name: CancelledEventsWithPaidOrders :many
SELECT DISTINCT e.id FROM events e JOIN orders o ON o.event_id = e.id AND o.status = 'paid' AND o.total > 0
WHERE e.status = 'cancelled' AND NOT EXISTS (SELECT 1 FROM refunds r WHERE r.order_id = o.id AND r.scope = 'order' AND r.status <> 'rejected')
LIMIT 50;

-- name: InsertEventCopy :exec
INSERT INTO events (id, organizer_id, venue_id, slug, title, description, genre, poster_key, starts_at, doors_at, ends_at, mode, status,
  visibility, queue_required, admit_window_sec, admit_rate, max_concurrent, queue_pre_open_min, hold_ttl_sec, max_per_order, max_per_account,
  age_min, transfer_enabled, resale_cap_pct, refund_policy, service_fee_bp, service_fee_fixed, fee_payer, tax_bp, created_by)
SELECT @new_id, organizer_id, venue_id, @slug, @title, description, genre, poster_key, @starts_at, NULL, @ends_at, mode, 'draft',
  CASE WHEN visibility = 'password' THEN 'unlisted' ELSE visibility END, queue_required, admit_window_sec, admit_rate, max_concurrent, queue_pre_open_min, hold_ttl_sec, max_per_order, max_per_account,
  age_min, transfer_enabled, resale_cap_pct, refund_policy, service_fee_bp, service_fee_fixed, fee_payer, tax_bp, @created_by
FROM events src WHERE src.id = @src_id;

-- name: EventAudienceIDs :many
SELECT u FROM (
  SELECT o.user_id AS u FROM orders o WHERE o.event_id = @event_id::text AND o.status IN ('paid','refunded')
  UNION
  SELECT t.owner_user_id FROM tickets t WHERE t.event_id = @event_id::text AND t.status IN ('valid','used','refunded')
) x;

-- name: EventsStartingBetween :many
SELECT e.*, v.name AS venue_name, v.tz AS venue_tz FROM events e JOIN venues v ON v.id = e.venue_id
WHERE e.status = 'published' AND e.schedule_state <> 'cancelled' AND e.starts_at >= @from_ts AND e.starts_at < @to_ts;
