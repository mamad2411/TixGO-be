-- name: CreateOrganizer :one
INSERT INTO organizers (id, name, owner_user_id, type, legal_name, contact_email, contact_phone, address, kyb_status)
VALUES ($1, $2, $3, $4, sqlc.narg('legal_name'), sqlc.narg('contact_email'), sqlc.narg('contact_phone'), sqlc.narg('address'), 'draft')
RETURNING *;

-- name: GetOrganizer :one
SELECT * FROM organizers WHERE id = $1;

-- name: GetOrganizerByOwner :one
SELECT * FROM organizers WHERE owner_user_id = $1;

-- name: AddOrganizerMember :exec
INSERT INTO organizer_members (organizer_id, user_id, role) VALUES ($1, $2, $3)
ON CONFLICT (organizer_id, user_id) DO UPDATE SET role = EXCLUDED.role;

-- name: RemoveOrganizerMember :exec
DELETE FROM organizer_members WHERE organizer_id = $1 AND user_id = $2 AND role <> 'owner';

-- name: GetMembership :one
SELECT om.organizer_id, om.role, o.kyb_status, o.status AS organizer_status
FROM organizer_members om JOIN organizers o ON o.id = om.organizer_id
WHERE om.user_id = $1
ORDER BY (om.role = 'owner') DESC
LIMIT 1;

-- name: ListOrganizerMembers :many
SELECT om.user_id, om.role, om.created_at, u.email, u.name
FROM organizer_members om JOIN users u ON u.id = om.user_id
WHERE om.organizer_id = $1 ORDER BY om.created_at;

-- name: UpdateOrganizerInfo :one
UPDATE organizers SET
  name = COALESCE(sqlc.narg('name'), name),
  type = COALESCE(sqlc.narg('type'), type),
  legal_name = COALESCE(sqlc.narg('legal_name'), legal_name),
  contact_email = COALESCE(sqlc.narg('contact_email'), contact_email),
  contact_phone = COALESCE(sqlc.narg('contact_phone'), contact_phone),
  address = COALESCE(sqlc.narg('address'), address),
  tax_id_enc = COALESCE(sqlc.narg('tax_id_enc'), tax_id_enc),
  updated_at = now()
WHERE id = @id
RETURNING *;

-- name: AcceptOrganizerContract :exec
UPDATE organizers SET contract_accepted_at = now(), contract_document_id = $2, updated_at = now() WHERE id = $1;

-- name: SubmitKYB :one
UPDATE organizers SET kyb_status = 'submitted', kyb_reason = NULL, updated_at = now()
WHERE id = $1 AND kyb_status IN ('draft','needs_info','rejected')
RETURNING *;

-- name: ReviewKYB :one
UPDATE organizers SET kyb_status = $2, kyb_reason = sqlc.narg('reason'), kyb_reviewed_by = $3, kyb_reviewed_at = now(), updated_at = now()
WHERE id = $1 AND kyb_status IN ('submitted','in_review')
RETURNING *;

-- name: SetOrganizerInReview :exec
UPDATE organizers SET kyb_status = 'in_review', updated_at = now() WHERE id = $1 AND kyb_status = 'submitted';

-- name: SetOrganizerStatus :exec
UPDATE organizers SET status = $2, updated_at = now() WHERE id = $1;

-- name: ListOrganizersByKYB :many
SELECT * FROM organizers
WHERE (sqlc.narg('status')::text IS NULL OR kyb_status = sqlc.narg('status'))
ORDER BY updated_at DESC LIMIT @lim OFFSET @off;

-- name: CountOrganizersByKYB :one
SELECT count(*) FROM organizers WHERE (sqlc.narg('status')::text IS NULL OR kyb_status = sqlc.narg('status'));

-- name: InsertOrganizerDocument :exec
INSERT INTO organizer_documents (id, organizer_id, kind, object_key) VALUES ($1, $2, $3, $4);

-- name: ListOrganizerDocuments :many
SELECT * FROM organizer_documents WHERE organizer_id = $1 ORDER BY created_at;

-- name: GetOrganizerDocument :one
SELECT * FROM organizer_documents WHERE id = $1 AND organizer_id = $2;

-- name: DeleteOrganizerDocument :exec
DELETE FROM organizer_documents WHERE id = $1 AND organizer_id = $2;

-- name: InsertBankAccount :one
INSERT INTO bank_accounts (id, organizer_id, bank_code, account_no_enc, account_no_last4, account_name, verified_at, active_from)
VALUES ($1, $2, $3, $4, $5, $6, sqlc.narg('verified_at'), $7)
RETURNING *;

-- name: ListBankAccounts :many
SELECT * FROM bank_accounts WHERE organizer_id = $1 AND active ORDER BY created_at DESC;

-- name: GetBankAccount :one
SELECT * FROM bank_accounts WHERE id = $1;

-- name: GetUsableBankAccount :one
SELECT * FROM bank_accounts
WHERE organizer_id = $1 AND active AND verified_at IS NOT NULL AND active_from <= $2
ORDER BY created_at DESC LIMIT 1;

-- name: VerifyBankAccount :exec
UPDATE bank_accounts SET verified_at = now() WHERE id = $1 AND verified_at IS NULL;

-- name: VerifyOrganizerBankAccounts :exec
UPDATE bank_accounts SET verified_at = now() WHERE organizer_id = $1 AND verified_at IS NULL;

-- name: DeactivateBankAccount :exec
UPDATE bank_accounts SET active = false WHERE id = $1 AND organizer_id = $2;

-- name: GetUserByEmailForTeam :one
SELECT id, email, name, role FROM users WHERE email = $1 AND status = 'active';

-- name: PauseOrgEventsForSuspension :exec
UPDATE events SET sales_paused = true, sales_paused_reason = 'organizer ditangguhkan', updated_at = now()
WHERE organizer_id = $1 AND status = 'published' AND NOT sales_paused;

-- name: ResumeOrgEventsAfterSuspension :exec
UPDATE events SET sales_paused = false, sales_paused_reason = NULL, updated_at = now()
WHERE organizer_id = $1 AND sales_paused AND sales_paused_reason = 'organizer ditangguhkan';
