# Configuration and operations

## Environment variables

| Variable | Default | Purpose |
|---|---|---|
| `DEDICATED_DATABASES_ENABLED` | unset (off) | `true` turns on routing and the signup option |
| `DEDICATED_DATABASES_PROVISION_TOKEN` | unset | required value of `X-Provisioning-Token` to use `dedicated_database_id`; unset = always 403 |
| `DEDICATED_DB_MAX_OPEN` | 25 | active routed connections per replica |
| `DEDICATED_DB_MAX_CACHED` | 200 | warm parked connections per replica |
| `DEDICATED_DB_MAX_CACHED_PER_DB` | 2 | cap per database inside the warm cache |
| `DEDICATED_DATABASES_MAX` | 0 (unlimited) | hard cap on dedicated databases; further signups with `dedicated_database_id` get 429. Counts `recurso_*` databases in Postgres (template excluded); not atomic across replicas, so a burst can overshoot by a few |
| `DEDICATED_JOB_LOCK_FACTOR` | 3 | multiplies each background job's Redis lock lease (one lock covers a pass over all workspaces) |

With the flag off, behaviour is identical to upstream (routed handle = the normal handle).

## Signup request

```
POST /auth/register
X-Provisioning-Token: <secret>
{ ..., "dedicated_database_id": "acme" }   -> creates database recurso_acme
```
Omit the field (or leave empty) for the shared database. Errors: 400 invalid id or feature
off, 403 missing/bad token, 409 database already exists, 429 `DEDICATED_DATABASES_MAX` reached.

## Postgres connection budget

Per replica worst case: 25 unrouted + 25 routed + 200 cache = 250. Two replicas ~ 500.
Default `max_connections` is 100. Options: raise to ~600 on the server, and/or lower
`DEDICATED_DB_MAX_CACHED`; PgBouncer is possible but must be tested with the routing driver.
User confirmed Postgres can allow more connections and that 2 replicas will run.

## Operational notes

- Each replica holds its own router cache (60 s). After assigning a database the creating
  replica calls `Forget`; the other replica sees it within 60 s (fine because a new tenant's
  first requests normally hit one replica; verify during load test).
- Deleting a workspace never drops its database automatically. Use `cmd/drop_workspace_db` (canceled
  workspaces only, exact name confirmation); see 07-BACKUP-AND-DELETE-RUNBOOK.md.
- Backups: every `recurso_*` database must be included; see 07-BACKUP-AND-DELETE-RUNBOOK.md.
- Migrations: shared DB and the template migrate at startup; each dedicated database is
  migrated the first time a replica uses it (`OnFirstUse`). `recurso__template` is created
  automatically; do not delete it (provisioning falls back to the slower path if missing).
- Monitoring ideas: count of `recurso_*` databases, `pg_stat_activity` per database, router
  cache hit rate, route-lookup errors (these surface as failed requests by design).

## Self-service signup (customers on a website)
Do not call `/auth/register` with `dedicated_database_id` from the browser: the provisioning
token is a secret. Put a small server between the website and the API. It should (1) decide who
may have a dedicated database (paid/approved plan, verified email), (2) rate-limit workspace
creation per user and IP, (3) reject reserved names, (4) call `POST /auth/register` with the
`X-Provisioning-Token` header. `DEDICATED_DATABASES_MAX` is the last line of defence if that
server has a bug.
