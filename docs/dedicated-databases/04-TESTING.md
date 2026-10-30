# Testing

## Setup (Git Bash)

```bash
export PATH="$PATH:/c/Program Files/Go/bin"
cd /d/InvoiceShelf/recurso
# Postgres-backed tests are gated on this variable. Verify host/port/dbname against the
# running container (docker ps) before use; user/password are user/password.
export TEST_DATABASE_URL="postgres://user:password@localhost:5432/recurso?sslmode=disable"
```

The DB user must be able to `CREATE DATABASE`.

## Quick (no database needed)

```bash
go build ./...
go test ./internal/service/ ./internal/adapter/handler/ ./internal/adapter/middleware/ ./cmd/api/
go test ./internal/adapter/db/ -run 'TestTenantDatabaseName|TestTenantDatabaseDSN'
```

## Postgres-backed

```bash
go test ./internal/adapter/db/ -run TestProvisionTenantDatabase_Postgres -v
go test ./internal/adapter/db/ -run TestRoutedDB_Postgres -v -timeout 600s
go test ./... -p 1 -timeout 1800s        # whole suite, run in background (long)
```

## What each test proves

| Test | Proves |
|---|---|
| `TestProvisionTenantDatabase_Postgres` | new DB has no real tenants, empty `invoices`; duplicate -> `ErrTenantDatabaseExists`; injection-shaped id rejected; drop works |
| `TestRoutedDB_Postgres` | 3 databases routed correctly; write isolation; transactions; 60 goroutines x 40 mixed-tenant statements never cross; fail-closed (missing DB, tampered name `postgres`, injection name, closed router) |
| `auth_dedicated_db_test.go` | assigns DB; empty id uses shared; rejected when feature off; provision failure creates nothing; assign failure drops the DB |
| `TestRegisterHandler_DedicatedDatabaseRequiresToken` | no token / wrong token / feature off -> 403; right token passes |
| `TestRegisterHandler_NoDatabaseFieldNeedsNoToken` | ordinary signup unaffected |
| OpenAPI drift tests (`cmd/api`) | `openapi.yaml` matches routes |

## Known / expected failures

- `TestSlowQueryLog_Postgres` fails on a clean upstream checkout too. Ignore.
- `TestRoutedDB_Postgres` is connection-heavy; one run took 131 s when the server was
  saturated. If you see "too many clients" check for leaked connections first
  (`select count(*) from pg_stat_activity`), then `max_connections`.
- On Windows/Docker, "connection forcibly closed" and 30 s stalls under heavy churn are
  environmental, not code bugs.

## Manual end-to-end check

1. Start Postgres, then run the API from the repo folder with:
   `DEDICATED_DATABASES_ENABLED=true DEDICATED_DATABASES_PROVISION_TOKEN=<secret>`
2. Sign up with `dedicated_database_id` and header `X-Provisioning-Token: <secret>`.
3. Create a customer + invoice in that workspace; in `psql` confirm rows are in
   `recurso_<id>` and NOT in the shared DB. Repeat for a normal signup (no field) and
   confirm rows are in the shared DB.
4. Try signup with the field but no token -> expect 403.

## Tests still to write (tie to 03-PENDING.md)

- Guard test: every repo in main.go is routed or on the stays-shared list.
- Per converted group: run its existing Postgres tests with a dedicated tenant context.
- Scheduler fan-out tests: dedicated tenants are visited; one failure doesn't abort others.
- Template-clone provisioning + lazy-migration race test (two replicas opening the same new DB).
- Linux load test: ~150 workspaces, 2 replicas, mixed traffic; record p50/p99 and connection counts.
