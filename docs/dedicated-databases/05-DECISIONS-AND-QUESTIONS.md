# Decisions and open questions

## Decided

| Decision | Why |
|---|---|
| Optional identifier at signup; empty = shared DB | user requirement; keeps existing behaviour unchanged |
| Name = `recurso_<id>`, lowercase only | user confirmed lowercase is fine |
| Id rule `^[a-z0-9][a-z0-9_]{0,39}$` | safe identifier, fits Postgres 63-char limit with prefix |
| Control-plane tables stay shared | needed before the tenant is known |
| Driver-level routing on one `*sql.DB` (option A) | avoids refactoring ~70 repositories; fine for 100-200 tenants |
| Fail closed on route errors | never leak data across tenants by falling back to shared |
| Provisioning gated by `X-Provisioning-Token` | `/auth/register` is public |
| Drop the new DB if signup fails afterwards | no orphan databases |
| Do not commit locally | user instruction |

## Option B (not chosen): bookshelf style
Pool per tenant (`TenantConnectionManager`, 30 min idle eviction, 25 open / 5 idle each) with
repositories taking the DB per request. Better at thousands of tenants, but requires touching
every repository. Revisit only if tenant count grows well beyond a few hundred.

## Open questions (need the user)

1. **Stays-shared list.** Proposed: tenants, users, sessions, API keys, MFA, password reset,
   email verification, OAuth/SSO, organizations. Anything else that must be visible across
   workspaces?
2. **No-tenant requests** (gateway webhooks, hosted checkout, portal links): put tenant id in
   the URL/token (simple, new links only) or a shared invoice->tenant lookup table (also
   covers existing links)? Recommended: tenant id in URL for new links + lookup table only
   if old links must keep working.
3. **Cross-workspace reports** (founder view, organization-wide MRR) needed, or is each
   workspace independent?
4. **Migrate existing workspaces** into dedicated databases? Recommended: no, new ones only.
5. **TigerBeetle**: dedicated workspaces would use ledger 1 like shared tenants do today.
   OK as is?
6. **Naming**: `recurso_<id>` (current) vs bookshelf's convention (shared per-tenant database
   with a `recurso` schema / `bookshelf_<id>`). Bookshelf notes and code disagree with each
   other; decide before production.
7. **`svc-tenant-provisioner`**: should Recurso use it instead of creating databases itself?
8. **Who calls signup** (a platform provisioning service?). The token gate assumes a trusted
   server-side caller, not a browser.
