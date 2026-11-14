-- Faktor kedua selain TOTP: OTP ke email dan OTP ke HP terverifikasi (SMS). `mfa_enabled` tetap berarti
-- "minimal satu faktor aktif" (dijaga service, dipakai login di semua perangkat).
ALTER TABLE users
  ADD COLUMN mfa_email boolean NOT NULL DEFAULT false,
  ADD COLUMN mfa_sms boolean NOT NULL DEFAULT false;
