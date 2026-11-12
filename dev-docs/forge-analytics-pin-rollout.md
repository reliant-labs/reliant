# forge@cdac1da2 analytics pin: post-deploy check and rollback

The 2026-11-12 forge bump (`forge-upgrade.md`, the `6e4246f3` → `cdac1da2` row)
changes two things only a live environment shows:

- **Dashboards.** `deploy/observability/spec/project_gen.json` drops the
  generated `usage` dashboard and gains forge #2075's built-in `users`,
  `product` (which declares `replaces: ["usage"]`) and `acquisition`
  dashboards, plus the Overview's Traffic and Users sections. They ship in the
  spec and bundle at the release cut. `internal/dashspeccontract` proves their
  SQL against ClickHouse before merge; whether every column they read exists in
  prod's tenant tables is only shown by prod.
- **Demo telemetry.** The demo shell (`forge-demo`, shipped with prod from
  control-plane `deploy/kcl/lib/reliant_web.k`) runs web-runtime 0.13.11's
  `full` analytics tier: `buildTelemetryConfig` sets `analytics: "full"` when
  `DEMO_TELEMETRY` is not `off`, and `start()` calls `grantAnalyticsConsent()`
  before the SDK starts (`frontends/web/src/lib/telemetry.ts`). A browser that
  sends Do Not Track or Global Privacy Control still starts no telemetry at all
  (`demoTelemetry()` is `off`). The app's own shells are unchanged: forge's
  default tier, no consent granted.

Neither adds a migration or writes server-side data.

## Check after deploy

Whoever merged the PR runs these once the rc-train release containing it is
live in prod. Record the release, the reliant commit and the time.

1. From control-plane: `reliant forge obs check prod --wait --json`. Exit 0 is
   done; 1 means investigate (run its `suggested[]` commands) and decide on the
   rollback below; 2 is **not proven good**, never "passed"; 8 needs a later
   check.
2. **Dashboards.** In the console, open the reliant project's prod
   observability dashboards. Expected: Overview, Users, Product and Acquisition
   are listed and `usage` is not; a star or copy of `usage` now points at
   Product. Over the last 24h, every panel of Users, Product, Acquisition and
   the Overview's Traffic and Users sections draws a figure or a legitimate
   empty result, and none shows a query error (unknown column or identifier,
   parse error, permission denied). Product's events-by-name counts are of the
   same order as the retired `usage` dashboard showed for the same range: both
   read `product_events`.
3. **Demo, full tier.** In a fresh browser profile with neither DNT nor GPC,
   open demo.reliantlabs.io. Expected: the banner reads "This demo records usage
   and session replays to improve the product."; requests go to `/_otel`;
   `localStorage["forge.visitor_id"]` is set and keeps its value across a
   reload. Within a few minutes the `reliant-forge-demo` service's frontend and
   Acquisition dashboards show the visit, the two page loads as one visitor,
   and a masked session replay.
4. **Demo, DNT/GPC.** Repeat with GPC on (e.g. Firefox
   `privacy.globalprivacycontrol.enabled`), then separately with DNT. Expected:
   the banner reads "Your browser asked not to be tracked, so this demo records
   nothing."; no request to `/_otel`; no `forge.visitor_id` and no `__rum_sid`.
5. **The app.** Signed out on the app host (the `reliant-web` shell), expected:
   no `forge.visitor_id` in `localStorage` and no `__rum_sid` cookie, as before
   this release.

## Rollback

If check 2 shows a broken panel, or 3–5 show a tier other than expected, the
same person rolls back. Pick the narrowest step that fixes what failed.

- **Demo telemetry only (3 or 4 failed).** In control-plane, set
  `DEMO_TELEMETRY = "off"` in `forge_demo`'s runtime config
  (`deploy/kcl/lib/reliant_web.k`) in a PR labelled `release:asap`. The next
  release stops every demo recording, whatever the bundle. (`"analytics"` is
  not a rollback: with this change it still runs the `full` tier.) Visitors who
  already loaded the full tier keep `forge.visitor_id` in their browser; it is
  sent nowhere while telemetry is off.
- **Everything (2 or 5 failed, or the above is not enough).** Open a PR that is
  `git revert` of the merged commit, labelled `release:asap`; control-plane
  takes the matching revert of its lockstep forge pin
  (`task pin:forge REF=6e4246f3… RELIANT=<the revert commit>`). The pipeline
  merges both and `rc-train` cuts the release, which restores web-runtime
  ^0.13.10 in `frontends/web` and `electron`, the demo's previous tier (no
  `grantAnalyticsConsent`), and the previous `project_gen.json` with the
  `usage` dashboard. Nothing on the server is undone: no migration, no data
  written; saved dashboards are stored by control-plane's `DashboardService`
  and untouched. A star or copy carried from `usage` to Product in the meantime
  is not carried back: check the console's starred list and re-star `usage`.

Fix forward through the normal review and release; never hand-edit
`project_gen.json` or its hash to hide a panel.
