# Architecture

## Data split

- **Shared database (control plane)** stays where it is. Holds: tenants, users, sessions,
  API keys, MFA, password reset, email verification, OAuth/SSO, organizations, platform.
  These are read BEFORE the tenant is known (login, API-key auth), so they cannot live in
  a per-tenant database.
- **Dedicated database `recurso_<id>` (business data)**: customers, invoices, ledger,
  entities, transactions today; the rest of the business tables once converted
  (see 03-PENDING.md).
- Every database (shared and dedicated) gets the same 173+ migrations, so the schema is
  identical. About 50 tables have FKs to `tenants(id)`, so each dedicated database holds a
  copy of its own tenant row (`SeedTenantInDatabase`). Migration 000002 also seeds a
  placeholder default tenant `00000000-0000-0000-0000-000000000001` in every database.
- A DB trigger on `tenants` insert creates the primary entity + invoice sequence, so seeding
  the tenant row into the dedicated database also creates those.

## Naming

- Input id is lowercased and trimmed, must match `^[a-z0-9][a-z0-9_]{0,39}$`.
- Database name = `recurso_` + id. Created with `CREATE DATABASE` using `pq.QuoteIdentifier`.
- The router re-validates the `recurso_` prefix and id before ever connecting, so a tampered
  `tenants.database_name` (e.g. `postgres`) is refused.

## Routing (driver level)

Why this way: Recurso builds ~70 repositories once at startup from one `*sql.DB`. Making
every repository take a per-request DB (bookshelf style) would be a huge refactor. Instead
the `*sql.DB` itself is made tenant-aware.

- `NewRoutedDB(sharedDSN, route, opts...)` returns a `*sql.DB` backed by a custom
  `driver.Connector` (`internal/adapter/db/routing.go`).
- `routedConn` wraps a pq connection. On EVERY statement it asks `route(ctx)` which database
  the request's tenant (`domain.TenantIDKey` on ctx) needs and re-points itself first.
  Re-pointing = park the old connection in a bounded warm cache (or close it), then take a
  warm connection for the target database or dial a new one.
- Never switches inside a transaction.
- **Fails closed**: if route lookup fails, the query errors. It never falls back to shared.
- `TenantRouter` maps tenant -> database name from `tenants.database_name` in the shared DB,
  cached 60 s, with `Forget(tenantID)` called right after a database is assigned.

### Why per-statement re-pointing (important history)

First design relied on `ResetSession` + returning `ErrBadConn`. `database/sql` hands pooled
connections to waiting callers without checking which database they need, so under load
tenant A's connection was served to tenant B and "driver: bad connection" appeared. The
per-statement check removes that whole class of bug. Do not go back to the first design.

### Cache options (`RoutedDBOptions`)

| Option | Library default | main.go value | Meaning |
|---|---|---|---|
| MaxOpen | 25 | env `DEDICATED_DB_MAX_OPEN` (25) | active slots per replica |
| MaxCachedConns | 50 | env `DEDICATED_DB_MAX_CACHED` (200) | warm parked connections |
| MaxCachedPerDB | 2 | env `DEDICATED_DB_MAX_CACHED_PER_DB` (2) | per-database cap in cache |
| CachedConnIdle | 2m | - | idle close time |

Slot pool: `SetMaxIdleConns(MaxOpen)`, 5 minute max lifetime. Size the warm cache at or
above the number of ACTIVE tenants, otherwise most requests pay a reconnect.

## Signup flow

`POST /auth/register` with optional `dedicated_database_id` (handler `internal/adapter/handler/auth.go`):

1. Field set but no valid `X-Provisioning-Token` -> 403 (constant-time compare against env
   `DEDICATED_DATABASES_PROVISION_TOKEN`; unset token = always 403). Register is a public
   endpoint, so without this anyone could create databases.
2. `AuthService.RegisterWithDatabase` (`internal/service/auth.go`):
   - empty id -> normal shared path, nothing else happens
   - feature disabled -> `ErrDedicatedDatabaseDisabled` (400)
   - `Provision` creates + migrates `recurso_<id>` (invalid id 400, exists 409)
   - create tenant in shared DB
   - `Assign`: seed tenant into the new DB, then `SetDatabaseName`, then router `Forget`
   - if tenant creation or Assign fails, the new database is dropped (detached 30 s context)
3. On success `tenant.DatabaseName` is set.

## Other fixes made on the way

- `RunMigrations` never closed its migrate handle (leaked one server connection per call).
  Fixed with `defer m.Close()`. Matters because provisioning calls it per signup.
