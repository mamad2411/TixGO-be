-- name: SalesSeries :many
SELECT date_trunc(@bucket::text, o.paid_at)::timestamptz AS bucket, count(*)::int AS orders, COALESCE(sum(o.total), 0)::bigint AS revenue,
  (SELECT count(*) FROM order_items oi WHERE oi.order_id = ANY(array_agg(o.id)))::int AS tickets
FROM orders o WHERE o.event_id = @event_id::text AND o.status IN ('paid','refunded') AND o.paid_at >= @from_ts AND o.paid_at < @to_ts
GROUP BY 1 ORDER BY 1;

-- name: GateFlow :many
SELECT date_bin('5 minutes', scanned_at, TIMESTAMPTZ '2000-01-01')::timestamptz AS bucket, COALESCE(gate_id, '') AS gate_id, count(*)::int AS entered
FROM gate_scans WHERE event_id = @event_id::text AND result = 'OK' AND scanned_at >= @from_ts AND scanned_at < @to_ts GROUP BY 1, 2 ORDER BY 1, 2;

-- name: GlobalFunnel :many
SELECT stage, sum(count)::bigint AS total FROM funnel_daily WHERE day >= @from_day AND day <= @to_day GROUP BY stage;

-- name: MedianCheckoutSeconds :one
SELECT COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM (paid_at - created_at))), 0)::float8 AS median_sec, count(*)::int AS n
FROM orders WHERE status IN ('paid','refunded') AND paid_at >= @from_ts AND paid_at < @to_ts AND total > 0;

-- name: PaymentOutcomeByMethod :many
SELECT COALESCE(method, 'unknown') AS method, count(*) FILTER (WHERE status = 'settled')::int AS settled,
  count(*) FILTER (WHERE status = 'failed')::int AS failed, count(*) FILTER (WHERE status = 'expired')::int AS expired, count(*)::int AS total
FROM payments WHERE created_at >= @from_ts AND created_at < @to_ts GROUP BY 1 ORDER BY total DESC;
