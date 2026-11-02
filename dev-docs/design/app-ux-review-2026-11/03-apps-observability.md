> Copied on 2026-11-01 from `research/app-ux-review-2026-11/03-apps-observability.md` (outside any repository) so it is versioned with the synthesis.

# 03 — Apps, environments, deploys, hosting and observability

Area reviewer 3 of 5, App UX review 2026-11. Review only: no product code was changed.

- **Code reviewed:** reliant `origin/main` **0ec4b8119**, `frontends/web/src/components/{Apps,Forge}/**`.
  An earlier pass on a stale checkout (9f51649ee) was thrown away; every finding below was re-checked on 0ec4b8119.
- **Preview:** https://5318-2aab1465-f76b-4d95-b61b-480d9ba070e6.workspaces.reliantapi.com/ (port 5318). It is left running.
  - It serves the lead's prod build of 0ec4b8119 through `harness/run.sh`, with my fixtures from
    `fixtures-apps-observability/`: `05-org.js` (an org) and `10-apps.js` (two apps, `shopfront` and `ledger`, for `/apps`).
  - **One patch, in the served copy only** (`/tmp/app-ux-review/5318/dist/assets/main-BH1z9FF3.js`): `getIsDev()` returns true when the path starts with `/forge-`.
    That turns on the dev-only, fixture-backed console previews (`/forge-workloads-preview`, `/forge-observability-preview`,
    `/forge-dashboards-preview`, `/forge-metrics-preview`, `/forge-releases-preview`, `/forge-secrets-preview`,
    `/forge-pane-preview`). They mount the real console components inside the real chrome, with full data.
  - Product routes (`/apps`, `/forge/p/shopfront/...`) behave exactly as in prod.
  - Sign in first by loading `/` once. That seeds the fake Supabase session.
- **How findings were made:** screenshots deliver no pixels in this harness (bug 3f6a8b8e). Saving them with `filePath` was also refused:
  the shots directory is outside the DevTools MCP's workspace roots. So every finding is **measured, not seen**. The sources are:
  - DOM text
  - computed styles
  - WCAG contrast maths (a text-node walker in iframes)
  - overflow and target-size probes at 1440, 768 and 390 px
  - the theme token values in `src/themes/professional-themes.css`
  - the code

---

## 1. Route inventory

### Entry points
| Route | Component | Notes |
|---|---|---|
| Sidebar **Apps** → `/apps` | `components/Apps/AppsPage.tsx` | One table of apps: the app, its site, the health of each long-lived env, and its last deploy. The empty state offers **Build an app** (a chat). |
| Sidebar **Observability** → `/forge/observability` | `Forge/Org/OrgObservabilityPage.tsx` | Org-wide. App and env are filters (`apps`, `envs`). |
| `/forge`, `/forge?project=`, `/forge?forgeProject=` | `Console/ForgeResolve.tsx` | Bare `/forge` goes to `/apps`. With a param it resolves one app's console (old links, control-plane action URLs). |
| `/forge/env/$env`, `/forge/domains`, `/forge/observability?project=` | `Console/ForgeEntryRoutes.tsx` | Retired URLs, redirected. |
| Home `/` (no chats yet) | `Apps/HomeBuildApp.tsx`, `HomeNewChat.tsx` | "Build an app: describe it, Reliant builds it with forge and deploys it". |
| Mobile drawer | `Mobile/MobileNavDrawer.tsx` | Apps and Observability open the desktop console (it is exempt from the `/m` redirect) with an off-canvas nav. |

### Org console (`Forge/Org/OrgShell.tsx`)
- `/forge/observability`: org overview.
- `/forge/observability/$view`, where view is `graph`, `rpc`, `errors`, `sessions`, `logs`, `traces`, `metrics`, `profiles`, `dashboards` or `alerts`.

### App console: `/forge/p/$project` (`Console/ConsoleShell.tsx`, routes in `src/forgeConsoleRoutes.tsx`)

Nav: Overview · Releases · Observability (10 children, plus up to 3 starred dashboards) · *Configure*: Environments (one row per env, plus Previews) · Secrets · Domains · Activity.

