# Verifying the built-in product analytics dashboards after release

`pkg/obsbuiltin` builds the built-in product dashboards, and `internal/obsdash`
puts them in every project's spec and release bundle:

- the Overview's _Traffic_ section (a project with a frontend) and _Users_
  section, after its existing panels, which become its _Health_ section;
- _Users_ (`<project>-users_gen.json`), _Product_ (`<project>-product_gen.json`,
  which declares `replaces: ["usage"]` and supersedes the old _Product usage_
  dashboard) and _Acquisition_ (`<project>-acquisition_gen.json`, a project
  with a frontend).

Every panel is a `sql` panel over `product_events` (and one over
`error_events`), using the dashspec presentation features: sections, `trendSql`
sparklines, heatmaps, `display.style: "funnel"`, `defaultCompare` and named
colours. Their numbers are proved by
`pkg/obsbuiltin/population_clickhouse_test.go` against a real ClickHouse; whether
the console draws them as intended, and whether the data is there, can only be
judged against a deployed project. They depend on:

- the control plane's tenant schema v28 (the `product_events` edge columns
  `DayVisitor`, `Country`, `Region`, `DeviceType`, `Browser`, `OS`, `Privacy`,
  `SaltScope`): before it a web panel's statement fails on an unknown column;
- the control plane's ingest edge stamping the daily visitor hash: before it
  every web panel reads zero (no row carries a hash), while the user panels,
  which read `UserId`, already count;
- control-plane's prod release pinning this forge, so it decodes and serves the
  bundle's new dashboards.

## Check after deploy

1. **The spec validates.** In a project on this forge, after
   `reliant forge generate`, the dashboards pass the same checks the release cut
   runs, with no finding on `users`, `product`, `acquisition` or `overview`:

   ```bash
   reliant forge obs dashboards check
   ```

2. **The console draws them.** Deploy a project with a frontend
   (`reliant forge env deploy <env>`) and open its Product group:

   - Users, Product and Acquisition are listed, and the old _Product usage_
     is not (a star on it moved to _Product_);
   - each section heading has its description, and the headline figures open
     beside their change against the previous period;
   - the retention heatmap leaves weeks that have not happened yet empty;
   - _Visit → sign-up → activation_ draws as a funnel;
   - the Overview's Traffic and Users sections follow its Health panels.

3. **The numbers agree with the Users card.** Over 7 days ending now, the
   Overview's _Active users_, _New users_ and _Returning users_ equal the
   console's Users card for the same environment (both compile from
   `obsquery.UserActivityEvidence` and `obsquery.NewUsersSource`).

4. **No decode, validation or query errors.** From a control-plane checkout:

   ```bash
   reliant forge obs logs prod 'service:admin-server body:dashboard' --severity warn --severity error --since 1h
   ```

5. `reliant forge obs check prod --wait --json` (from the control-plane
   checkout) exits 0 for the control-plane release that carried the pin.

## Rollback

The change is presentation only: no schema, no stored data, no telemetry
pipeline. Each release bundle carries its own dashboards, so a project's
running release keeps the set it shipped with. If a panel is wrong, fix it
forward in `pkg/obsbuiltin` with a release. A project that needs a dashboard
changed sooner overrides it with its own
`deploy/observability/dashboards/<project>-<id>.json` (spec form), which
replaces the generated one, or drops all of them with
`forge.Analytics {dashboards = False}`.
