-- Hold, order, pembayaran, tiket, gate, refund (docs/09 §2, 11, 22)
CREATE TABLE holds (
  id char(26) PRIMARY KEY,
  event_id char(26) NOT NULL REFERENCES events(id),
  user_id char(26) NOT NULL REFERENCES users(id),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','converted','released','expired')),
  expires_at timestamptz NOT NULL,
  extended_at timestamptz,
  squad_id char(26),
  order_id char(26),
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX holds_active_idx ON holds(expires_at) WHERE status = 'active';
CREATE INDEX holds_user_event_idx ON holds(user_id, event_id);

CREATE TABLE hold_items (
  id bigserial PRIMARY KEY,
  hold_id char(26) NOT NULL REFERENCES holds(id) ON DELETE CASCADE,
  seat_id char(26) REFERENCES seats(id),
  tier_id char(26) NOT NULL REFERENCES tiers(id),
  qty int NOT NULL DEFAULT 1 CHECK (qty > 0)
);
CREATE UNIQUE INDEX hold_items_seat_uniq ON hold_items(hold_id, seat_id) WHERE seat_id IS NOT NULL;
CREATE INDEX hold_items_hold_idx ON hold_items(hold_id);

CREATE TABLE orders (
  id char(26) PRIMARY KEY,
  user_id char(26) NOT NULL REFERENCES users(id),
  event_id char(26) NOT NULL REFERENCES events(id),
  hold_id char(26) REFERENCES holds(id),
  status text NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending','paid','expired','cancelled','refunding','refunded','partially_refunded')),
  subtotal bigint NOT NULL CHECK (subtotal >= 0),
  addons_total bigint NOT NULL DEFAULT 0 CHECK (addons_total >= 0),
  discount bigint NOT NULL DEFAULT 0 CHECK (discount >= 0),
  fee bigint NOT NULL DEFAULT 0 CHECK (fee >= 0),
  platform_fee bigint NOT NULL DEFAULT 0 CHECK (platform_fee >= 0),
  payment_fee bigint NOT NULL DEFAULT 0 CHECK (payment_fee >= 0),
  tax bigint NOT NULL DEFAULT 0 CHECK (tax >= 0),
  total bigint NOT NULL CHECK (total >= 0),
  currency char(3) NOT NULL DEFAULT 'IDR',
  promo_id char(26) REFERENCES promo_codes(id),
  campaign_slug text,
  age_confirmed boolean NOT NULL DEFAULT false,
  refund_policy_snapshot jsonb,
  payment_method text,
  expires_at timestamptz NOT NULL,
  paid_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX orders_user_idx ON orders(user_id, created_at DESC);
CREATE INDEX orders_event_status_idx ON orders(event_id, status);
CREATE INDEX orders_pending_exp_idx ON orders(expires_at) WHERE status = 'pending';

CREATE TABLE order_items (
  id char(26) PRIMARY KEY,
  order_id char(26) NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
  seat_id char(26) REFERENCES seats(id),
  tier_id char(26) NOT NULL REFERENCES tiers(id),
  price bigint NOT NULL CHECK (price >= 0),
  attendee_name text NOT NULL,
  attendee_email text NOT NULL
);
CREATE INDEX order_items_order_idx ON order_items(order_id);

CREATE TABLE order_addons (
  order_id char(26) NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
  addon_id char(26) NOT NULL REFERENCES addons(id),
  qty int NOT NULL CHECK (qty > 0),
  price bigint NOT NULL,
  PRIMARY KEY (order_id, addon_id)
);

CREATE TABLE promo_redemptions (
  id char(26) PRIMARY KEY,
  promo_id char(26) NOT NULL REFERENCES promo_codes(id),
  order_id char(26) NOT NULL UNIQUE REFERENCES orders(id) ON DELETE CASCADE,
  user_id char(26) NOT NULL REFERENCES users(id),
  discount bigint NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX promo_redemptions_user_idx ON promo_redemptions(promo_id, user_id);

CREATE TABLE payments (
  id char(26) PRIMARY KEY,
  order_id char(26) NOT NULL REFERENCES orders(id),
  provider text NOT NULL DEFAULT 'midtrans',
  provider_ref text,
  method text,
  amount bigint NOT NULL,
  status text NOT NULL DEFAULT 'created'
    CHECK (status IN ('created','pending','settled','failed','expired','refunded')),
  snap_token text,
  redirect_url text,
  created_at timestamptz NOT NULL DEFAULT now(),
  settled_at timestamptz,
  UNIQUE (provider, provider_ref)
);
CREATE INDEX payments_order_idx ON payments(order_id);

CREATE TABLE payment_events (
  id char(26) PRIMARY KEY,
  payment_id char(26) REFERENCES payments(id),
  provider_event_id text NOT NULL UNIQUE,
  payload jsonb NOT NULL,
  received_at timestamptz NOT NULL DEFAULT now(),
  processed_at timestamptz
);

CREATE TABLE refunds (
  id char(26) PRIMARY KEY,
  order_id char(26) NOT NULL REFERENCES orders(id),
  amount bigint NOT NULL CHECK (amount > 0),
  reason text,
  status text NOT NULL DEFAULT 'requested'
    CHECK (status IN ('requested','approved','rejected','awaiting_account','refunding','refunded','refund_failed')),
  method text NOT NULL DEFAULT 'gateway' CHECK (method IN ('gateway','manual')),
  initiated_by text NOT NULL DEFAULT 'buyer' CHECK (initiated_by IN ('buyer','organizer_cancel','admin')),
  provider_ref text,
  refund_key text,
  payment_id char(26) REFERENCES payments(id),
  scope text NOT NULL DEFAULT 'order' CHECK (scope IN ('order','payment')),
  attempts int NOT NULL DEFAULT 0,
  refund_bank_code text,
  refund_account_name text,
  refund_account_enc bytea,
  due_at timestamptz,
  proof_key text,
  approved_by char(26) REFERENCES users(id),
  processed_by char(26) REFERENCES users(id),
  note text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK (approved_by IS NULL OR processed_by IS NULL OR approved_by <> processed_by OR method = 'gateway')
);
CREATE UNIQUE INDEX one_open_refund_per_order ON refunds(order_id)
  WHERE status IN ('requested','approved','awaiting_account','refunding');
CREATE INDEX refunds_status_idx ON refunds(status, method);

CREATE TABLE tickets (
  id char(26) PRIMARY KEY,
  order_item_id char(26) UNIQUE REFERENCES order_items(id),
  event_id char(26) NOT NULL REFERENCES events(id),
  tier_id char(26) NOT NULL REFERENCES tiers(id),
  owner_user_id char(26) NOT NULL REFERENCES users(id),
  seat_id char(26) REFERENCES seats(id),
  seat_label text,
  entry_number text,
  attendee_name text,
  face_price bigint,
  secret_enc bytea NOT NULL,
  status text NOT NULL DEFAULT 'valid' CHECK (status IN ('valid','used','void','refunded','transferred')),
  origin text NOT NULL DEFAULT 'sale' CHECK (origin IN ('sale','comp','import','transfer')),
  issued_at timestamptz NOT NULL DEFAULT now(),
  used_at timestamptz
);
CREATE INDEX tickets_event_status_idx ON tickets(event_id, status);
CREATE INDEX tickets_owner_idx ON tickets(owner_user_id);
CREATE SEQUENCE ticket_entry_seq;

CREATE TABLE gate_scans (
  id char(26) PRIMARY KEY,
  scan_id text UNIQUE,
  ticket_id char(26) REFERENCES tickets(id),
  event_id char(26) NOT NULL REFERENCES events(id),
  gate_id text,
  staff_user_id char(26) REFERENCES users(id),
  result text NOT NULL,
  detail text,
  scanned_at timestamptz NOT NULL,
  received_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX one_valid_entry ON gate_scans(ticket_id) WHERE result = 'OK';
CREATE INDEX gate_scans_event_idx ON gate_scans(event_id, scanned_at);

CREATE TABLE ticket_transfers (
  id char(26) PRIMARY KEY,
  ticket_id char(26) NOT NULL REFERENCES tickets(id),
  from_user_id char(26) NOT NULL REFERENCES users(id),
  to_user_id char(26) NOT NULL REFERENCES users(id),
  via text NOT NULL CHECK (via IN ('claim','resale','comp')),
  ref_id char(26),
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ticket_transfers_ticket_idx ON ticket_transfers(ticket_id, created_at);

CREATE TABLE claim_links (
  id char(26) PRIMARY KEY,
  ticket_id char(26) NOT NULL REFERENCES tickets(id),
  token_hash text NOT NULL UNIQUE,
  target_email text,
  created_by char(26) NOT NULL REFERENCES users(id),
  expires_at timestamptz NOT NULL,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','claimed','expired','revoked')),
  claimed_by char(26) REFERENCES users(id),
  claimed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX one_active_claim_per_ticket ON claim_links(ticket_id) WHERE status = 'active';

CREATE TABLE comp_invitations (
  id char(26) PRIMARY KEY,
  event_id char(26) NOT NULL REFERENCES events(id),
  tier_id char(26) NOT NULL REFERENCES tiers(id),
  name text NOT NULL,
  email text NOT NULL,
  reason text,
  issued_by char(26) NOT NULL REFERENCES users(id),
  ticket_id char(26) REFERENCES tickets(id),
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE broadcasts (
  id char(26) PRIMARY KEY,
  event_id char(26) NOT NULL REFERENCES events(id),
  tier_id char(26) REFERENCES tiers(id),
  subject text NOT NULL,
  body text NOT NULL,
  status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','sending','sent','failed')),
  recipients int NOT NULL DEFAULT 0,
  sent_count int NOT NULL DEFAULT 0,
  created_by char(26) NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE resale_listings (
  id char(26) PRIMARY KEY,
  ticket_id char(26) NOT NULL REFERENCES tickets(id),
  event_id char(26) NOT NULL REFERENCES events(id),
  seller_id char(26) NOT NULL REFERENCES users(id),
  face_price bigint NOT NULL CHECK (face_price >= 0),
  price bigint NOT NULL CHECK (price > 0),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','reserved','sold','cancelled')),
  buyer_id char(26) REFERENCES users(id),
  reserved_until timestamptz,
  order_id char(26),
  created_at timestamptz NOT NULL DEFAULT now(),
  sold_at timestamptz
);
CREATE UNIQUE INDEX one_open_listing_per_ticket ON resale_listings(ticket_id) WHERE status IN ('active','reserved');
CREATE INDEX resale_event_idx ON resale_listings(event_id, status);
ALTER TABLE orders ADD COLUMN resale_listing_id char(26) REFERENCES resale_listings(id);
CREATE UNIQUE INDEX one_order_per_listing ON orders(resale_listing_id) WHERE resale_listing_id IS NOT NULL AND status IN ('pending','paid');

CREATE TABLE resale_payouts (
  id char(26) PRIMARY KEY,
  listing_id char(26) NOT NULL UNIQUE REFERENCES resale_listings(id),
  seller_id char(26) NOT NULL REFERENCES users(id),
  amount bigint NOT NULL CHECK (amount > 0),
  status text NOT NULL DEFAULT 'awaiting_account' CHECK (status IN ('awaiting_account','ready','paid')),
  bank_code text,
  account_name text,
  account_enc bytea,
  proof_key text,
  processed_by char(26) REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  paid_at timestamptz
);
CREATE UNIQUE INDEX one_active_listing_per_ticket ON resale_listings(ticket_id) WHERE status = 'active';

CREATE TABLE waitlists (
  id char(26) PRIMARY KEY,
  event_id char(26) NOT NULL REFERENCES events(id),
  tier_id char(26) NOT NULL REFERENCES tiers(id),
  user_id char(26) NOT NULL REFERENCES users(id),
  qty int NOT NULL DEFAULT 1 CHECK (qty BETWEEN 1 AND 10),
  status text NOT NULL DEFAULT 'waiting' CHECK (status IN ('waiting','offered','accepted','expired','cancelled')),
  offered_until timestamptz,
  hold_id char(26),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (event_id, tier_id, user_id)
);
CREATE INDEX waitlists_queue_idx ON waitlists(event_id, tier_id, created_at) WHERE status = 'waiting';

CREATE TABLE squads (
  id char(26) PRIMARY KEY,
  event_id char(26) NOT NULL REFERENCES events(id),
  leader_id char(26) NOT NULL REFERENCES users(id),
  code text NOT NULL UNIQUE,
  hold_id char(26) REFERENCES holds(id),
  size int NOT NULL CHECK (size BETWEEN 2 AND 10),
  policy text NOT NULL DEFAULT 'release_unpaid' CHECK (policy IN ('release_unpaid','cancel_all')),
  status text NOT NULL DEFAULT 'forming' CHECK (status IN ('forming','holding','completed','cancelled','expired')),
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE squad_members (
  squad_id char(26) NOT NULL REFERENCES squads(id) ON DELETE CASCADE,
  user_id char(26) NOT NULL REFERENCES users(id),
  seat_id char(26) REFERENCES seats(id),
  hold_id char(26) REFERENCES holds(id),
  order_id char(26) REFERENCES orders(id),
  status text NOT NULL DEFAULT 'joined' CHECK (status IN ('joined','paid','released')),
  joined_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (squad_id, user_id)
);

CREATE TABLE invoice_counters (
  kind text NOT NULL CHECK (kind IN ('invoice','credit_note')),
  year int NOT NULL,
  last bigint NOT NULL DEFAULT 0,
  PRIMARY KEY (kind, year)
);
CREATE TABLE invoices (
  id char(26) PRIMARY KEY,
  order_id char(26) NOT NULL UNIQUE REFERENCES orders(id),
  number text NOT NULL UNIQUE,
  year int NOT NULL,
  seq bigint NOT NULL,
  subtotal bigint NOT NULL,
  discount bigint NOT NULL DEFAULT 0,
  fee bigint NOT NULL,
  payment_fee bigint NOT NULL DEFAULT 0,
  tax bigint NOT NULL DEFAULT 0,
  total bigint NOT NULL,
  issued_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (year, seq)
);
CREATE TABLE credit_notes (
  id char(26) PRIMARY KEY,
  invoice_id char(26) NOT NULL REFERENCES invoices(id),
  refund_id char(26) REFERENCES refunds(id),
  number text NOT NULL UNIQUE,
  amount bigint NOT NULL CHECK (amount > 0),
  issued_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE passport_points (
  id bigserial PRIMARY KEY,
  user_id char(26) NOT NULL REFERENCES users(id),
  event_id char(26) REFERENCES events(id),
  ticket_id char(26) REFERENCES tickets(id),
  points int NOT NULL,
  reason text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (user_id, ticket_id, reason)
);
CREATE TABLE passport_badges (
  user_id char(26) NOT NULL REFERENCES users(id),
  badge text NOT NULL,
  awarded_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, badge)
);

CREATE TABLE reviews (
  id char(26) PRIMARY KEY,
  ticket_id char(26) NOT NULL UNIQUE REFERENCES tickets(id),
  event_id char(26) NOT NULL REFERENCES events(id),
  user_id char(26) NOT NULL REFERENCES users(id),
  rating int NOT NULL CHECK (rating BETWEEN 1 AND 5),
  body text,
  organizer_reply text,
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','published','rejected')),
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX reviews_event_idx ON reviews(event_id, status);
CREATE TABLE review_reports (
  id char(26) PRIMARY KEY,
  review_id char(26) NOT NULL REFERENCES reviews(id),
  reporter_id char(26) NOT NULL REFERENCES users(id),
  reason text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (review_id, reporter_id)
);
CREATE TABLE event_photos (
  id char(26) PRIMARY KEY,
  event_id char(26) NOT NULL REFERENCES events(id),
  user_id char(26) NOT NULL REFERENCES users(id),
  object_key text NOT NULL,
  rights_confirmed boolean NOT NULL DEFAULT false,
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','published','rejected')),
  created_at timestamptz NOT NULL DEFAULT now()
);
