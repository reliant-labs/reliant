# forge_dash grants: what this repo owns, and how to verify them live

`db/migrations/20261102171444_dashboard_grants.up.sql` grants the dashboard role
`forge_dash` SELECT on a column allowlist (chats, messages, provider_accounts,
llm_call_usage, llm_call_usage_daily). This repo owns only that allowlist.
`internal/db/dashboard_grants_test.go` pins it twice: once against the catalog
(exact column grants, no table-level privilege) and once by logging in with
forge_dash's privileges (panel queries run; `chats.title`,
`provider_accounts.email`/`label`/`key_hint`/`organization_name`/`secret_sealed`,
`messages.id`, `llm_call_usage.run_id` and `projects` are refused with 42501).

## What lives outside this repo

| Concern | Owner | Where it is specified |
|---------|-------|-----------------------|
| Creating `forge_dash` NOLOGIN before any migration runs | forge's migrator | forge `db/dashboards` skill |
| Giving it a login, per database, only on opt-in | forge KCL `ManagedDatabase.dashboards` (absent = disabled) | same |
| Who may run a statement as it | control-plane `AppDatabase.QueryDatabasePanel` / `ListDashboardDatabases` | `proto-vendor/controlplane/services/app_database/v1/app_database.proto`, "LIVE DASHBOARD PANELS" |

The admission contract there: unknown environment or non-member of its org is
NotFound; a caller that is not a signed-in person holding `db:query` (any access
token included) is PermissionDenied; a database that has not opted in is
FailedPrecondition `dashboards_not_enabled`; every request is audited. Those
implementations are tested in their own repositories, not here.

Until a deploy sets `ManagedDatabase.dashboards` for reliant's database,
forge_dash has no login and these grants are inert. No environment in this
repo's `deploy/kcl/` opts in, so merging this PR changes nothing live.

## Post-merge verification plan

Run this the first time an environment opts reliant's database into dashboards
(the deploy that sets `ManagedDatabase.dashboards`), before any panel is shared.
`<env>` is that environment; every step is read-only.

1. **Grants landed.** As a person holding `db:query`:

   ```sh
   reliant forge db query <env> --confirm-env <env> --reason "verify forge_dash grants" \
     "SELECT table_name, column_name, privilege_type
        FROM information_schema.column_privileges
       WHERE grantee = 'forge_dash' ORDER BY 1, 2"
   reliant forge db query <env> --confirm-env <env> --reason "verify forge_dash grants" \
     "SELECT table_name, privilege_type FROM information_schema.role_table_grants
       WHERE grantee = 'forge_dash'"
   ```

   Pass: the first returns exactly `dashboardColumns` from
   `internal/db/dashboard_grants_test.go`, all SELECT; the second returns no rows.

2. **Panel queries run as forge_dash.** Through `QueryDatabasePanel` (a dashboard
   panel in the web app, or the RPC), run each "admitted" query from
   `TestDashboardRoleLoginReadsGrantedColumnsOnly`. Pass: each returns rows (or
   an empty result), not an error. `SELECT current_user` through the same door
   returns `forge_dash`, never `forge_ro`.

3. **Sensitive reads are refused.** Through the same door, run each "denied"
   query from that test. Pass: every one fails with permission denied (SQLSTATE
   42501); none returns a row.

4. **Admission holds.** Pass, each against `<env>`:
   - an access token (not a signed-in person) calling `QueryDatabasePanel`:
     PermissionDenied;
   - a signed-in member of the org without `db:query`: PermissionDenied;
   - a person who is not a member of the org: NotFound;
   - a database of the environment that has not opted in:
     FailedPrecondition `dashboards_not_enabled`;
   - each admitted request from step 2 appears in control-plane's audit log.

Any failure: turn dashboards off for the database (remove
`ManagedDatabase.dashboards`, which returns forge_dash to NOLOGIN) and fix
forward. A grant that is too wide is removed with a REVOKE in a new migration;
never edit the original one.
