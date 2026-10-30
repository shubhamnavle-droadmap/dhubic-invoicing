-- Customer reference and profile fields for API integrations.
--   external_id      the caller's own id for this customer (unique per workspace)
--   metadata         free-form scalar key/value pairs owned by the caller
-- All columns are nullable or defaulted, so existing rows and existing API
-- calls are unaffected.
ALTER TABLE customers
    ADD COLUMN IF NOT EXISTS external_id      TEXT,
    ADD COLUMN IF NOT EXISTS legal_name      TEXT   NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS locale          TEXT   NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS payment_terms   TEXT   NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS website         TEXT   NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS notes           TEXT   NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS tags            TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS billing_emails  TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS shipping_address JSONB,
    ADD COLUMN IF NOT EXISTS metadata        JSONB  NOT NULL DEFAULT '{}'::jsonb;

-- One customer per external id within a workspace; customers without one are unconstrained.
CREATE UNIQUE INDEX IF NOT EXISTS uq_customers_tenant_external_id
    ON customers (tenant_id, external_id) WHERE external_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_customers_tags ON customers USING GIN (tags);
