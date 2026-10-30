# Status: what is done

Everything below is uncommitted in `D:\InvoiceShelf\recurso` (by the user's instruction).
Last verified: no-database test suite passes; Postgres-backed suite: 50 packages ok, only
the existing `TestSlowQueryLog_Postgres` fails (slow-log code/test is byte-identical to
upstream main, so not caused by this work). OpenAPI drift tests pass.

## Files added / changed for this feature

| File | What |
|---|---|
| `internal/adapter/db/tenant_database.go` | name rule, DSN rewrite, `ProvisionTenantDatabase`, `DropTenantDatabase` (`DROP ... WITH (FORCE)`), `SeedTenantInDatabase` |
| `internal/adapter/db/tenant_database_test.go` | name-rule + DSN unit tests; Postgres provisioning test (needs `TEST_DATABASE_URL`) |
| `internal/adapter/db/routing.go` | `NewRoutedDB`, `routingConnector`, `routedConn`, warm cache, `TenantRouter` |
| `internal/adapter/db/routing_pg_test.go` | isolation, transactions, 60x40 concurrency, fail-closed cases |
| `internal/adapter/db/migrate.go` | `m.Close()` leak fix |
| `internal/adapter/db/migrations/000177_tenant_database_name.{up,down}.sql` | `tenants.database_name` + unique partial index |
| `internal/adapter/db/tenant_repository.go` | `SetDatabaseName`, `GetDatabaseName` |
| `internal/core/domain/tenant.go` | `DatabaseName` field |
| `internal/service/tenant_database.go` | `TenantDatabaseManager` (Provision / Assign / Drop / OnAssign) |
| `internal/service/auth.go` | `ConfigureDedicatedDatabases`, `RegisterWithDatabase`, rollback |
| `internal/service/auth_dedicated_db_test.go` | 4+ tests with a fake provisioner |
| `internal/adapter/handler/auth.go` | `dedicated_database_id`, token gate, error mapping |
| `internal/adapter/handler/auth_test.go` | token-gate tests |
| `cmd/api/main.go` | `envInt`, routed DB wiring, manager wiring, token wiring |
| `cmd/api/openapi.yaml` | `/auth/register` documents the field, header, 400/403/409 |

Earlier, separate work (manual invoice API, NOT part of this feature): migration 000176,
`manual_invoice` service/handler/tests, `scripts/ar_import.py`, `docs/ar-import-runbook.md`,
`CHANGELOG.md`, `docker-compose.override.yml`. Upstream PR patch lives in
`D:\InvoiceShelf\upstream_patch\`; the user raised that PR themselves.

## Added 2026-10-29 (step 1)
Template-clone provisioning (`createFromTemplate`, `EnsureTenantTemplate`, `TenantTemplateName`
in `tenant_database.go`), `OnFirstUse` hook in `routing.go`, wiring in `cmd/api/main.go`.

## Added 2026-10-29 (step 2: tenant from the link)
`internal/adapter/middleware/workspace.go` (+test), `internal/adapter/handler/workspace.go` (+test),
checkout/payment/webhook handler edits, `routes_public.go`, `WorkspaceLinker` in
`service/notification.go`, `scheduler/dunning.go`, `service/invoice.go`, main.go wiring,
`frontend/src/pages/Checkout.jsx`. See 03-PENDING item 4.

## Added 2026-10-29 (step 3: background jobs)
`internal/workspace/` (+tests), `workspace.Each` in 8 schedulers and 7 workers, per-tenant context
in MRR/nexus/reconciliation, `TenantRepository.ListDedicatedTenantIDs`, renewal tenant fix,
detached-context fixes. See 03-PENDING item 3.

## Added 2026-10-29 (step 4: tenant-less entry points)
Portal (`service/portal.go`, `middleware/portal_auth.go`, `routes_portal.go`, frontend
`PortalLogin/PortalVerify`), accounting OAuth callback tenant, audit-log tenant fix,
`workspace.Link`, dunning/card-expiry/pre-charge/trial links, webhook routing-tenant test.

## What is routed to the dedicated database today
Everything business-related in `cmd/api/main.go` uses `routedDB`/`routedDBX` (customers,
invoices, plans, subscriptions, usage, coupons, wallets, credit notes, quotes, disputes,
dunning, revenue recognition, webhooks/events, tax/branding settings, analytics, audit log,
etc.). The control-plane allowlist is in `cmd/api/dedicated_db_wiring_test.go`.

**Still incomplete:** background jobs (03-PENDING item 3), tenant-less `...Public` lookups,
the customer portal, and the accounting OAuth callback. Do not use for production until
those are done and the unexercised flows in item 2 are verified.

## Verified end to end locally

A dedicated and a shared workspace were created; customer and invoice data landed in the
correct databases. Signup rollback and token gate are covered by unit tests.
