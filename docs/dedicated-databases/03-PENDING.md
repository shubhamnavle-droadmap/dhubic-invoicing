# Pending work (in recommended order)

Mark items done here as you go and add a date.

## 1. Fast, safe provisioning  [DONE 2026-10-29]
- New databases are cloned from `recurso__template` (`EnsureTenantTemplate` at startup,
  `createFromTemplate` with retry on "template busy" and fallback to migrate-from-empty).
  Measured clone provisioning ~0.45 s vs ~5.9 s.
- Migrate on first use: `RoutedDBOptions.OnFirstUse` (main.go passes `db.RunMigrations`) runs
  once per database per process, serialised per database; failure fails the query and retries.
- Tests: `TestProvisionFromTemplate_Postgres`, `TestRoutedDB_OnFirstUse_Postgres`.
- Remaining caveat: a database nobody has touched since a release is only migrated when
  first used (by design).

## 2. Route the rest of the business data  [DONE for main.go wiring 2026-10-29]
Not done in groups: the foreign-key graph (plans <- subscriptions <- invoices <- credit notes,
customers <- almost everything) means a partial move breaks inserts. All business repositories
(62 constructions) were switched to `routedDB`/`routedDBX` in one pass; see 02-STATUS.
- Stays shared (allowlist in `cmd/api/dedicated_db_wiring_test.go`, which FAILS if a new
  construction in main.go uses the plain handle): tenants, users, sessions, password reset,
  email verification, MFA, OAuth, SSO, organizations, platform, cloud billing/usage/charges,
  gateway connections, waitlist, magic links + portal sessions (portal login has no workspace
  yet), telemetry, signup primary-country stamp.
- Foreign keys from business tables to `users`, `gateway_connections`, `organizations` cannot be
  satisfied in a dedicated database, so `PrepareTenantDatabase` drops them (on provision and on
  first use after every release). Without this, credit notes (created_by user) and customers
  with a gateway connection would fail to insert.
- Verified end to end (running API, dedicated workspace): plan, customer, subscription (and its
  invoice), wallet, billable metric, webhook endpoint all landed in `recurso_<id>`; nothing for
  that tenant appeared in the shared database.
- Also verified end to end (2026-10-29): coupon, quote, manual invoice, credit note (needs the
  dropped user FK), offline payment, usage event, second entity, wallet; all 68 `GET /v1` list /
  report endpoints return no 5xx for a dedicated workspace.
- Found and fixed while doing that: the audit-log writer used a detached context without the
  tenant, so audit rows for dedicated workspaces landed in the SHARED database.
- NOT yet exercised: card/gateway payments and refunds with a real gateway, disputes, gifts,
  referrals, revenue-recognition runs, accounting/integration sync, e-invoicing, exports, MRR
  analytics output. Run each area's flow (or its Postgres tests with a dedicated tenant).
- Code that looks records up WITHOUT a tenant (`...Public` methods, 54 uses in 19 files: credit
  notes, disputes, gifts, portal service, dunning campaigns, e-invoice worker, GoCardless
  webhooks) now searches the shared database only, so it will not find dedicated records.
  Each needs a tenant on the context (step 3 below / portal work) or a lookup.
- Accounting OAuth callback (`/v1/accounting/callback/:provider`) has no session: confirm the
  signed state carries the tenant and set it on the context before the connection is stored.

## 3. Background jobs and workers  [DONE except the items below, 2026-10-29]
`internal/workspace` (package doc explains it): `workspace.Each(ctx, fn)` runs `fn` once for the
shared database exactly as before, then once per dedicated workspace with that tenant on the
context. `workspace.Configure(tenantRepo.ListDedicatedTenantIDs)` is called in main.go only when
`DEDICATED_DATABASES_ENABLED=true`; otherwise `Each` is a plain call (no behaviour change).
Failure/panic in one workspace is logged and does not stop the others; listing failure still
runs the shared pass.
- Wrapped with `Each` (global scans): billing cycle (renewals, wallet expiry/auto-recharge,
  credit expiry, usage alerts), dunning, mandate debit, trial, card expiry, pre-charge,
  progressive billing, subscription resume; workers: retry, e-invoice (IN), EU e-invoice,
  webhook delivery, dunning campaign, revenue recognition, accounting sync.
- Loops over `ListTenants` now carry each tenant on the context: MRR snapshot, nexus,
  ledger reconciliation (churn, CRM sync and S3 export already did).
- Fixed a latent bug that also affected shared tenants: `RenewalService.ProcessDueRenewals`
  renewed each subscription with a context that had no tenant, so plan lookups failed with
  "tenant_id missing from context". It now carries `sub.TenantID`.
- Detached contexts that dropped the tenant now keep it: invoice-created email goroutine,
  accounting manual sync + status write, smart-retry next-time helper.
- Verified on a running API: a dedicated workspace's due subscription was renewed (new invoice,
  period advanced, in `recurso_<id>`, nothing in shared); its overdue invoice was claimed by
  dunning, emailed, handed to the retry worker.
NOT DONE / to check:
- Recurso Cloud usage metering (`CloudUsageScheduler`) runs one cross-tenant SQL against the
  shared database, so dedicated workspaces are not counted. Only matters for the hosted
  platform's own billing. Needs per-workspace aggregation written back to shared.
- `demo_reset` and `health_alert` intentionally not fanned out.
- Jobs hold their Redis lock across the whole fan-out (10-30 min TTL). With many workspaces and
  slow work, raise the TTLs or the lock could lapse mid-run; claims still prevent double work.