| Path | Component | Contents |
|---|---|---|
| `/` | `Workloads/OverviewHome.tsx` | Frontends (static sites with address and domains), env strip, 4 health tiles (requests, error rate, p95, alerts firing), Needs attention, Recent releases, Recent activity, Services table, Process health, Jobs |
| `workloads/$env/$name` | `Workloads/WorkloadDetail.tsx` | Status banner. Logs, Restart, Stop and Scale actions. Tabs: Overview · Logs · Traces · Profiles · Events · History · Config. Pods, Provenance, Deploy history |
| `releases` | `Releases/ReleasesList.tsx` | DORA strip, env pointer strip, release × environment matrix, Deploys log |
| `releases/$version` | `Releases/ReleaseDetail.tsx` | Tabs Changes · Rollout · Checks · Artifacts · Health. Promotion timeline, Details. **Roll back…** and **Promote…** |
| `observability` | redirect | Goes to the Overview (retired) |
| `observability/{graph,rpc,errors,sessions,logs,traces,profiles,alerts}` | `Console/ObservabilityHost.tsx` → `Observability/ObservabilityPanel.tsx` | Service graph (`Graph/GraphView.tsx`) · RPC explorer (`Graph/RPCExplorer.tsx`) · Errors and issue detail (`Errors/*`) · Sessions and replay (`Sessions/*`) · Logs · Traces and waterfall / flamegraph (`TraceDetail.tsx`, `waterfall/`) · Profiles (speedscope iframe) · Alerts (Firing / History / Silences / Rules / Receivers) |
| `observability/metrics` | `Metrics/MetricsExplorerPage.tsx` | Catalog, metric detail, query builder, SQL tab, breakdown, exemplars, save to dashboard |
| `observability/dashboards` | `Dashboards/DashboardsList.tsx` | Starred, recently viewed, from your releases, workspace, templates |
| `observability/dashboards/$kind/$key` | `Dashboards/DashboardView.tsx` | Variables, compare, release markers, alert bands, **Edit layout**, **Add panel** (`PanelDrawer.tsx`, the panel builder), Promote to code, History, Share, Full screen |
| `environments`, `environments/$env` | `Overview/EnvironmentsPage.tsx`, `EnvPage/ForgeEnvPage.tsx` | Env tabs: Overview · Identity · Releases · Activity · Secrets · Domains. Register, Deploy, Promote, lifecycle (stop, start, destroy) |
| `secrets` | `Secrets/ManagedSecretsView.tsx` (via `EnvPage/EnvTabPanels.tsx`) | Per env. Set-secret modal with step-up, version history |
| `domains` | `Domains/ForgeDomainsPage.tsx` | Add domain dialog, DNS records, domain detail |
| `activity` | `Activity/ActivityFeed.tsx` | Audit stream, plus promotions per env |
| `monitoring` → Alerts, `settings` → `/settings/billing` | redirects (`Console/paths.ts`) | Observability usage (`UsageView.tsx`) now lives in Settings › Billing, which belongs to another reviewer |

### Dialogs and flows in this area
- Deploy (`Deploy/DeployDialog.tsx` → plan, approve, job)
- Promote (`Promote/PromoteFlow.tsx`)
- Roll back
- Set secret and step-up (`Secrets/SetSecretModal.tsx`, `StepUpDialog.tsx`)
- Add domain
- Panel builder
- Create alert from panel (as KCL)
- Save to dashboard
- **Ask Reliant** (`Observability/AskReliant/*`: "Investigate in chat" or "Fix with agent")

### Not present (asked for in the brief)
- **Managed Postgres:** no page, row or panel. See F14.
- **A frontend (RUM) dashboard as a page:** there is none. It exists only as the release dashboard `Frontend · web` (funnel, JS errors by release, issues, sessions with errors).
- **Hosting cost per app or env:** none. See F15.

---

## 2. What works (keep it)

- **Status colour is disciplined everywhere except charts.** Hue sits on a dot, icon or border, and the text stays neutral. `HostedWorkloads.tsx` documents why: hued 11 px text measured 2.8:1. Green appears only on healthy, succeeded and converged dots. Measured: Healthy has text rgb(242,242,242) and dot rgb(28,202,91). Crash-looping and failing have a dot rgb(221,60,60).
- **Text contrast passes AA** on every page probed, in pure-black dark and professional-blue light: Overview, workload, alerts, sessions, errors, logs, graph, metrics, dashboard, /apps. The only failure is the React Flow badge (F4b).
- **Cross-navigation is good:**
  - log ↔ trace, trace → logs
  - issue → trace
  - a replay timeline entry → trace and issue
  - panel → "View logs / View traces filtered to this series"
  - alert → Explore (metrics) → exemplars → trace
