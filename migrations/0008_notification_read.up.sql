-- Status baca untuk kotak masuk in-app (log pengiriman yang sama dipakai sebagai sumber).
ALTER TABLE notifications ADD COLUMN read_at timestamptz;
CREATE INDEX notifications_unread_idx ON notifications(user_id) WHERE read_at IS NULL;