- Each job's remaining per-item code paths were not all exercised (mandate debit, progressive,
  trial conversion, card expiry, pre-charge, e-invoice, webhook delivery, rev-rec, accounting
  sync on a dedicated workspace). Add a test per job.

## 4. Requests with no logged-in tenant  [MOSTLY DONE 2026-10-29]
Decision: workspace marker is the query parameter `?w=<tenant-uuid>` (not a `/w/...` path:
no new routes, no OpenAPI duplication, SPA and nginx need no routing change).
- `middleware.WorkspaceFromQuery()` on `/checkout/:id`, `/checkout/:id/pay`, `/success`,
  `/razorpay/verify`, `/payments/order`. Invalid value -> 400; absent -> unchanged.
- `handler.workspaceMismatch`: a `?w=` for workspace A cannot open B's invoice (404). Applied at
  the 5 invoice lookups in checkout.go / payment.go.
- Webhooks `/webhooks/{stripe,razorpay,gocardless}/:connID`: `webhookSecretFor` now also puts the
  connection's tenant on the request context for routing. `gateway_connections` stays SHARED.
- Plain `/webhooks/{provider}` (no connID, platform env keys) cannot see dedicated databases;
  dedicated workspaces must use their per-connection URLs.
- Links: `NotificationService.SetWorkspaceLinker` and `DunningScheduler.SetWorkspaceLinker`
  append `?w=` for tenants that have a dedicated database (main.go closure over TenantRouter).
- Frontend `frontend/src/pages/Checkout.jsx` forwards `?w=` on all API calls and keeps it in
  Stripe `return_url`. NOT run: node/npm are not installed on this machine - run `npm test`
  in `frontend/` before merging.
- E2E verified against a running API: dedicated invoice -> checkout 404 without `?w=`, 200 with
  it, 404 with another workspace id, 400 with a malformed id.
Customer portal [DONE 2026-10-29]: `magic_links` and `portal_sessions` are routed (business data,
FK to customers). `?w=` on `/portal/auth/request` and `/portal/auth/verify`; the emailed link
carries `&w=`; the session token of a dedicated workspace is `<tenant-uuid>.<random>`, so the
cookie alone names the workspace (`service.TenantFromSessionToken`, `PortalAuthMiddleware` puts
it on the context). Shared workspaces' tokens/links are unchanged. Verified on a running API:
dedicated request -> verify -> profile/invoices with only the cookie; a shared customer's flow
unchanged (no prefix). Frontend `PortalLogin.jsx` / `PortalVerify.jsx` forward `?w=` (NOT run:
no node). Lifecycle emails (dunning, card expiry, pre-charge, trial) add `?w=` via
`workspace.Link(ctx, ...)` on a dedicated workspace's pass.
Accounting OAuth callback [DONE]: tenant from the HMAC-verified state is put on the context.
Per-connection webhooks [DONE + unit test]: `webhookSecretFor` sets the routing tenant.
STILL OPEN: admin-generated "portal login" links in `Customers.jsx` / `CustomerPage.jsx` do not
add `?w=` yet (the dashboard does not know the workspace has a dedicated database); plain
`/webhooks/{provider}` URLs (platform env keys) cannot reach dedicated databases.

## 5. Verification  [size: medium]
- Full flow on a dedicated workspace: plan -> customer -> subscription -> invoice -> payment
  -> webhook -> reports.
- Load test at ~150 workspaces, 2 replicas, on a LINUX server (laptop numbers are unreliable).
- Measure per-database disk size.

## 6. Connection sizing / ops  [size: small]
Worst case per replica today: 25 (unrouted pool) + 25 (routed slots) + 200 (cache) = 250,
so ~500 for 2 replicas vs Postgres default `max_connections` 100. Either set server
`max_connections` ~600 or lower `DEDICATED_DB_MAX_CACHED`; consider PgBouncer (transaction
pooling needs care with this driver-level routing - test before adopting).

## 7. Smaller items  [DONE 2026-10-29 unless marked]
- DONE signup country stamped on the dedicated entity too (verified: IN / GB).
- DONE job lock lease x `DEDICATED_JOB_LOCK_FACTOR` (default 3) via `workspace.LockTTL`.
- DONE OpenAPI documents `?w=` (`components.parameters.Workspace`) on checkout, payments/order, portal auth.
- DONE `cmd/drop_workspace_db` + `db.DropWorkspaceDatabase` (canceled only, exact-name confirm, tested).
- DONE backup / restore / delete runbook (07).
- DONE plain-webhook "invoice not found" logs carry a hint to use the per-connection URL.
- DONE (frontend, NOT run - no node here) admin "copy portal link" buttons add `&w=<tenant id>` for a dedicated
  workspace: `frontend/src/lib/portalLink.js` (+ test), used by `Customers.jsx` and `CustomerPage.jsx`, which
  read `GET /v1/account` (`id`, `database_name`). Existing page tests got a `getAccount` mock. Run `npm test`.
- OPEN default-setup hook for new dedicated tenants (none needed so far); operator/website-server "bucket name" input.
- DONE `DEDICATED_DATABASES_MAX` hard cap (429 `rate_limited` at the limit; counts `recurso_*` databases in Postgres; tests at service, handler and db level).
(original list:)
- Signup country is stamped only on the shared entity, not the dedicated one.
- Default-setup hook for new dedicated tenants (like bookshelf's new-tenant hook).
- Dashboard signup form option for the identifier.
- Document env vars (see 06).
- Remove/relocate `docker-compose.override.yml` changes before any upstream PR.

## 8. Upstream
Separate PR to `recurso-dev/recurso` after discussing with maintainers. Consider opening an
issue first (like the manual-invoice one). The PR must not contain the CSV script or local
override files.
