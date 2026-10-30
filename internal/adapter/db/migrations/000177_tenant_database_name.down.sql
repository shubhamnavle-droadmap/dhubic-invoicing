DROP INDEX IF EXISTS uq_tenants_database_name;
ALTER TABLE tenants DROP COLUMN IF EXISTS database_name;
