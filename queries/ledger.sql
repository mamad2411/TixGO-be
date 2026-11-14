-- name: UpsertLedgerAccount :exec
INSERT INTO ledger_accounts (id, kind, organizer_id) VALUES ($1, $2, sqlc.narg('organizer_id'))
ON CONFLICT (kind, organizer_id) DO NOTHING;

-- name: GetLedgerAccount :one
SELECT * FROM ledger_accounts WHERE kind = $1 AND organizer_id IS NOT DISTINCT FROM sqlc.narg('organizer_id');

-- name: InsertLedgerTx :execrows
INSERT INTO ledger_transactions (id, ref_type, ref_id, memo) VALUES ($1, $2, $3, sqlc.narg('memo'))
ON CONFLICT (ref_type, ref_id) DO NOTHING;

-- name: InsertLedgerEntry :exec
INSERT INTO ledger_entries (tx_id, account_id, debit, credit) VALUES ($1, $2, $3, $4);

-- name: LedgerAccountBalance :one
SELECT (COALESCE(sum(credit), 0) - COALESCE(sum(debit), 0))::bigint FROM ledger_entries WHERE account_id = $1;

-- name: OrganizerBalances :many
SELECT la.kind, (COALESCE(sum(le.credit), 0) - COALESCE(sum(le.debit), 0))::bigint AS balance
FROM ledger_accounts la LEFT JOIN ledger_entries le ON le.account_id = la.id
WHERE la.organizer_id = $1 GROUP BY la.kind;

-- name: LedgerTrialBalance :many
SELECT la.kind, COALESCE(sum(le.debit), 0)::bigint AS debit, COALESCE(sum(le.credit), 0)::bigint AS credit
FROM ledger_accounts la LEFT JOIN ledger_entries le ON le.account_id = la.id GROUP BY la.kind ORDER BY la.kind;

-- name: LedgerStatement :many
SELECT lt.created_at, lt.ref_type, lt.ref_id, lt.memo, la.kind, le.debit, le.credit
FROM ledger_entries le JOIN ledger_transactions lt ON lt.id = le.tx_id JOIN ledger_accounts la ON la.id = le.account_id
WHERE la.organizer_id = $1 ORDER BY lt.created_at DESC, le.id DESC LIMIT $2 OFFSET $3;

-- name: CountLedgerStatement :one
SELECT count(*) FROM ledger_entries le JOIN ledger_accounts la ON la.id = le.account_id WHERE la.organizer_id = $1;
