---
name: v1.0.14
description: Upgrade a project to forge v1.0.14. The dashboard role (forge_dash) is granted by ordinary migrations, not annotations - the forge:dashboard / forge:no-dashboard markers, the generated db/grants/dashboard_gen.sql and db/grants/dashboard/, and the forge-db-grants-stale lint rule are gone, and the migrator now creates forge_dash before any migration runs and fails a run after which it holds more than read privileges. Use when the project's migrations carry forge:dashboard or forge:no-dashboard, it commits db/grants/dashboard_gen.sql, Go code reads the removed dashboard fields of pkg/migratekit or pkg/dbgrants, or the project runs Postgres 14, where every project's migrations fail until the owner of the public schema revokes PUBLIC's CREATE on it. Also: the scaffolded GitHub Actions workflows group push-to-main runs per workflow and ref instead of per commit, so a newer push supersedes the pending run; use when a .github/workflows file still groups on github.sha. Also: `forge.flux_chart()` installs Flux through the Flux Operator and a FluxInstance (see migrations/v1.0.14/flux-operator); use it when deploy/kcl declares `forge.flux_chart(`.
version: v1.0.14
detection: grep -qsE 'flux_chart\(' deploy/kcl/*/*.k deploy/kcl/lib/*.k && exit 0; test -f db/grants/dashboard_gen.sql -o -d db/grants/dashboard && exit 0; grep -rqsE --include='*.go' --exclude-dir=gen --exclude-dir=vendor --exclude-dir=node_modules 'Dashboard(Applied|RoleDisabled|RoleMissing|GrantsPath|GrantsRowsDir|GrantsFile|Tables)\b|RenderDashboard|ApplyDashboard|DeclaresNoDashboard|TableMarkerDashboard|ColumnMarkerNoDashboard' . && exit 0; grep -qsE 'postgres_version:[[:space:]]*"?v?14' forge.yaml && exit 0; grep -rqsE 'postgres:14' docker-compose.yml docker-compose.yaml compose.yml compose.yaml deploy && exit 0; grep -qsF "format('-{0}', github.sha)" .github/workflows/*.yml && exit 0; exit 1
---

# Upgrading to v1.0.14

**What changed.** forge stopped deciding what the dashboard role reads. `forge_dash` keeps its whole lifecycle (the `ManagedDatabase.dashboards` opt-in, the CNPG managed role and its connection limit, the `<name>-forge-dash` password Secret, NOLOGIN when off), but its access is now granted by your own migrations:

```sql
GRANT SELECT (id, created_at, model, cost_cents) ON messages TO forge_dash;
```

Gone: the `forge:dashboard` table marker, the `forge:no-dashboard` column marker, the generated `db/grants/dashboard_gen.sql` and `db/grants/dashboard/`, the migrator step that applied them, and the `forge-db-grants-stale` lint rule. Unchanged: `forge_ro`, its `forge:secret` / `forge:readable` markers and `db/grants/readonly*`.

New, in every migration run (`db migrate up`, boot-time AutoMigrate, `testkit.NewMigratedPostgresDB`): the migrator creates `forge_dash` NOLOGIN before applying anything when the cluster has none, so a migration may GRANT to it everywhere; and after the migrations it **fails** if `forge_dash` can effectively do anything but read: a write, `CREATE` on a schema, owning an object, a parameter ACL, a membership, counting what it holds through PUBLIC. The contract and the rules for writing grants: `forge skill load db/dashboards`.

Apply the sections whose "Use when" matches.

## 1. Turn the markers into a GRANT migration

**Use when** a migration carries `forge:dashboard` or `forge:no-dashboard`, or the project commits `db/grants/dashboard_gen.sql`.

On v1.0.14 `forge generate` fails while the applied schema still carries either marker (`forge:dashboard was retired in forge v1.0.14 …`), and it deletes forge's own copies of the old dashboard grants files. So read what they granted **before** regenerating, from git if generate already ran:

```bash
ls db/grants/dashboard/                                   # one file per table
git show HEAD:db/grants/dashboard/public.messages_gen.sql # if generate removed them
```

Each row file holds one row, `('<schema>', '<table>', ARRAY['col', …]::text[])`: the columns forge granted (the marked table's columns `forge_ro` may read, minus the `forge:no-dashboard` ones). A row with `ARRAY[]::text[]` is an unmarked table and grants nothing.

1. `forge db migration new dashboard_grants`.
2. For each row with columns, write `GRANT SELECT (<those columns>) ON <schema>.<table> TO forge_dash;`. Review each list against the rules in `db/dashboards` before you keep it: the old projection granted every readable column of a marked table, and a panel usually needs fewer. Drop any PII or free text.
3. In the same migration, clear the markers, because the retired ones fail generate. `COMMENT ON` replaces the whole comment, so keep any other marker or text it carried:
   ```sql
   COMMENT ON TABLE usage_events IS 'forge:append-only';   -- was 'forge:append-only forge:dashboard'
   COMMENT ON TABLE messages IS NULL;                      -- was 'forge:dashboard'
   COMMENT ON COLUMN chats.title IS NULL;                  -- was 'forge:no-dashboard'
   ```
4. Head the file `-- forge:backward-compatible — dashboard grants and comments only`.
5. Leave the migration that added the markers as it is: it has run everywhere. `forge lint --column-markers` does not report a retired marker in migration text; `forge generate` judges the applied schema.
6. `forge generate`, then commit the migration together with the deletions of `db/grants/dashboard_gen.sql` and `db/grants/dashboard/`. A hand-edited or disowned copy is left for you: delete it.

**Databases already deployed** keep the column grants the old script last applied until this migration runs; nothing revokes them in between. `GRANT` is idempotent, so the migration changes nothing there unless you narrowed a list, and makes a fresh database match. To narrow what a deployed database already holds, revoke explicitly in the same migration (`REVOKE ALL ON <table> FROM forge_dash;` before the `GRANT`): granting fewer columns does not take the others away.

## 2. The Go API lost its dashboard-grants half

**Use when** Go code names any of these.

| Removed | Instead |
|---|---|
| `migratekit.Grants.DashboardApplied`, `.DashboardRoleDisabled`, `.DashboardRoleMissing` | `migratekit.Result.Dashboard` (`*migratekit.DashboardRole`: `Created`, `Missing`, `CanLogin`, `SecurityDefiner`) |
| `migratekit.DashboardGrantsFile`, `migratekit.DashboardGrantsRowsDir` | nothing: there is no file |
| `dbgrants.DashboardTables`, `RenderDashboard`, `RenderDashboardFor`, `RenderDashboardFiles`, `ApplyDashboard`, `DashboardGrantsPath`, `DashboardGrantsRowsDir`, `Table.Dashboard`, `Column.NoDashboard` | grants are migrations; `dbgrants.EnsureDashboardRole` / `EnsureRole` and `dbgrants.DashboardPrivilegeViolations` (returning `[]dbgrants.Violation`, each with its fix) are the lifecycle and the check |
| `schemadef.TableMarkerDashboard`, `ColumnMarkerNoDashboard`, `Table.Dashboard()`, `Column.DeclaresNoDashboard()` | `schemadef.RetiredMarker` |

A run can now fail two new ways: `*migratekit.DashboardRoleMissingError` (the role does not exist, the migrating role may not create it, and a pending migration names it; nothing was applied, so retry once the role exists) and `*migratekit.DashboardPrivilegeError` (the role can do more than read; its `Violations` name each object, privilege and fix; fix with a migration that revokes it).

**A migration runner of your own** (one that applies the files without `migratekit` or `testkit`) must call `dbgrants.EnsureDashboardRole(ctx, db)` before the first file, or a migration that grants to `forge_dash` fails on a fresh database with `role "forge_dash" does not exist`.

## 3. Delete `forge-db-grants-stale` from `lint.rules`

**Use when** `forge.yaml` names `forge-db-grants-stale` under `lint.rules`.

The rule is gone, so the entry is reported as a rule ID nothing has. Delete it.

## 4. `forge db squash` now keeps the dashboard role's and PUBLIC's privileges

**Use when** you squash a forge history (`forge db squash`).

The squash used to dump with `--no-privileges`. It now keeps every `GRANT … TO forge_dash` and every `GRANT … TO PUBLIC` / `REVOKE … FROM PUBLIC`, and drops every other privilege statement, so a baseline that replaces your migrations still grants what they did and still takes away what they took from PUBLIC (`REVOKE EXECUTE ON FUNCTION f() FROM PUBLIC`). Nothing to do; re-squash if a baseline written by an earlier forge replaced such a migration.

## 5. A run can fail on what PUBLIC holds

**Use when** the database runs Postgres 14 (or was upgraded from it), or a migration grants anything to PUBLIC. This applies to every project, dashboards or not: the migrator creates `forge_dash` and checks it on every run.

The check counts PUBLIC's privileges as the dashboard role's, because they are. On Postgres 14 PUBLIC can `CREATE` in the `public` schema by default, so the first run on v1.0.14 fails naming `CREATE on schema public, held through PUBLIC`. The fix is `REVOKE CREATE ON SCHEMA public FROM PUBLIC;` (Postgres 15's default), and **it must run as the owner of `public`**, which on Postgres 14 is the bootstrap superuser (`postgres`), not your application's role. Run by any other role, the REVOKE "succeeds" with only `WARNING: no privileges could be revoked`, so a migration your application role applies changes nothing and every run keeps failing. Have whoever administers the server run it once (`psql -U postgres -d <db> -c 'REVOKE CREATE ON SCHEMA public FROM PUBLIC'`), or ship it as a migration only where the migrator connects as that owner. `task test` passes on Postgres 14 regardless, because forge's test databases already revoke it: check production before you upgrade. A `GRANT SELECT ON t TO PUBLIC` or a write granted to PUBLIC fails the same way: revoke it from PUBLIC and grant the roles that need it. forge does not revoke anything itself.

## 6. A newer push to main supersedes the pending CI run

**Use when** a workflow under `.github/workflows/` still groups on the commit: its `concurrency.group` ends in `${{ github.event_name != 'pull_request' && format('-{0}', github.sha) || '' }}`.

**What changed.** The workflows forge scaffolds (`ci.yml`, `build-images.yml`, `e2e.yml`, `proto-breaking.yml`) gave every push to main a concurrency group of its own commit, so every merge queued a full run and a burst of merges queued one run per merge. Now every push to main shares one group per workflow and ref, with `cancel-in-progress` still true only on a pull request. GitHub keeps at most one pending run per group: when a newer push queues, the older *pending* run is cancelled, and the run *in progress* always finishes. A burst of merges costs the run in progress plus one run of the newest commit; the commits between get no run of their own, and a superseded run shows as cancelled. A workflow that also runs on a dispatch (`build-images.yml`, `e2e.yml`) keys its group on `github.event_name` too, so a manual run never supersedes a push run. A `merge_group` run stays alone in its group, because its ref names the group's own head.

```yaml
# before
concurrency:
  group: ci-${{ github.ref }}${{ github.event_name != 'pull_request' && format('-{0}', github.sha) || '' }}
  cancel-in-progress: ${{ github.event_name == 'pull_request' }}
# after
concurrency:
  group: ci-${{ github.ref }}
  cancel-in-progress: ${{ github.event_name == 'pull_request' }}
```

**What generate does.** Nothing to a workflow that exists. Workflows are scaffolded once and then yours: `forge generate` writes one only when it is missing, so a new project, or a workflow you deleted, gets the new groups. For the rest:

1. `forge project upgrade --check` names every workflow whose template moved on; `forge project upgrade --check .github/workflows/ci.yml` shows the diff.
2. A workflow you never edited: `forge project upgrade --force .github/workflows/ci.yml` (and each other one named) adopts the template.
3. A workflow you edited: change its `group:` line by hand, as above. `--force` would discard your edits. If it also runs on `schedule`, `workflow_dispatch` or `workflow_call`, key the group on the event: `group: <name>-${{ github.event_name }}-${{ github.ref }}`.

Never set `cancel-in-progress: true` on a workflow that runs on push to main: it cancels the run in progress, and a busy runner pool then leaves main with no verdict at all.

If a release process of yours needs a CI verdict for *every* commit of main, keep the per-commit group in that one workflow: it is your file.

## 7. `forge.flux_chart()` installs Flux through the Flux Operator

Use when deploy/kcl declares `forge.flux_chart(`. The chart is renamed
`flux-operator` and adopts an existing Flux in place. See
`migrations/v1.0.14/flux-operator`.
