-- Dompet voucher pengguna: kode promo yang ditukar/disimpan akun (docs/22, FE akun).
CREATE TABLE user_vouchers (
  user_id char(26) NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  promo_id char(26) NOT NULL REFERENCES promo_codes(id) ON DELETE CASCADE,
  saved_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, promo_id)
);
CREATE INDEX user_vouchers_user_idx ON user_vouchers(user_id, saved_at DESC);
