# Dedicated database per workspace - start here

If you lost context, read these in order:

1. [01-ARCHITECTURE.md](01-ARCHITECTURE.md) - how it works and why it was built this way
2. [02-STATUS.md](02-STATUS.md) - what is done, file by file
3. [03-PENDING.md](03-PENDING.md) - what is left, in order, with how to do each item
4. [04-TESTING.md](04-TESTING.md) - how to run and extend the tests
5. [05-DECISIONS-AND-QUESTIONS.md](05-DECISIONS-AND-QUESTIONS.md) - decisions made, open questions
6. [06-CONFIG-AND-OPERATIONS.md](06-CONFIG-AND-OPERATIONS.md) - env vars, connection sizing, runbook
7. [07-BACKUP-AND-DELETE-RUNBOOK.md](07-BACKUP-AND-DELETE-RUNBOOK.md) - backup, restore, deleting a workspace database

## One-paragraph summary

At workspace signup the caller may pass an optional `dedicated_database_id`.
If set (and the request carries a valid `X-Provisioning-Token`), the system creates
Postgres database `recurso_<id>`, migrates it, copies the new tenant row into it,
records the name in `tenants.database_name`, and from then on routes that workspace's
business-data queries to that database. If empty, the workspace uses the normal shared
Recurso database exactly as before. Feature is off unless `DEDICATED_DATABASES_ENABLED=true`.

## Standing rules from the user

- Do NOT git commit in `D:\InvoiceShelf\recurso`. All work is uncommitted on purpose.
- Tell the user the plan before building anything big.
- Target scale: 100-200 tenants, 2 replicas (pods), production use.
- Goal: eventually raise an upstream PR to `recurso-dev/recurso` from fork `snavale230/recurso`
  (browser/web UI only, under the `snavale230` account, not the droadmap account).
  This feature is a SEPARATE PR from the manual-invoice API PR.
- Reference implementation in the user's org: `D:\Krackend-Notification\svc-bookshelf`
  (database per tenant). Do not copy it blindly; pick what is best for Recurso.

## Environment facts

- Windows, Git Bash `grep` is broken (use the Grep tool or awk).
- Go is at `/c/Program Files/Go/bin`.
- Docker Postgres container `recurso-postgres-1` (user `user`, password `password`, `max_connections` 100).
- Laptop Docker networking produces "connection forcibly closed" and 30 s stalls under heavy
  connection churn. That is environmental. Do not trust load numbers from the laptop.
- The API binary must be started from the repo folder (`LoadHTMLGlob` uses a relative path).
