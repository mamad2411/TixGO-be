-- Katalog, inventori, antrean, promo (docs/09 §2, 20 M2-M6, M35, M37, M43)
CREATE TABLE venues (
  id char(26) PRIMARY KEY,
  name text NOT NULL,
  city text NOT NULL,
  address text,
  lat double precision,
  lng double precision,
  tz text NOT NULL DEFAULT 'Asia/Jakarta',
  capacity int,
  seatmap_svg_key text,
  created_by char(26) REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX venues_city_idx ON venues(city);

CREATE TABLE artists (
  id char(26) PRIMARY KEY,
  slug text UNIQUE NOT NULL,
  name text NOT NULL,
  bio text,
  image_key text,
  socials jsonb NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX artists_name_trgm ON artists USING gin (name gin_trgm_ops);

CREATE TABLE media_assets (
  id char(26) PRIMARY KEY,
  owner_user_id char(26) REFERENCES users(id),
  kind text NOT NULL CHECK (kind IN ('poster','artist','banner','avatar','photo','other')),
  object_key text NOT NULL,
  content_type text NOT NULL,
  width int,
  height int,
  variants jsonb NOT NULL DEFAULT '[]',
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE events (
  id char(26) PRIMARY KEY,
  organizer_id char(26) NOT NULL REFERENCES organizers(id),
  venue_id char(26) NOT NULL REFERENCES venues(id),
  slug text UNIQUE NOT NULL,
  title text NOT NULL,
  description text,
  genre text,
  poster_key text,
  starts_at timestamptz NOT NULL,
  doors_at timestamptz,
  ends_at timestamptz,
  mode text NOT NULL CHECK (mode IN ('seated','general')),
  status text NOT NULL DEFAULT 'draft'
    CHECK (status IN ('draft','in_review','approved','published','paused','cancelled','finished')),
  schedule_state text NOT NULL DEFAULT 'scheduled'
    CHECK (schedule_state IN ('scheduled','postponed','rescheduled','cancelled')),
  previous_starts_at timestamptz,
  schedule_changed_at timestamptz,
  visibility text NOT NULL DEFAULT 'public' CHECK (visibility IN ('public','unlisted','password')),
  password_hash text,
  queue_required boolean NOT NULL DEFAULT false,
  admit_window_sec int NOT NULL DEFAULT 600 CHECK (admit_window_sec BETWEEN 30 AND 3600),
  admit_rate int NOT NULL DEFAULT 50 CHECK (admit_rate BETWEEN 1 AND 5000),
  max_concurrent int NOT NULL DEFAULT 500 CHECK (max_concurrent >= 1),
  queue_pre_open_min int NOT NULL DEFAULT 30,
  hold_ttl_sec int NOT NULL DEFAULT 600 CHECK (hold_ttl_sec BETWEEN 60 AND 3600),
  max_per_order int NOT NULL DEFAULT 6 CHECK (max_per_order >= 1),
  max_per_account int NOT NULL DEFAULT 4 CHECK (max_per_account >= 1),
  age_min int,
  tracking jsonb NOT NULL DEFAULT '{}',
  transfer_enabled boolean NOT NULL DEFAULT true,
  resale_cap_pct int NOT NULL DEFAULT 0 CHECK (resale_cap_pct BETWEEN 0 AND 200),
  refund_policy jsonb NOT NULL DEFAULT '{"type":"non_refundable","fee_refundable":false,"on_reschedule":{"window_days":14}}',
  service_fee_bp int NOT NULL DEFAULT 0 CHECK (service_fee_bp BETWEEN 0 AND 5000),
  service_fee_fixed bigint NOT NULL DEFAULT 0 CHECK (service_fee_fixed >= 0),
  fee_payer text NOT NULL DEFAULT 'buyer' CHECK (fee_payer IN ('buyer','organizer')),
  tax_bp int NOT NULL DEFAULT 0 CHECK (tax_bp BETWEEN 0 AND 5000),
  sales_paused boolean NOT NULL DEFAULT false,
  sales_paused_reason text,
  publish_at timestamptz,
  published_at timestamptz,
  review_note text,
  created_by char(26) REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX events_title_trgm ON events USING gin (title gin_trgm_ops);
CREATE INDEX events_status_starts ON events(status, starts_at);
CREATE INDEX events_organizer_idx ON events(organizer_id);
CREATE INDEX events_venue_idx ON events(venue_id);

CREATE TABLE event_artists (
  event_id char(26) NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  artist_id char(26) NOT NULL REFERENCES artists(id),
  billing_order int NOT NULL DEFAULT 0,
  PRIMARY KEY (event_id, artist_id)
);

CREATE TABLE event_sessions (
  id char(26) PRIMARY KEY,
  event_id char(26) NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  name text NOT NULL,
  starts_at timestamptz NOT NULL,
  ends_at timestamptz,
  capacity int
);

CREATE TABLE tiers (
  id char(26) PRIMARY KEY,
  event_id char(26) NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  name text NOT NULL,
  price bigint NOT NULL CHECK (price >= 0),
  quota int NOT NULL DEFAULT 0 CHECK (quota >= 0),
  sold int NOT NULL DEFAULT 0 CHECK (sold >= 0),
  held int NOT NULL DEFAULT 0 CHECK (held >= 0),
  comp_quota int NOT NULL DEFAULT 0 CHECK (comp_quota >= 0),
  comp_issued int NOT NULL DEFAULT 0 CHECK (comp_issued >= 0),
  color text,
  sort int NOT NULL DEFAULT 0,
  CONSTRAINT tiers_capacity_ck CHECK (sold + held <= quota OR quota = 0),
  CONSTRAINT tiers_comp_ck CHECK (comp_issued <= comp_quota)
);
CREATE INDEX tiers_event_idx ON tiers(event_id);

CREATE TABLE sections (
  id char(26) PRIMARY KEY,
  event_id char(26) NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  tier_id char(26) REFERENCES tiers(id),
  name text NOT NULL,
  svg_ref text,
  sort int NOT NULL DEFAULT 0
);
CREATE INDEX sections_event_idx ON sections(event_id);

CREATE TABLE seats (
  id char(26) PRIMARY KEY,
  event_id char(26) NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  section_id char(26) NOT NULL REFERENCES sections(id) ON DELETE CASCADE,
  tier_id char(26) NOT NULL REFERENCES tiers(id),
  row_label text NOT NULL,
  seat_no text NOT NULL,
  label text NOT NULL,
  x double precision,
  y double precision,
  accessible boolean NOT NULL DEFAULT false,
  status text NOT NULL DEFAULT 'available' CHECK (status IN ('available','held','sold','blocked')),
  UNIQUE (event_id, section_id, row_label, seat_no)
);
CREATE INDEX seats_event_status ON seats(event_id, status);
CREATE INDEX seats_tier_idx ON seats(tier_id);

CREATE TABLE sale_phases (
  id char(26) PRIMARY KEY,
  event_id char(26) NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  name text NOT NULL,
  audience text NOT NULL CHECK (audience IN ('public','code','fan')),
  starts_at timestamptz NOT NULL,
  ends_at timestamptz NOT NULL,
  queue_open_at timestamptz,
  CHECK (ends_at > starts_at)
);
CREATE INDEX sale_phases_event_idx ON sale_phases(event_id, starts_at);

CREATE TABLE presale_codes (
  id char(26) PRIMARY KEY,
  phase_id char(26) NOT NULL REFERENCES sale_phases(id) ON DELETE CASCADE,
  code_hash text NOT NULL UNIQUE,
  max_uses int,
  used int NOT NULL DEFAULT 0 CHECK (used >= 0),
  CHECK (max_uses IS NULL OR used <= max_uses)
);

CREATE TABLE phase_access (
  phase_id char(26) NOT NULL REFERENCES sale_phases(id) ON DELETE CASCADE,
  user_id char(26) NOT NULL REFERENCES users(id),
  via text NOT NULL CHECK (via IN ('code','fan','passport')),
  granted_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (phase_id, user_id)
);

CREATE TABLE fan_registrations (
  event_id char(26) NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  user_id char(26) NOT NULL REFERENCES users(id),
  status text NOT NULL DEFAULT 'registered' CHECK (status IN ('registered','selected','rejected')),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (event_id, user_id)
);

CREATE TABLE queue_runs (
  event_id char(26) PRIMARY KEY REFERENCES events(id) ON DELETE CASCADE,
  seed_hash text NOT NULL,
  seed text NOT NULL,
  started_at timestamptz,
  participants int NOT NULL DEFAULT 0,
  participants_digest text
);

CREATE TABLE gates (
  id char(26) PRIMARY KEY,
  event_id char(26) NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  name text NOT NULL,
  UNIQUE (event_id, name)
);
CREATE TABLE gate_staff (
  event_id char(26) NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  user_id char(26) NOT NULL REFERENCES users(id),
  gate_id char(26) REFERENCES gates(id),
  PRIMARY KEY (event_id, user_id)
);

CREATE TABLE promo_codes (
  id char(26) PRIMARY KEY,
  event_id char(26) NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  code text NOT NULL,
  kind text NOT NULL CHECK (kind IN ('percent','fixed')),
  value bigint NOT NULL CHECK (value > 0),
  max_uses int,
  used int NOT NULL DEFAULT 0 CHECK (used >= 0),
  max_per_account int NOT NULL DEFAULT 1 CHECK (max_per_account >= 1),
  starts_at timestamptz,
  ends_at timestamptz,
  min_total bigint NOT NULL DEFAULT 0,
  tier_ids text[],
  active boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (event_id, code),
  CHECK (max_uses IS NULL OR used <= max_uses),
  CHECK (kind <> 'percent' OR value <= 100)
);

CREATE TABLE campaign_links (
  id char(26) PRIMARY KEY,
  event_id char(26) NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  slug text NOT NULL,
  utm_source text,
  utm_medium text,
  utm_campaign text,
  commission_bp int NOT NULL DEFAULT 0,
  UNIQUE (event_id, slug)
);

CREATE TABLE addons (
  id char(26) PRIMARY KEY,
  event_id char(26) NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  name text NOT NULL,
  price bigint NOT NULL CHECK (price >= 0),
  quota int NOT NULL CHECK (quota >= 0),
  sold int NOT NULL DEFAULT 0,
  active boolean NOT NULL DEFAULT true,
  CHECK (sold >= 0 AND sold <= quota)
);

CREATE TABLE favorites (
  user_id char(26) NOT NULL REFERENCES users(id),
  event_id char(26) NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, event_id)
);
CREATE TABLE reminders (
  user_id char(26) NOT NULL REFERENCES users(id),
  event_id char(26) NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  remind_at timestamptz NOT NULL,
  sent_at timestamptz,
  PRIMARY KEY (user_id, event_id)
);
CREATE INDEX reminders_due_idx ON reminders(remind_at) WHERE sent_at IS NULL;
CREATE TABLE follows (
  user_id char(26) NOT NULL REFERENCES users(id),
  kind text NOT NULL CHECK (kind IN ('artist','venue','city','genre')),
  target text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, kind, target)
);
CREATE INDEX follows_target_idx ON follows(kind, target);
CREATE TABLE seat_preferences (
  user_id char(26) NOT NULL REFERENCES users(id),
  event_id char(26) NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  prefs jsonb NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, event_id)
);
