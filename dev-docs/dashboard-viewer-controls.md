# Dashboard viewer controls

Viewer state is URL-only. Changing filters, breakdown, section/metric visibility,
release markers or alert bands does not save a dashboard version. Layout/panel
editing remains explicit; editing a layout shows all panels, including those
hidden in the viewer. Share, refresh, and browser history preserve the view.

## Optional spec metadata

```json
{
  "sections": [{ "id": "activity", "title": "Activity" }],
  "filters": [{
    "id": "rpc-service", "label": "RPC service",
    "field": "span.attr.rpc.service", "signal": "traces",
    "panelIds": ["rpc-traces"]
  }],
  "panels": [{ "id": "rpc-traces", "section": "activity" }]
}
```

`panelIds` is canonical JSON (Go `PanelIDs`, proto `panel_ids`). Empty or omitted
means all panels. A producer of a mixed activity/financial dashboard must target
process and RPC filters explicitly away from financial facts. The viewer does not
infer policy from title, prefix, SQL, units, or amount. Undeclared URL filters do
nothing. Panel IDs and section IDs stay stable across releases.

`service.name` is the deployed process, not an RPC service. Trace RPC filters
use `span.attr.rpc.service` / `span.attr.rpc.method`; RPC metrics use the supplied
`metric.attr.procedure` field (full `/package.Service/Method`), targeted to their
metric panels. Runtime panels must not receive these predicates. Generic declared
filter IDs use URL `variables`; known RPC selections have dedicated URL keys.

Breakdown offers only fields declared in dashboard filters or saved panel
`groupBy`, using their actual semantic field spellings. A filter-declared field
uses its `panelIds` targets; a field found only in saved grouping affects panels
already using it. `Saved grouping` restores the original query; `None` explicitly
clears grouping for supported aggregate panels. SQL, funnel, issues, sessions,
search and heatmap panels are not regrouped by guesswork. No client dimension is
offered unless the spec declares it.

URL keys: `service`, `rpcService`, `rpcMethod`, `rpcProcedure`, `variables`,
`breakdown`, `hiddenSections`, `hiddenPanels`, `releases`, `alerts`, plus existing
range, compare, refresh, TV and history-version keys. Hidden sections and hidden
panels are independent: re-enabling a section retains its metric selections.

## Organization Cost integration

The approved CP contract is `BillingService.GetOrganizationCostSummary({org_id})`.
Discovery uses `DashboardSource.BUILTIN = 5`, key `org-costs`, and
`DashboardViewKind.ORGANIZATION_COSTS = 1` (`summary.view_kind`, tag 13).
This is a dedicated organization view, not a telemetry panel or SQL dashboard.
It requires live explicit `billing:read`, not billing-management permission.

Generated CP bindings are synced in `490fa24e`; adapter/discovery routing and
NULL mapping are checkpointed in `7a0511ed`. The renderer is parallel-owned.
Use the typed
period/source/allowance records; distinguish actual consumption, list-equivalent,
credit grants, pending estimates and unmeasured amounts. No grand total across
periods; absent amounts are not zero. Show source coverage, limitations,
completeness and measured-through. Environment/process/RPC/time viewer controls
must not filter the canonical organization accounting request.

CP's table NULL addition is `missing_value_indexes`, tag 3: preserve a flagged
value as `null` even when its wire placeholder is zero. The existing table
formatter renders null as a dash. Update `observability/api.ts` when generated
bindings provide `missingValueIndexes`; do not hand-edit generated code.

## Verification and integration checkpoint

Worktree `dashboard-consolidation`, Reliant only. Chart agent owns
`UPlotChart.tsx`; this work does not touch that file or `PanelBody.tsx`.

Before targeted metadata changes: scoped DashboardView/Home/URL tests, frontend
typecheck, touched-file ESLint, and Playwright viewer reload/back/forward passed.
Responsive fixture screenshots: `/tmp/dashboard-controls-wide.png` (1440px),
`/tmp/dashboard-controls-narrow.png` (540px). These screenshots precede the final
metadata controls and must be refreshed after integration.

The machine filled to 100% while other work ran. After a brief recovery, the
controls gate passed: scoped 6 files / 78 tests; affected web 88 files / 1002
tests; production `npm run build` (tsc + Vite); touched-file ESLint; Playwright
reload/share/back/forward 1/1. Updated wide/narrow screenshots are at
`/tmp/dashboard-controls-final-{wide,narrow}.png`; the 540px browser overflow
probe reports no horizontal overflow. Fixture RPC/section controls were added
after these screenshots, so refresh those during integration.

Integrated verification after capacity recovered (2026-10-25): frontend
typecheck PASS; 19 scoped files / 235 tests PASS (Cost renderer/model, adapters,
NULL-versus-zero, list, empty-roster route, viewer, Home and URL state); all 38
authored TypeScript files ESLint PASS; affected web 280 files / 2685 tests PASS;
production tsc+Vite build PASS. Chromium Cost suite 6/6 PASS includes actual
selection → DashboardView → Cost renderer without deployments at 390/1440px,
plus Cost light/dark at both widths. Viewer reload/share/back/forward 1/1 PASS
includes open-menu viewport bounds at 540/1440px. This exposed and fixed viewer
menus escaping under the sidebar or past the viewport edge; floating menus now
clamp horizontally on open/resize. Latest viewer screenshots:
`/tmp/dashboard-controls-1440.png`, `/tmp/dashboard-controls-540.png`. Cost
selection screenshots are attached under `/tmp/reliant-organization-costs-playwright/`.

`task test:affected LANES=web` is the actual gate in this Taskfile checkout (the
older memory's Make target does not exist). Parent still owns cross-contract
scope pin/membership migration and CP review gates. No merge or deploy is
authorized in this implementation wave.
