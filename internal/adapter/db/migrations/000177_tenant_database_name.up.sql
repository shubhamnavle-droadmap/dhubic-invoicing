-- Dedicated per-workspace database. NULL (the default for every existing and
-- every new tenant) means the tenant lives in the shared database; a value is
-- the name of its own database (recurso_<id>), created at signup.
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS database_name TEXT;

-- Two workspaces can never point at the same database.
CREATE UNIQUE INDEX IF NOT EXISTS uq_tenants_database_name
    ON tenants (database_name) WHERE database_name IS NOT NULL;
