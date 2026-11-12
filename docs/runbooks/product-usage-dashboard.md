# Verifying the Product usage dashboard after release

`internal/obsdash` generates the `usage` dashboard (`<project>-usage_gen.json`)
into every project's spec and release bundle. It is 17 sql panels over
`product_events`, using the dashspec presentation features added with it: sql
heatmaps, `trendSql` sparklines, `defaultCompare`, section descriptions and
`display.color` / `display.colors`. Whether the console draws them as intended
can only be judged against a deployed project, so it is checked after both
halves are live in prod:

- control-plane's prod release pins this forge (`go.mod` in control-plane
  names the forge commit), so the control plane decodes and serves the new
  fields instead of refusing or stripping them;
- the reliant web release carrying reliant#2497 (`21b454b67`, already on main)
  is out, so the console renders them.

Control-plane serves the Product usage built-in from its own template copy
(`internal/dashtemplates/templates/usage.json`), which is updated only after the
pin moves.

## Check after deploy

1. **The spec validates.** In a project on this forge, after
   `reliant forge generate`, the generated usage dashboard passes the same
   checks the release cut runs, with no finding on `usage`:

   ```bash
   reliant forge obs dashboards check
   ```

2. **The console draws it.** Deploy a project (`reliant forge env deploy <env>`),
   open its Product usage dashboard in the console and confirm:

   - the four section headings (At a glance, Users, What users do, Retention),
     each with its description;
   - the eight headline figures, each with a sparkline, and the
     previous-period comparison on by default (no Compare click needed);
   - the weekly retention heatmap, with empty cells (not zeros) for weeks that
     have not happened yet;
   - the named colours: active users blue, rolling windows cyan and violet,
     new users amber, events fuchsia, anonymous slate.

3. **No decode or validation errors.** The control plane's admin-server logs
   carry no dashboard decode or validation error for the release or for the
   `usage` template. From a control-plane checkout:

   ```bash
   reliant forge obs logs prod 'service:admin-server body:dashboard' --severity warn --severity error --since 1h
   ```

4. `reliant forge obs check prod --wait --json` (from the control-plane
   checkout) exits 0 for the control-plane release that carried the pin.

## Rollback

The change is presentation only: no schema, no stored data, no telemetry
pipeline. Each release bundle carries its own dashboards, so a project's
running release keeps the set it shipped with. If the control plane has a
problem with the new fields, revert this PR's forge commits (one revert PR
through the pipeline) and re-pin control-plane to the reverted forge commit
(`task pin:forge` in control-plane), which promotes like any release. A project
that wants the previous layout without waiting can override the dashboard with
its own `deploy/observability/dashboards/<project>-usage.json`, which replaces
the generated one.
