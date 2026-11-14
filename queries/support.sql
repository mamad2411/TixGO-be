-- name: InsertSupportTicket :one
INSERT INTO support_tickets (id, user_id, category, subject, order_id, event_id, ticket_id, priority)
VALUES ($1, $2, $3, $4, sqlc.narg('order_id'), sqlc.narg('event_id'), sqlc.narg('ticket_id'), $5) RETURNING *;

-- name: GetSupportTicket :one
SELECT * FROM support_tickets WHERE id = $1;

-- name: GetSupportTicketForUpdate :one
SELECT * FROM support_tickets WHERE id = $1 FOR UPDATE;

-- name: ListUserSupportTickets :many
SELECT * FROM support_tickets WHERE user_id = $1 ORDER BY updated_at DESC LIMIT $2 OFFSET $3;

-- name: InsertSupportMessage :one
INSERT INTO support_messages (id, ticket_id, author_id, internal, body) VALUES ($1, $2, $3, $4, $5) RETURNING *;

-- name: ListSupportMessages :many
SELECT m.*, u.name AS author_name, u.role AS author_role FROM support_messages m JOIN users u ON u.id = m.author_id
WHERE m.ticket_id = @ticket_id AND (@include_internal::boolean OR NOT m.internal) ORDER BY m.created_at, m.id;

-- name: SetSupportStatus :exec
UPDATE support_tickets SET status = @status, updated_at = now(),
  first_response_at = CASE WHEN @mark_response::boolean AND first_response_at IS NULL THEN now() ELSE first_response_at END WHERE id = @id;

-- name: SetSupportAssignee :exec
UPDATE support_tickets SET assignee_id = $2, updated_at = now() WHERE id = $1;

-- name: SetSupportPriority :exec
UPDATE support_tickets SET priority = $2, updated_at = now() WHERE id = $1;

-- name: AgentSupportQueue :many
SELECT t.*, u.email AS user_email FROM support_tickets t JOIN users u ON u.id = t.user_id
WHERE (sqlc.narg('status')::text IS NULL OR t.status = sqlc.narg('status'))
  AND (sqlc.narg('category')::text IS NULL OR t.category = sqlc.narg('category'))
  AND (sqlc.narg('priority')::text IS NULL OR t.priority = sqlc.narg('priority'))
  AND (sqlc.narg('assignee')::text IS NULL OR t.assignee_id = sqlc.narg('assignee'))
ORDER BY (t.priority = 'high') DESC, t.created_at LIMIT @lim OFFSET @off;

-- name: CountAgentSupportQueue :one
SELECT count(*) FROM support_tickets t
WHERE (sqlc.narg('status')::text IS NULL OR t.status = sqlc.narg('status'))
  AND (sqlc.narg('category')::text IS NULL OR t.category = sqlc.narg('category'))
  AND (sqlc.narg('priority')::text IS NULL OR t.priority = sqlc.narg('priority'))
  AND (sqlc.narg('assignee')::text IS NULL OR t.assignee_id = sqlc.narg('assignee'));

-- name: EscalateStalePaymentTickets :execrows
UPDATE support_tickets SET priority = 'high', updated_at = now()
WHERE status = 'open' AND priority = 'normal' AND category = 'payment' AND created_at < @before;

-- name: CloseStalePendingCustomer :many
UPDATE support_tickets SET status = 'closed', updated_at = now() WHERE status = 'pending_customer' AND updated_at < @before RETURNING id, user_id, number;

-- name: CloseOldResolved :execrows
UPDATE support_tickets SET status = 'closed', updated_at = now() WHERE status = 'resolved' AND updated_at < @before;

-- name: SupportOrderSummary :one
SELECT o.id, o.status, o.total, o.event_id, o.created_at, o.paid_at, e.title AS event_title,
  (SELECT count(*) FROM tickets t JOIN order_items i ON i.id = t.order_item_id WHERE i.order_id = o.id)::int AS tickets,
  (SELECT p.status FROM payments p WHERE p.order_id = o.id ORDER BY p.created_at DESC LIMIT 1) AS payment_status
FROM orders o JOIN events e ON e.id = o.event_id WHERE o.id = $1;
