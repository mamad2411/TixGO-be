DELETE FROM media_assets WHERE kind = 'venue';
ALTER TABLE media_assets DROP CONSTRAINT media_assets_kind_check;
ALTER TABLE media_assets ADD CONSTRAINT media_assets_kind_check
  CHECK (kind IN ('poster','artist','banner','avatar','photo','other'));
ALTER TABLE venues DROP COLUMN IF EXISTS image_key;
