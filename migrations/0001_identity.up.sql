-- Identitas, sesi, organizer (docs/09 §2, 20 M27/M33)
CREATE TABLE users (
  id char(26) PRIMARY KEY,
  email citext UNIQUE NOT NULL,
  password_hash text NOT NULL,
  name text NOT NULL,
  phone text UNIQUE,
  phone_verified_at timestamptz,
  email_verified_at timestamptz,
  role text NOT NULL DEFAULT 'customer'
    CHECK (role IN ('customer','organizer','organizer_member','gate_staff','support','finance','editor','admin')),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','blocked','deleted')),
  locale text NOT NULL DEFAULT 'id' CHECK (locale IN ('id','en')),
  avatar_key text,
  mfa_enabled boolean NOT NULL DEFAULT false,
  failed_logins int NOT NULL DEFAULT 0,
  locked_until timestamptz,
  last_login_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  deleted_at timestamptz,
  anonymized_at timestamptz
);

CREATE TABLE refresh_tokens (
  id char(26) PRIMARY KEY,
  user_id char(26) NOT NULL REFERENCES users(id),
  family_id char(26) NOT NULL,
  token_hash text NOT NULL UNIQUE,
  device_label text,
  device_id text,
  ip inet,
  user_agent text,
  mfa boolean NOT NULL DEFAULT false,
  expires_at timestamptz NOT NULL,
  used_at timestamptz,
  revoked_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz
);
CREATE INDEX refresh_tokens_user_idx ON refresh_tokens(user_id);
CREATE INDEX refresh_tokens_family_idx ON refresh_tokens(family_id);

CREATE TABLE password_history (
  id bigserial PRIMARY KEY,
  user_id char(26) NOT NULL REFERENCES users(id),
  password_hash text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX password_history_user_idx ON password_history(user_id, id DESC);

CREATE TABLE email_tokens (
  id char(26) PRIMARY KEY,
  user_id char(26) NOT NULL REFERENCES users(id),
  kind text NOT NULL CHECK (kind IN ('verify_email','reset_password','change_email')),
  token_hash text NOT NULL UNIQUE,
  new_value text,
  expires_at timestamptz NOT NULL,
  used_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX email_tokens_user_idx ON email_tokens(user_id, kind);

CREATE TABLE mfa_totp (
  user_id char(26) PRIMARY KEY REFERENCES users(id),
  secret_enc bytea NOT NULL,
  confirmed_at timestamptz
);
CREATE TABLE mfa_recovery_codes (
  id char(26) PRIMARY KEY,
  user_id char(26) NOT NULL REFERENCES users(id),
  code_hash text NOT NULL,
  used_at timestamptz
);
CREATE INDEX mfa_recovery_user_idx ON mfa_recovery_codes(user_id);

CREATE TABLE social_identities (
  provider text NOT NULL,
  subject text NOT NULL,
  user_id char(26) NOT NULL REFERENCES users(id),
  PRIMARY KEY (provider, subject)
);

CREATE TABLE organizers (
  id char(26) PRIMARY KEY,
  name text NOT NULL,
  owner_user_id char(26) NOT NULL REFERENCES users(id),
  type text NOT NULL DEFAULT 'individual' CHECK (type IN ('individual','company')),
  legal_name text,
  contact_email text,
  contact_phone text,
  address text,
  tax_id_enc bytea,
  kyb_status text NOT NULL DEFAULT 'draft'
    CHECK (kyb_status IN ('draft','submitted','in_review','approved','rejected','needs_info')),
  kyb_reason text,
  kyb_reviewed_by char(26) REFERENCES users(id),
  kyb_reviewed_at timestamptz,
  contract_accepted_at timestamptz,
  contract_document_id char(26),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','suspended')),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX organizers_owner_idx ON organizers(owner_user_id);

CREATE TABLE organizer_members (
  organizer_id char(26) NOT NULL REFERENCES organizers(id),
  user_id char(26) NOT NULL REFERENCES users(id),
  role text NOT NULL CHECK (role IN ('owner','member','gate_staff')),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (organizer_id, user_id)
);
CREATE INDEX organizer_members_user_idx ON organizer_members(user_id);
