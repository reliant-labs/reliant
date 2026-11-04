# Dashboard postgres panels: live check and rollback

A `postgres` panel reads one environment's own database through
control-plane's `AppDatabaseService` (`QueryDatabasePanel`, and
`ListDashboardDatabases` for the editor's schema browser), the one caller in
`frontends/web/src/services/controlPlane/observability/appDatabaseApi.ts`. What
that depends on is only visible in a hosted environment, so it is checked after
the release that ships it reaches prod.

## Check after deploy

Whoever merged the change (the PR's owner) runs these against prod once the
release is live:

1. The deployed control-plane is at least `v1.7.87` (the release with
   `AppDatabaseService`).
2. With the `db:query` grant: the editor's schema browser lists the
   environment's enabled databases and their tables, and a panel running
   `SELECT 1` draws a figure.
3. Without the grant the panel shows the denial; for another organization's
   environment it shows NotFound; for a database whose dashboard role is not
   provisioned the browser shows the database with its `tables_error`, and
   existing dashboards still draw.

## Rollback

If any check fails, the same person reverts it:

1. Open a PR that is `git revert` of the merged commit, labelled
   `release:asap`. The pipeline reviews and merges it, and `rc-train` cuts the
   release right after, which redeploys the web console without the caller.
2. Nothing on the server changes, so nothing else is undone: this change adds no
   migration, writes no data, and dashboards are stored by control-plane's
   `DashboardService`, untouched by the revert.
3. Existing dashboards are preserved. Every non-postgres panel draws as it did
   before. A postgres panel saved in the meantime stays in its dashboard and,
   since the reverted console does not list `postgres` in `DRAWN_KINDS`, draws
   "This console doesn't draw postgres panels yet" and never runs a query
   (`panelRunsQuery`), so no call reaches `AppDatabaseService`. Re-shipping the
   fixed panel draws it again without anyone editing the dashboard.
