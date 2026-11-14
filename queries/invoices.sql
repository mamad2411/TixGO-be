-- name: NextCounter :one
INSERT INTO invoice_counters (kind, year, last) VALUES ($1, $2, 1)
ON CONFLICT (kind, year) DO UPDATE SET last = invoice_counters.last + 1 RETURNING last;

-- name: InsertInvoice :one
INSERT INTO invoices (id, order_id, number, year, seq, subtotal, discount, fee, payment_fee, tax, total)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING *;

-- name: GetInvoiceByOrder :one
SELECT * FROM invoices WHERE order_id = $1;

-- name: InsertCreditNote :one
INSERT INTO credit_notes (id, invoice_id, refund_id, number, amount) VALUES ($1, $2, sqlc.narg('refund_id'), $3, $4) RETURNING *;

-- name: ListCreditNotes :many
SELECT * FROM credit_notes WHERE invoice_id = $1 ORDER BY issued_at;

-- name: ListEventInvoices :many
SELECT i.*, o.user_id FROM invoices i JOIN orders o ON o.id = i.order_id WHERE o.event_id = $1 ORDER BY i.issued_at DESC LIMIT $2 OFFSET $3;
