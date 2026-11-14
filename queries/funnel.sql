-- name: IncFunnel :exec
INSERT INTO funnel_daily (day, event_id, stage, count) VALUES ($1, $2, $3, $4)
ON CONFLICT (day, event_id, stage) DO UPDATE SET count = funnel_daily.count + EXCLUDED.count;

-- name: FunnelForEvent :many
SELECT stage, sum(count)::bigint AS total FROM funnel_daily
WHERE event_id = $1 AND day >= $2 AND day <= $3 GROUP BY stage;

-- name: FunnelByDay :many
SELECT day, stage, count FROM funnel_daily WHERE event_id = $1 AND day >= $2 AND day <= $3 ORDER BY day, stage;
