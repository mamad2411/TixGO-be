-- Platform: legal, privasi, support, notifikasi, audit, ops, CMS, integrasi (docs/20)
CREATE TABLE legal_documents (
  id char(26) PRIMARY KEY,
  kind text NOT NULL CHECK (kind IN ('terms','privacy','refund','about','faq','contact','how_to_buy','organizer_terms')),
  locale text NOT NULL CHECK (locale IN ('id','en')),
  version int NOT NULL,
  title text NOT NULL,
  body text NOT NULL,
  published_at timestamptz,
  created_by char(26) REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (kind, locale, version)
);

CREATE TABLE consents (
  id char(26) PRIMARY KEY,
  user_id char(26) NOT NULL REFERENCES users(id),
  document_id char(26) NOT NULL REFERENCES legal_documents(id),
  accepted_at timestamptz NOT NULL DEFAULT now(),
  ip_hash text,
  UNIQUE (user_id, document_id)
);

CREATE TABLE cookie_consents (
  subject_key text PRIMARY KEY,
  user_id char(26) REFERENCES users(id),
  analytics boolean NOT NULL DEFAULT false,
  marketing boolean NOT NULL DEFAULT false,
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE dsr_requests (
  id char(26) PRIMARY KEY,
  user_id char(26) REFERENCES users(id),
  kind text NOT NULL CHECK (kind IN ('export','delete','correct','withdraw_consent')),
  status text NOT NULL DEFAULT 'received'
    CHECK (status IN ('received','identity_verified','in_review','executed','rejected_with_reason')),
  reason text,
  detail jsonb,
  due_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  closed_at timestamptz
);
CREATE INDEX dsr_status_idx ON dsr_requests(status, due_at);

CREATE TABLE privacy_complaints (
  id char(26) PRIMARY KEY,
  name text,
  email text NOT NULL,
  subject text NOT NULL,
  body text NOT NULL,
  status text NOT NULL DEFAULT 'open' CHECK (status IN ('open','closed')),
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE export_jobs (
  id char(26) PRIMARY KEY,
  user_id char(26) NOT NULL REFERENCES users(id),
  kind text NOT NULL CHECK (kind IN ('privacy','attendees','sales','settlement','orders')),
  params jsonb NOT NULL DEFAULT '{}',
  status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','running','done','failed')),
  object_key text,
  error text,
  expires_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz
);

CREATE TABLE support_tickets (
  id char(26) PRIMARY KEY,
  number bigserial UNIQUE,
  user_id char(26) NOT NULL REFERENCES users(id),
  category text NOT NULL CHECK (category IN ('payment','ticket','refund','account','event','other')),
  subject text NOT NULL,
  order_id char(26) REFERENCES orders(id),
  event_id char(26) REFERENCES events(id),
  ticket_id char(26) REFERENCES tickets(id),
  status text NOT NULL DEFAULT 'open' CHECK (status IN ('open','pending_customer','resolved','closed')),
  priority text NOT NULL DEFAULT 'normal' CHECK (priority IN ('normal','high')),
  assignee_id char(26) REFERENCES users(id),
  first_response_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX support_tickets_queue_idx ON support_tickets(status, priority, created_at);
CREATE INDEX support_tickets_user_idx ON support_tickets(user_id);

CREATE TABLE support_messages (
  id char(26) PRIMARY KEY,
  ticket_id char(26) NOT NULL REFERENCES support_tickets(id) ON DELETE CASCADE,
  author_id char(26) NOT NULL REFERENCES users(id),
  internal boolean NOT NULL DEFAULT false,
  body text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE notification_prefs (
  user_id char(26) NOT NULL REFERENCES users(id),
  category text NOT NULL CHECK (category IN ('marketing','reminders','follows','broadcasts')),
  channel text NOT NULL CHECK (channel IN ('email','push')),
  enabled boolean NOT NULL DEFAULT true,
  PRIMARY KEY (user_id, category, channel)
);

CREATE TABLE notifications (
  id char(26) PRIMARY KEY,
  user_id char(26) REFERENCES users(id),
  channel text NOT NULL CHECK (channel IN ('email','sms','push')),
  template text NOT NULL,
  to_addr text,
  payload jsonb NOT NULL DEFAULT '{}',
  status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','sent','failed','suppressed')),
  attempts int NOT NULL DEFAULT 0,
  error text,
  created_at timestamptz NOT NULL DEFAULT now(),
  sent_at timestamptz
);
CREATE INDEX notifications_user_idx ON notifications(user_id, created_at DESC);

CREATE TABLE audit_logs (
  id char(26) PRIMARY KEY,
  actor_id char(26),
  actor_role text,
  action text NOT NULL,
  entity text NOT NULL,
  entity_id text,
  before jsonb,
  after jsonb,
  ip inet,
  request_id text,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_logs_entity_idx ON audit_logs(entity, entity_id);
CREATE INDEX audit_logs_actor_idx ON audit_logs(actor_id, created_at DESC);

CREATE TABLE blacklist (
  id char(26) PRIMARY KEY,
  kind text NOT NULL CHECK (kind IN ('user','phone','device','ip')),
  value_hash text NOT NULL,
  reason text,
  expires_at timestamptz,
  created_by char(26) REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (kind, value_hash)
);

CREATE TABLE risk_signals (
  id char(26) PRIMARY KEY,
  user_id char(26) REFERENCES users(id),
  event_id char(26) REFERENCES events(id),
  kind text NOT NULL,
  score int NOT NULL,
  meta jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX risk_signals_user_idx ON risk_signals(user_id, created_at DESC);

CREATE TABLE idempotency_keys (
  key text NOT NULL,
  user_id char(26) NOT NULL,
  route text NOT NULL,
  request_hash text NOT NULL,
  response jsonb,
  status int,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, route, key)
);
CREATE INDEX idempotency_created_idx ON idempotency_keys(created_at);

CREATE TABLE outbox (
  id bigserial PRIMARY KEY,
  channel text NOT NULL,
  topic text NOT NULL,
  payload jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  published_at timestamptz
);
CREATE INDEX outbox_unpublished_idx ON outbox(id) WHERE published_at IS NULL;

CREATE TABLE feature_flags (
  key text PRIMARY KEY,
  enabled boolean NOT NULL DEFAULT false,
  rollout_pct int NOT NULL DEFAULT 0 CHECK (rollout_pct BETWEEN 0 AND 100),
  updated_by char(26),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE announcements (
  id char(26) PRIMARY KEY,
  level text NOT NULL CHECK (level IN ('info','warning','incident','maintenance')),
  body_id text NOT NULL,
  body_en text,
  starts_at timestamptz NOT NULL,
  ends_at timestamptz,
  created_by char(26)
);

CREATE TABLE cms_pages (
  id char(26) PRIMARY KEY,
  kind text NOT NULL CHECK (kind IN ('page','article','help','collection')),
  slug text NOT NULL,
  locale text NOT NULL CHECK (locale IN ('id','en')),
  title text NOT NULL,
  body text NOT NULL,
  category text,
  seo jsonb NOT NULL DEFAULT '{}',
  status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','in_review','published','archived')),
  version int NOT NULL DEFAULT 1,
  publish_at timestamptz,
  updated_by char(26),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (kind, slug, locale, version)
);
CREATE INDEX cms_pages_pub_idx ON cms_pages(kind, slug, locale, status);

CREATE TABLE banners (
  id char(26) PRIMARY KEY,
  title text NOT NULL,
  image_key text NOT NULL,
  link text,
  starts_at timestamptz NOT NULL,
  ends_at timestamptz,
  sort int NOT NULL DEFAULT 0
);

CREATE TABLE collections (
  id char(26) PRIMARY KEY,
  slug text NOT NULL UNIQUE,
  title text NOT NULL,
  event_ids text[] NOT NULL DEFAULT '{}',
  starts_at timestamptz,
  ends_at timestamptz
);

CREATE TABLE push_subscriptions (
  id char(26) PRIMARY KEY,
  user_id char(26) NOT NULL REFERENCES users(id),
  endpoint text NOT NULL UNIQUE,
  keys_enc bytea NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE api_keys (
  id char(26) PRIMARY KEY,
  organizer_id char(26) NOT NULL REFERENCES organizers(id),
  name text NOT NULL,
  key_hash text NOT NULL UNIQUE,
  key_prefix text NOT NULL,
  scopes text[] NOT NULL,
  last_used_at timestamptz,
  revoked_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE webhook_endpoints (
  id char(26) PRIMARY KEY,
  organizer_id char(26) NOT NULL REFERENCES organizers(id),
  url text NOT NULL,
  secret_enc bytea NOT NULL,
  events text[] NOT NULL,
  active boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE webhook_deliveries (
  id char(26) PRIMARY KEY,
  endpoint_id char(26) NOT NULL REFERENCES webhook_endpoints(id) ON DELETE CASCADE,
  event text NOT NULL,
  payload jsonb NOT NULL,
  status int,
  attempts int NOT NULL DEFAULT 0,
  next_attempt_at timestamptz,
  delivered_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX webhook_deliveries_due_idx ON webhook_deliveries(next_attempt_at) WHERE delivered_at IS NULL;

CREATE TABLE widget_domains (
  organizer_id char(26) NOT NULL REFERENCES organizers(id),
  domain text NOT NULL,
  PRIMARY KEY (organizer_id, domain)
);

CREATE TABLE funnel_daily (
  day date NOT NULL,
  event_id char(26) NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  stage text NOT NULL CHECK (stage IN ('view','queue_join','admitted','hold','paid','ticket')),
  count bigint NOT NULL DEFAULT 0,
  PRIMARY KEY (day, event_id, stage)
);