- **Ask Reliant exists** on issues, traces, firing alerts, rules, dashboard panels and the service graph.
- **Accessibility:**
  - Every uPlot chart is a `<figure>` with a worded caption.
  - The service graph has "Connections as a list".
  - Dashboard layout editing has keyboard buttons ("Move p95 checkout earlier/later", "Width … Change it", "Remove").
  - Tables become stacked cards below `md` (`StackedTable`, `max-md:block`).
- **Empty states explain the page** (`Forge/GettingStarted.tsx`): what the page shows, numbered steps with a copyable command, and a docs link. That is a big improvement on the old single line.

---

## 3. Findings

**Severity tags used in this section and in §5:**
- **[B]** broken
- **[A11y]** accessibility
- **[UX]** experience
- **[IA]** information architecture

### F1 [UX, H]: Empty states for a new app are CLI-only; "your agents do the work" disappears at the first step

`Forge/GettingStarted.tsx` renders a command and no action. It is used by:
- `Workloads/OverviewHome.tsx` (Nothing deployed yet)
- `Releases/ReleasesList.tsx`
- `Observability/ObsStates.tsx` (every obs view)
- `Dashboards/DashboardsList.tsx`
- `AlertsView.tsx`
- `Overview/EnvironmentsPage.tsx`
- `Frontends/FrontendList.tsx`

Measured on `/forge/p/shopfront`, `/releases` and `/observability/logs`: each says "Deploy an environment through Reliant from your checkout … `forge env deploy <env>`" and offers no button.

`/apps` does it right ("Build an app" opens a chat). Its not-live hint even says "Ask the agent in a chat on this project to deploy it" (`AppsPage.tsx:182`).

Command spelling is also inconsistent: the console says `forge env deploy <env>`, the Apps page says `reliant forge env deploy prod`.

### F2 [UX, H]: Ask Reliant is missing exactly where things break hardest

It is wired in:
- `Errors/IssueDetail.tsx` and `ErrorsView.tsx`
- `TraceDetail.tsx:136`
- `AlertsView.tsx:405,686`
- `Dashboards/DashboardView.tsx:359` and `PanelDrawer.tsx:168`
- `Graph/GraphPanel.tsx:127`

It is **absent** from:
- **A crash-looping workload** (`Workloads/WorkloadDetail.tsx`). The banner reads "Restarting repeatedly (14 times…). It ran out of memory (limit 1 GiB). Raise its memory or reduce its usage". The only actions are Logs, Restart, Stop and Scale. This is the textbook "Fix with agent" moment.
- The **Overview's "Needs attention"** rows (`OverviewHome.tsx`).
- A **failed or regressed release**: `ReleaseComparison.tsx` returns FAIL, and the Health tab in `Releases/ReleaseDetail.tsx` has no handoff.
- A **failed deploy job or migration job** (`Deploy/DeployJobPanel.tsx`, the Jobs table).
- A **session with errors** (`Sessions/SessionDetail.tsx`).

### F3 [B, H]: The second chart series and the second service in a trace are near-invisible; series 3 and 5 are painted "healthy green" and "error red"

There are three copies of one categorical palette, all built from **semantic** tokens:
- `Observability/charts/UPlotChart.tsx:117`: `CATEGORICAL = [primary, secondary, success, warning, destructive, muted]`
- `Observability/Metrics/ExplorerChart.tsx:44`: `TONES`, plus `TONE_SWATCH`
- `Observability/waterfall/TraceWaterfall.tsx:80`: `SERVICE_TOKENS = [--primary, --secondary, --success, --warning, …]`

`--secondary` is a surface tint. Measured from the scheme tokens, `--secondary` against `--card` is **1.14–1.28:1** in all 12 scheme/mode blocks (pure-black dark: secondary 14 % vs card 7 %). WCAG 1.4.11 needs 3:1 for graphical objects. So:
- In every grouped chart (by service or route, compare series), the second line vanishes.
- In every two-service trace (api + worker in the fixture), the worker bars vanish.
- The third line or service is green, which reads as "healthy". The fifth is red, which reads as "failing". That contradicts the green-only-for-success rule.
- In pure-black, `--primary` is white, so series 1 is white rather than the brand blue.

