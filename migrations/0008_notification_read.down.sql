DROP INDEX IF EXISTS notifications_unread_idx;
ALTER TABLE notifications DROP COLUMN IF EXISTS read_at;
