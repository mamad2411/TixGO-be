-- name: InsertSquad :one
INSERT INTO squads (id, event_id, leader_id, code, hold_id, size, policy, status) VALUES ($1, $2, $3, $4, $5, $6, $7, 'holding') RETURNING *;

-- name: GetSquad :one
SELECT * FROM squads WHERE id = $1;

-- name: GetSquadForUpdate :one
SELECT * FROM squads WHERE id = $1 FOR UPDATE;

-- name: GetSquadByCode :one
SELECT * FROM squads WHERE code = $1;

-- name: SetSquadStatus :exec
UPDATE squads SET status = $2 WHERE id = $1;

-- name: InsertSquadMember :exec
INSERT INTO squad_members (squad_id, user_id, seat_id) VALUES ($1, $2, sqlc.narg('seat_id'));

-- name: ListSquadMembers :many
SELECT m.*, u.name AS user_name FROM squad_members m JOIN users u ON u.id = m.user_id WHERE m.squad_id = $1 ORDER BY m.joined_at, m.user_id;

-- name: GetSquadMemberForUpdate :one
SELECT * FROM squad_members WHERE squad_id = $1 AND user_id = $2 FOR UPDATE;

-- name: SetSquadMemberHold :exec
UPDATE squad_members SET hold_id = $3 WHERE squad_id = $1 AND user_id = $2;

-- name: SetSquadMemberOrder :exec
UPDATE squad_members SET order_id = $3 WHERE squad_id = $1 AND user_id = $2;

-- name: SetSquadMemberStatus :exec
UPDATE squad_members SET status = $3 WHERE squad_id = $1 AND user_id = $2;

-- name: MarkSquadMemberPaidByOrder :one
UPDATE squad_members SET status = 'paid' WHERE order_id = $1 AND status = 'joined' RETURNING squad_id;

-- name: SquadsOfUser :many
SELECT s.* FROM squads s JOIN squad_members m ON m.squad_id = s.id WHERE m.user_id = $1 ORDER BY s.created_at DESC LIMIT 50;

-- name: MoveSeatHoldItem :execrows
UPDATE hold_items SET hold_id = @to_hold WHERE hold_id = @from_hold AND seat_id = @seat_id;

-- name: TakeOneFromTierHoldItem :one
UPDATE hold_items SET qty = qty - 1 WHERE id = (SELECT hi.id FROM hold_items hi WHERE hi.hold_id = @from_hold::text AND hi.seat_id IS NULL AND hi.qty > 0 ORDER BY hi.id LIMIT 1) RETURNING tier_id, qty;

-- name: DeleteEmptyHoldItems :exec
DELETE FROM hold_items WHERE hold_id = $1 AND qty <= 0;

-- name: HoldSeatIDs :many
SELECT seat_id FROM hold_items WHERE hold_id = $1 AND seat_id IS NOT NULL ORDER BY seat_id;

-- name: SquadsDue :many
SELECT s.* FROM squads s JOIN holds h ON h.id = s.hold_id WHERE s.status = 'holding' AND h.expires_at < $1 LIMIT 100;

-- name: SquadMemberHoldsActive :many
SELECT h.id, h.expires_at FROM squad_members m JOIN holds h ON h.id = m.hold_id WHERE m.squad_id = $1 AND h.status = 'active';

-- name: GetSeatLabel :one
SELECT label FROM seats WHERE id = $1;
