# Backup, restore and delete runbook

Every dedicated workspace is its own Postgres database named `recurso_<id>`. The shared
database (control plane: users, sessions, API keys, tenants, organizations, gateway
connections, ...) holds the map from workspace to database. **A backup must include both.**
Restoring only one side leaves workspaces pointing at missing or stale data.

## What to back up
- The shared database (name from `DATABASE_URL`, e.g. `recurso`).
- Every `recurso_*` database, **except** `recurso__template` (it is rebuilt automatically).

```bash
# list them
psql "$ADMIN_URL" -At -c "select datname from pg_database where datname like 'recurso\_%' and datname <> 'recurso__template' order by 1"
```

## Backup (logical, per database)
```bash
for db in recurso $(psql "$ADMIN_URL" -At -c "select datname from pg_database where datname like 'recurso\_%' and datname <> 'recurso__template'"); do
  pg_dump -Fc -d "postgres://USER:PASS@HOST:5432/$db" -f "backup/$(date +%F)/$db.dump"
done
```
Or use your cluster-level backup (base backup + WAL, managed-database snapshots), which covers
all databases at one consistent point in time. Prefer that for production: per-database dumps
are taken at slightly different moments, so a workspace's shared rows (tenants, users) and its
own database can disagree by a few minutes.

## Restore one workspace
1. Make sure the shared database already has the tenant row with `database_name = 'recurso_<id>'`
   (restore the shared database first if this is a disaster recovery).
2. Recreate and load the workspace database:
```bash
psql "$ADMIN_URL" -c 'DROP DATABASE IF EXISTS "recurso_<id>" WITH (FORCE)'   # only if replacing it
psql "$ADMIN_URL" -c 'CREATE DATABASE "recurso_<id>"'
pg_restore -d "postgres://USER:PASS@HOST:5432/recurso_<id>" --no-owner backup/<date>/recurso_<id>.dump
```
3. Restart or wait up to 60 s: each replica caches workspace routes for 60 s, and migrates a
   database the first time it uses it, so an older dump is brought to the current schema
   automatically.

## Deleting a workspace's database
Nothing in the API deletes a database. There is no automatic cleanup when a tenant is
canceled; that is on purpose (a dropped database cannot be recovered).

Recommended policy: cancel the workspace, keep the database for 30 days, take a final dump,
then drop it with the operator tool:
```bash
pg_dump -Fc -d "postgres://USER:PASS@HOST:5432/recurso_<id>" -f final/recurso_<id>.dump
DATABASE_URL=postgres://... go run ./cmd/drop_workspace_db -tenant <tenant-uuid> -confirm recurso_<id>
```
The tool refuses unless the tenant's `billing_status` is `canceled` and `-confirm` is the exact
database name. The tenant row keeps `database_name`, so any later request for that workspace
fails (it never falls back to the shared database).

## Webhook URLs (dedicated workspaces)
Use the per-connection URL, `/webhooks/<stripe|razorpay|gocardless>/<connection-id>`. The plain
`/webhooks/<provider>` URL cannot see dedicated databases; the log line "invoice not found"
carries a `hint` pointing here.

## Job locks
One Redis lock covers a job's whole pass over every workspace. Its lease is multiplied by
`DEDICATED_JOB_LOCK_FACTOR` (default 3) when the feature is on. Raise it if a job's pass over
all workspaces can take longer than a third of its normal lease.
