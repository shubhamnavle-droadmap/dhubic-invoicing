DROP INDEX IF EXISTS idx_customers_tags;
DROP INDEX IF EXISTS uq_customers_tenant_external_id;
ALTER TABLE customers
    DROP COLUMN IF EXISTS metadata,
    DROP COLUMN IF EXISTS shipping_address,
    DROP COLUMN IF EXISTS billing_emails,
    DROP COLUMN IF EXISTS tags,
    DROP COLUMN IF EXISTS notes,
    DROP COLUMN IF EXISTS website,
    DROP COLUMN IF EXISTS payment_terms,
    DROP COLUMN IF EXISTS locale,
    DROP COLUMN IF EXISTS legal_name,
    DROP COLUMN IF EXISTS external_id;
