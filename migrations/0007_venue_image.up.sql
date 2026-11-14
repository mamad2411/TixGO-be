-- Foto venue: kolom kunci aset media (sama seperti artists.image_key) + jenis aset 'venue'.
ALTER TABLE venues ADD COLUMN image_key text;
ALTER TABLE media_assets DROP CONSTRAINT media_assets_kind_check;
ALTER TABLE media_assets ADD CONSTRAINT media_assets_kind_check
  CHECK (kind IN ('poster','artist','venue','banner','avatar','photo','other'));
