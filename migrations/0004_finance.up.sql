-- KYB dokumen, rekening, ledger berganda, payout, rekonsiliasi (docs/20 M33, M34, M47)
CREATE TABLE organizer_documents (
  id char(26) PRIMARY KEY,
  organizer_id char(26) NOT NULL REFERENCES organizers(id),
  kind text NOT NULL,
  object_key text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE bank_accounts (
  id char(26) PRIMARY KEY,
  organizer_id char(26) NOT NULL REFERENCES organizers(id),
  bank_code text NOT NULL,
  account_no_enc bytea NOT NULL,
  account_no_last4 text NOT NULL,
  account_name text NOT NULL,
  verified_at timestamptz,
  active_from timestamptz,
  active boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX bank_accounts_org_idx ON bank_accounts(organizer_id);

CREATE TABLE ledger_accounts (
  id char(26) PRIMARY KEY,
  kind text NOT NULL CHECK (kind IN (
    'platform_revenue','payment_clearing','bank_cash','organizer_pending','organizer_available',
    'organizer_payout_in_transit','organizer_paid','adjustments','resale_payable')),
  organizer_id char(26) REFERENCES organizers(id),
  UNIQUE NULLS NOT DISTINCT (kind, organizer_id)
);

CREATE TABLE ledger_transactions (
  id char(26) PRIMARY KEY,
  ref_type text NOT NULL,
  ref_id text NOT NULL,
  memo text,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (ref_type, ref_id)
);

CREATE TABLE ledger_entries (
  id bigserial PRIMARY KEY,
  tx_id char(26) NOT NULL REFERENCES ledger_transactions(id),
  account_id char(26) NOT NULL REFERENCES ledger_accounts(id),
  debit bigint NOT NULL DEFAULT 0 CHECK (debit >= 0),
  credit bigint NOT NULL DEFAULT 0 CHECK (credit >= 0),
  CHECK ((debit = 0) <> (credit = 0))
);
CREATE INDEX ledger_entries_account_idx ON ledger_entries(account_id);
CREATE INDEX ledger_entries_tx_idx ON ledger_entries(tx_id);

-- Jaring pengaman DB: total debit = total kredit per transaksi (diperiksa saat COMMIT).
CREATE FUNCTION ledger_check_balanced() RETURNS trigger AS $$
DECLARE d bigint; c bigint;
BEGIN
  SELECT COALESCE(SUM(debit),0), COALESCE(SUM(credit),0) INTO d, c FROM ledger_entries WHERE tx_id = NEW.tx_id;
  IF d <> c THEN
    RAISE EXCEPTION 'ledger tidak seimbang untuk tx %: debit=% kredit=%', NEW.tx_id, d, c;
  END IF;
  RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE CONSTRAINT TRIGGER ledger_balanced_trg
  AFTER INSERT ON ledger_entries
  DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION ledger_check_balanced();

CREATE TABLE payouts (
  id char(26) PRIMARY KEY,
  organizer_id char(26) NOT NULL REFERENCES organizers(id),
  bank_account_id char(26) NOT NULL REFERENCES bank_accounts(id),
  amount bigint NOT NULL CHECK (amount > 0),
  status text NOT NULL DEFAULT 'requested'
    CHECK (status IN ('requested','approved','rejected','processing','paid','failed')),
  requested_by char(26) NOT NULL REFERENCES users(id),
  approved_by char(26) REFERENCES users(id),
  proof_key text,
  note text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  paid_at timestamptz,
  CHECK (approved_by IS NULL OR approved_by <> requested_by)
);
CREATE INDEX payouts_org_idx ON payouts(organizer_id, status);

CREATE TABLE recon_exceptions (
  id char(26) PRIMARY KEY,
  kind text NOT NULL,
  payment_id char(26) REFERENCES payments(id),
  order_id char(26) REFERENCES orders(id),
  detail jsonb,
  status text NOT NULL DEFAULT 'open' CHECK (status IN ('open','investigating','resolved')),
  resolution text,
  created_at timestamptz NOT NULL DEFAULT now(),
  resolved_at timestamptz
);
CREATE INDEX recon_exceptions_status_idx ON recon_exceptions(status);