### F4 [B]: Broken items

- **(a) One malformed panel blanks the whole app.**
  - `Dashboards/drill.ts:59` reads `panel.series.length`.
  - It is reached from `DashboardView.tsx:244` (`searchHref`, built for every panel's menu).
  - Panels have no error boundary, so a panel with no `series` shows the app-wide "Something went wrong / TypeError: Cannot read properties of undefined (reading 'length')".
  - Reproduced on current main: `/forge-dashboards-preview?page=dashboard&dash=release/workloads`, forge's generated "Kubernetes workloads" dashboard from `__fixtures__/forge-dashboard-spec.json`.
  - The production API path normalises with `asArray(raw.series)` (`services/controlPlane/observability/dashboardsApi.ts:149`), so the trigger is the fixture path. The fragility is real either way: one panel should never take down the app.
- **(b) React Flow attribution badge in the Service graph:** "React Flow" at 10 px, **1.4:1** contrast. That is third-party branding inside the product (`Graph/ProcessGraph.tsx` / `GraphView.tsx`).
- **(c) Org Metrics is a stale placeholder.** `/forge/observability/metrics` (`Org/OrgObservabilityPage.tsx:120-124`) still says "A metrics explorer … lands here. Until then, metric panels live in Dashboards". The app console has the full `MetricsExplorerPage`.
- **(d) "Deployed" has two sources of truth.**
  - `/apps` (`services/forge/apps.ts`: `DeployService.ListEnvironments` + `GetStatus`) showed `shopfront` "prod: Healthy · live, staging: Healthy".
  - `/forge/p/shopfront` showed "Not deployed / Nothing deployed yet", and `environments/prod` showed "Not registered in Reliant". The console roster reads `DeployService.GetLiveView`, which my fixtures left empty.
  - Caveat: this was induced by the fixtures. But when the two disagree in production (GetLiveView empty, lagging or failing), the console asserts "nothing deployed" about an app the Apps list calls live.

### F5 [UX, M]: Full release identifiers are unreadable and repeated

- The release page H1 is `20261018.052711-950b560e58ce` (the breadcrumb already uses `shortVersion`).
- `/apps` env cells read "prod: Healthy · 20261101.141200-a1b2c3d4e5f6".
- Under **every** workload chart, a visible 12 px truncated line reads "5 releases: 20261018.030249-d1ad18266f0f, 20261018.063708-…", four times on one page (`WorkloadDetail.tsx` charts; the marker summary from `UPlotChart.tsx`).

### F6 [A11y, M]: 10 px type in a data-dense console

- `text-2xs` is 0.714rem (≈10 px) per `tailwind.config.js:87`. It is used **143×** in `components/Forge`.
- Measured under 12 px: alert severity, source and label chips (`critical`, `Your rule`, `service=api`), graph node kinds (`service`, `database`, `queue`), the metrics explorer's Group / Break down buttons, rule-query captions.

### F7 [IA, M]: The Observability nav is 10 rows deep (up to 13 with stars)

- `Console/paths.ts:72` `OBS_NAV_VIEWS`: Service graph, RPC explorer, Errors, Sessions, Logs, Traces, Metrics, Profiles, Dashboards, Alerts. `ConsoleShell.tsx` nests up to 3 starred dashboards under Dashboards.
- The whole console nav is about 20 rows.
- RPC explorer and Service graph show the same edges in two forms.

### F8 [UX, M]: The Apps list's "Healthy" means "converged", not "working"

- `/apps` shows each env's control-plane verdict (`verdictLabel`).
- An app whose prod has firing alerts or a crash-looping worker but converged pods still reads "Healthy". The workloads fixture shows 2 alerts firing while strip cells say "Healthy" or "1 failing".
- No error rate, alert count or issue count reaches the list, which is the one page that answers "is everything I shipped OK?".

### F9 [A11y, M]: Prose links are not distinguishable in pure-black

- Inline links use `text-primary underline-offset-2 hover:underline`, for example `AlertsView.tsx:230,315,322` and `ConsolePages.tsx` (ScopedEnvNote).
- In pure-black, primary is white. Measured on `/apps`: link colour rgb(242,242,242), the same as body text, with `text-decoration: none`.
- That fails WCAG 1.4.1. The brand-token owner fixes the colour. The area-specific fix is a resting underline.

### F10 [UX, M]: Alerts don't say which release they followed

- A firing alert card (`AlertsView.tsx` ~335-420) offers Ask Reliant, Explore, Rule and Silence.
- The release markers already exist (`releaseMarkers.ts`, used on every chart), but the card never says "started 8 min after 20261018.155157 was promoted to prod" with a link to that release's Health compare.
- So alert → release → fix is three hops through other pages.

### F11 [A11y, M]: Phone

- The real console routes at 390 px have **no horizontal overflow** (5 routes measured), and the hamburger nav is present.
- **17 of 26** controls on the Logs page are shorter than 32 px (the env, time and refresh controls in `ObsScopeBar.tsx`). Touch targets should be 44 px.
- Data-filled pages could not be measured on a phone: the dev previews redirect to `/m/chats` below ~800 px. Code review instead:
  - `StackedTable` turns tables into cards.
  - The waterfall has a narrow layout (`COLUMNS_NARROW`).
  - Dashboards drop to 1 column.

### F12 [UX, M]: Environment page copy contradicts itself and repeats

- `/forge/p/<app>/environments/prod` says "Register it from your checkout — after that it shows here with your daemon offline". The same page has a **Register** button (`Preview/RegisterEnvPanel.tsx`).
- "Checking your daemon…" appears twice, under Promote and under Deploy (`EnvPage/EnvPageHeader.tsx`).
- The dev env's runtime-checks copy (`Status/DevStackPanel.tsx`) is engineer-voiced:
  - "Incomplete — not a clean bill of health … This is not a clean bill of health."
  - "A check that could not obtain its facts reports could not measure — never a pass and never a skip. Three outcomes, not two."
  - That last line sits above five counters.

### F13 [UX, L]: Smaller issues

- **(a)** The metrics catalog shows raw UCUM units: `By`, `{order}`, `{call}`, `{goroutine}` (`Metrics/Catalog.tsx`).
- **(b)** A ¼-width table panel scrolls sideways inside its card. "Slowest routes" is a 400 px table in a 362 px panel (`Dashboards/PanelBody.tsx`).
- **(c)** Alert affordances are duplicated:
  - the nav badge
  - the scope-bar bell ("Alert notifications, 2 unread", `AlertNotifications.tsx`)
  - the Overview "Alerts firing" tile
  - Needs attention

  The bell's 10 px red count badge sits inside a 32 px button.
- **(d)** Funnel steps read as one run of digits in the a11y tree: "84646% of previous" is 846 + "46% of previous" (`PanelBody.tsx` funnel). A separator or `aria-label` per step fixes it.
- **(e)** "last value 1.42" on an alert whose summary says "1.42s": the unit is dropped (`AlertsView.tsx`).

### F14 [UX, H for "Run it"]: Managed Postgres has no surface

- The deploy proto has `DEPLOY_TIER_DATABASE`.
- In the UI a database appears only as a service-graph node (`Graph/ProcessGraph.tsx`) and as a destroy warning ("Database data is retained, but it is orphaned", `EnvPage/EnvLifecycleControls.tsx:220`).
- There is no database row on the Overview and nothing on size, connections, slow queries, backups or restore, or how to connect.
- A worktree `feat/dashboards-postgres-panels` is in flight. Check it against this list.

### F15 [UX, M]: Hosting and observability cost is invisible where the user runs things

- Observability usage moved to Settings › Billing (`paths.ts` `ORG_USAGE_PATH`).
- The console, the Apps list and the env pages show no per-app or per-env compute or storage figure.
- The only cost signal in the area is a deploy **queued on billing** (`ConsoleShell.tsx` `QueuedMark`).

### Static sites

These are covered well by `Frontends/FrontendList.tsx` on the Overview and the env page:
- the live address first
- the generated address muted beside a custom one
- each domain's state ("Waiting for DNS")
- Manage domains

No issue found beyond F5 (version strings).

---

## 4. Colour use vs the landing

- **Blue for the active state:** the active nav row is a white 8 % tint (`oklab(0.96 … / 0.08)`) with no blue. The Apps page primary button is rgb(36,36,36). The global shell and tokens belong to the brand-token owner, so these are not repeated here.
- **Area-specific:** the chart palette (F3) is the one place this area invents its own colours, and it does it from semantic tokens. That is where blue (#0d8cff) and violet (#6e39ff) should enter the console, as `--chart-1` and `--chart-2`.

---

## 5. Ranked recommendations

| # | Change | Impact | Effort | Files | Concrete change |
|---|---|---|---|---|---|
| 1 | Dedicated chart palette | H | S | `src/index.css`, `src/themes/professional-themes.css`, `Observability/charts/UPlotChart.tsx:117`, `Observability/Metrics/ExplorerChart.tsx:44`, `Observability/waterfall/TraceWaterfall.tsx:80`, `Settings/__tests__/colorSchemeTokenContract.test.ts` | Add `--chart-1…6` per scheme and mode. 1 = brand blue #0d8cff, 2 = violet #6e39ff, then cyan, amber-neutral, pink and slate at matched L/C, with **no green and no red**. Replace the three semantic palettes with one exported `CHART_TONES`. Keep `destructive` only for error spans and alert bands. Extend the contract test to assert each `--chart-n` ≥ 3:1 against `--card`. Agree the values with the brand-token owner. |
| 2 | "Deploy with an agent" in every empty state | H | S–M | `Forge/GettingStarted.tsx`, `Workloads/OverviewHome.tsx`, `Releases/ReleasesList.tsx`, `Observability/ObsStates.tsx`, `Dashboards/DashboardsList.tsx`, `AlertsView.tsx`, `Apps/startBuilding.ts`, `Observability/AskReliant/startAskReliantChat.ts` | Give `GettingStarted` an optional primary `action`. Pass "Deploy with an agent" (a sent chat: "deploy &lt;app> to &lt;env> with reliant forge"). Keep the command as the secondary path. Spell it `reliant forge env deploy <env>` everywhere (it matches `AppsPage.tsx:182`). |
| 3 | Ask Reliant where things break | H | M | `Workloads/WorkloadDetail.tsx` (status banner), `Workloads/OverviewHome.tsx` (Needs attention rows), `Observability/ReleaseComparison.tsx` + `Releases/ReleaseDetail.tsx` (FAIL verdict), `Deploy/DeployJobPanel.tsx` (failed job), `Sessions/SessionDetail.tsx`, `AskReliant/askReliantPrompt.ts` | Add subject kinds `workload`, `release`, `deploy`, `job` and `session` to `askReliantPrompt.ts`. Render `AskReliantActions` (Investigate / Fix with agent) beside the existing actions. On the crash-loop banner make **Fix with agent** the primary button. |
| 4 | Panels cannot crash the app | H | S | `Dashboards/DashboardView.tsx` (`DashboardPanelCard` map), `Dashboards/drill.ts:58-59` | Wrap each panel in an error boundary that renders "This panel couldn't render · View spec · Remove". Guard `(panel.series ?? [])` in `countsErrors` and `drillSearches`. Add a fixture test using `forge-dashboard-spec.json` raw. |
| 5 | Alert ↔ release correlation | M | M | `Observability/AlertsView.tsx` (firing card, ~L335), `Observability/releaseMarkers.ts` | Find the newest promotion in the alert's env before `firingAtMs`. Show "8 min after &lt;shortVersion> · Compare" linking to the release's Health tab, with the compare param prefilled. |
| 6 | Short release identifiers | M | S | `Releases/ReleaseDetail.tsx` (H1), `Apps/AppsPage.tsx` (env cell), `Workloads/WorkloadDetail.tsx` + `charts/UPlotChart.tsx` (marker line) | Show `shortVersion` plus the commit subject. Put the full id in `title` and a copy button. Replace the visible "5 releases: &lt;ids>" line with "5 releases marked" (ids stay in the figure caption). |
| 7 | 12 px type floor | M | S | All `text-2xs` / `text-3xs` in `components/Forge/**` (143 uses), `tailwind.config.js:71-87` comment | Use `text-xs` as the minimum for chips, labels and captions. Reserve `2xs` for the nav count badge only. |
| 8 | Group the Observability nav | M | M | `Console/ConsoleShell.tsx` (`buildConsoleNav`), `Org/OrgShell.tsx`, `Console/paths.ts:72` | Use three subsections: **Explore** (Logs, Traces, Metrics, Profiles), **Frontend** (Sessions, Errors), **Topology** (Service graph with RPC explorer as a tab). Keep Dashboards and Alerts top-level. Cap starred dashboards at 3 (already). |
| 9 | Runtime health on the Apps list | M | M | `Apps/AppsPage.tsx`, `services/forge/apps.ts`, `hooks/apps-queries.ts` | Add an "Attention" column: firing alerts plus open issues for prod, from the observability API the console already uses. Show "Healthy" only when converged **and** nothing is firing. Otherwise show the worse state with its count. |
| 10 | Managed Postgres surface | H | L | `Workloads/OverviewHome.tsx` (a "Databases" section like Frontends), new `Workloads/DatabaseDetail.tsx`, `services/forge/cloudEnvs.ts` (`tier: database`) | One row per database: engine and version, size against plan, connections, last backup, the connection-info command (`reliant forge db query …`), and Ask Reliant. Link the db panels from `feat/dashboards-postgres-panels`. |
| 11 | Cost where you run things | M | M | `Apps/AppsPage.tsx`, `EnvPage/EnvPageHeader.tsx`, `Observability/UsageView.tsx` | A "This month" figure per app (compute, database, static, telemetry), linking to Settings › Billing for the breakdown. |
| 12 | Resting underline on prose links | M | S | `AlertsView.tsx:230,315,322`, `ConsolePages.tsx` (ScopedEnvNote), every `hover:underline` inline link in `components/Forge` | Use `underline decoration-ink/30 hover:decoration-ink` for links inside sentences. Nav and table-cell links can stay as they are. |
| 13 | One "deployed" truth | M | M | `Console/ConsoleLayout.tsx` (roster), `Workloads/OverviewHome.tsx`, `services/forge/apps.ts` | When `ListEnvironments` has envs but the live view is empty or failing, say "prod is registered; waiting for its first observation" (or "couldn't read the live view · Retry"), never "Nothing deployed". |
| 14 | Env page copy | M | S | `Preview/RegisterEnvPanel.tsx`, `EnvPage/EnvPageHeader.tsx`, `Status/DevStackPanel.tsx` | Replace "Register it from your checkout" with "Register to track its releases and secrets" (the button is right there). Show "Checking your daemon…" once in the header. Cut the runtime-checks explainer to one line: "1 check couldn't measure, so dev's health is partly unknown." |
| 15 | Phone touch targets | M | S | `Observability/ObsScopeBar.tsx`, `forge-ui` select / button sizes under `max-md` | `h-11` controls below `md`. Move the bell into the nav badge on phones. |
| 16 | Org Metrics → the explorer | L | S | `Org/OrgObservabilityPage.tsx:120-124` | Mount `MetricsExplorerPage` with the org place, as the app console does (`ConsolePages.tsx:156`). |
| 17 | Polish | L | S | `Metrics/Catalog.tsx` (units), `Graph/ProcessGraph.tsx` (attribution), `Dashboards/PanelBody.tsx` (¼-width tables, funnel labels), `AlertsView.tsx` (last-value unit) | Humanise UCUM units (By → bytes, `{order}` → orders). Restyle or (if the licence allows) hide the React Flow badge. Let table panels default to ½ width. Add an `aria-label` per funnel step. Format last value with the rule's unit. |

---

## 6. Harness notes (for the lead)

- **Dev-only previews:** `/forge-*-preview` routes need `getIsDev()`. A prod build compiles `import.meta.env.DEV` to false, and `vite build --mode development` did not change that. I patched the served copy only (see the header).
- **`/forge/p/<app>` stays "Not deployed" without more fixtures.** The console needs `DeployService/GetLiveView` and the observability RPCs. `OrganizationService/ListMyOrganizations` stays in `__fx.unknown` even with a fixture registered (`05-org.js`). The codec may lack its schema.
- **Previews below ~800 px:** they redirect to `/m/chats`, so data-filled phone layouts can't be measured through them.
- **Screenshots:** `take_screenshot` with `filePath` under `research/…/shots/` is refused ("not within any of the configured workspace roots").
- **`evaluate_script` with `sourcePath`:** it sometimes returned results from a previous file of the same name, and briefly reported ENOENT for a file just written. Copy to a fresh filename per run.
