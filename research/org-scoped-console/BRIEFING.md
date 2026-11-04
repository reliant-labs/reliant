# Org-scoped Apps & Observability — research briefing

Goal: sidebar "Apps" → `/forge` = index of every forge app the org has deployed;
"Observability" → `/forge/observability` = org-wide telemetry with app/env as
filters. Per-app console `/forge/p/$project/...` stays as the drill-in.
Paths below are relative to `reliant/frontends/web/src/` unless noted.

## Executive summary

- **Backend is ready.** Obs `QueryScope.project_id` optional; deploy
  `ListDeployEnvironmentsRequest.project` is `optional` (control-plane
  `proto/services/deploy/v1/deploy.proto:836-853`: "Unset lists every project's
  environments; set to "" lists only the project-less ones"). Dashboards
  `ListDashboardsRequest.project_id` optional (`proto/services/dashboard/v1/dashboard.proto:331`).
  `GetLiveView` is per-project only (`deploy.proto:658-669`, request `:1726`, `project` is a plain string) — no org-wide Live.
- **No frontend hook lists the org's forge apps.** The only env-list client is
  `listProjectEnvironments(project)` (`services/forge/cloudEnvs.ts:313`), which
  ALWAYS sends `project` and re-filters client-side on `env.project === project`.
  Needed: a new `listOrgEnvironments()` (call `listEnvironments({})`, no project)
  + `useOrgForgeApps()` grouping by `env.project`.
- **Observability panel already tolerates no project** at the query layer
  (`wireScope` drops a falsy projectId). What needs work is the `ObsPlace` type
  (only `env` | `project`), the env picker (names are only unique within a
  project), and the console-context-bound pages (dashboards, ObservabilityHost).

## 1. Where project flows into observability

`ObsPlace` (`components/Forge/Observability/obsPlace.ts:34-47`):
`{kind:"env", env, onWiden?}` | `{kind:"project", project, environments}`.
Helpers `placeEnvironments` (:30) and `envIdsOf` (:50) — `envIdsOf` maps picked
env NAMES → ids via `place.environments`; **across apps names collide (every app
has `prod`)**, so an org place must key the picker by id or `project/env`.

`ObservabilityPanel.tsx`:
- :111 `projectId = place.kind === "project" ? place.project : undefined` → `ObsScope.projectId` (:115).
- :119 `subject = place.kind==="env" ? place.env.name : place.project` (header text).
- :124 `environments = placeEnvironments(place)` → `useReleaseMarkers(scope, range, environments, marked)` (:127).
- :158 `envName(id)` for AlertNotifications.
- :244 `ObsScopeBar environments={place.kind==="project" && !consoleChrome ? place.environments : undefined}` → `ObsScopeBar.tsx:77` `EnvPicker` (by name).

Scope → wire: `services/controlPlane/observability/api.ts:137-151` `wireScope`
sends `projectId: s.projectId || undefined` — **undefined already = whole org**.
Query keys (`hooks/observability-queries.ts:51, 454, 533, 618`) include
`projectId ?? ""` — fine.

Derived scopes copying projectId (all pass undefined through; OK org-wide):
- `Observability/deliveryScope.ts:54` (releases/DORA/release health)
- `Observability/Errors/ErrorsView.tsx:81`, `Errors/IssueDetail.tsx:102`
- `Observability/ReleaseComparison.tsx:124` (scoped to `target.envId`; inherently one env → one app)
- `Observability/Home/ObservabilityHome.tsx:84` (`projectId: project`)
- `Observability/Dashboards/DashboardsList.tsx:84`, `DashboardView.tsx:133,183,192`

Alerts: `useAlerts(orgId, envId?)` / `useAlertHistory(orgId, range, envId?)`
(`observability-queries.ts:369,381`) are **org-keyed already, no project** —
`AlertsView.tsx:91-92` filters client-side by `envIds` (`inView`). Works org-wide
but `place.environments` must hold every org env for `envName`/declared rules.
`AlertNotifications` is org-wide already.

Usage view: org allowance, no scope (comment at `ObservabilityPanel.tsx:239`).

**Inherently per-project (keep in drill-in, or require an app filter):**
- Dashboards: `Dashboards/DashboardsPages.tsx:129,151,173-199` read `forgeProject`
  from `useForgeConsole()` (throws outside ConsoleLayout, `consoleContext.ts:45-48`).
  `useDashboardIndex(orgId, projectId: string, envId)` (`hooks/dashboard-queries.ts:38`)
  → `services/controlPlane/observability/dashboards*.ts:175` sends `projectId` as-is;
  **a `""` would mean "project-less only"**, not org — pass undefined if org-wide
  listing is wanted. `DashboardRef.projectId` (`dashboardModel.ts:90`), save/copy
  need a project (`DashboardsList.tsx:78-81`). Release dashboards are per release → per app.
- Release comparison / release health (per promotion, per env).
- Workload pages, releases, secrets, domains, environments (deploy objects).
- `ObservabilityHost.tsx` (console page host) depends on `useForgeConsole()`
  (`forgeProject, env, roster, page`, :55) and builds env list from `roster.envs[].live` (:59-64) — not reusable as-is for org.

## 2. Environments & apps listing

- `useForgeRoster(projectId, forgeProject?)` (`hooks/forge-queries.ts:546`):
  joins `useLiveView(forgeProject)` (:329, → `services/forge/live.ts:960`
  `getLiveView(project)`) with the daemon topology. Per project.
- `useCloudEnvironments(forgeProject)` (`forge-queries.ts:279`) →
  `listProjectEnvironments` (`services/forge/cloudEnvs.ts:313`) — per project,
  client-side filter by design (guards old control planes).
- **Nothing lists the org's forge projects.** Options for the apps index:
  (a) `DeployService.ListEnvironments` with `project` unset, group by
  `DeployEnvironment.project` (one RPC; recommended; mapper `toCloudEnv` already
  carries `project`); (b) then optionally `GetLiveView` per app for rich cards
  (N RPCs) or `GetStatus` per env (`useCloudEnvStatuses`, :414).
  Project-less envs (`project === ""`) need a bucket/decision.
  Env ids are what obs needs (`ObsEnvironment.id`); `declared` shape only comes
  from GetLiveView (`ObservabilityHost.tsx:61`) — org-wide Alerts "declared
  rules" would need per-app GetLiveView or degrade to null.
- `ForgeProjectPicker.tsx` lists **Reliant projects** from `useProjectStore`
  (not forge apps) and calls `selectProject` — current-project fallback only.
  `ConsoleProjectSwitcher.tsx:84-104` also lists Reliant projects, showing
  `forge_project_name`; switching uses `ConsoleLayout.switchTarget`
  (`ConsoleLayout.tsx:64-81`), which falls back to `/forge?project=<id>` when the
  project has no forge name. Mapping app→Reliant project: `projectDeclaring`
  (`ConsoleLayout.tsx:53`, by `forge_project_name`). Apps an admin never opened
  have no Reliant project — console already handles `projectId: null`
  (`consoleContext.ts` header).

## 3. Console chrome reuse

- `ConsoleShell.tsx`: pure chrome (no fetching). `buildConsoleNav({project, page, env, envs, alertsFiring, starred})` (:85) — all rows are project pages via `consolePath(project, …)`; `consoleCrumbs(project, page, …)` (:167); breadcrumb root is `nav.project` linking to project overview (:258-266); `EnvScopeControl` single-env scope; `navLabel={`${project} console`}`. **Everything assumes a project**; reuse = `SidebarLayout` (`components/forge-ui/sidebar_layout`) + `EnvScopeControl` pattern, with a new org nav builder (Apps · Observability › views · Alerts…) or generalize `ConsoleShellProps` to take `items/crumbs/root`.
- `ConsoleProjectSwitcher` (brand row): reusable as an "app switcher" if re-sourced from the org app list instead of Reliant projects; would need an "All apps" entry.
- `consoleContext.ts`: `ForgeConsole {forgeProject, projectId, page, env, setEnv, roster, href}` — project-only. An org layout needs its own context (e.g. `{apps, envsByApp, scope:{apps[], envIds[]}}`) rather than overloading this one, since `useForgeConsole` throwing is relied on.
- `envScope.ts` / `rememberedEnv(forgeProject)`: env scope remembered per project; org scope needs its own key.
- `paths.ts`: `FORGE_ROOT` (:45), `consoleBase/consolePath` (:84-89), `consolePageOf(pathname)` (:136; returns null for `/forge`, pinned by `paths.test.ts:72`), `pageOfObsView`/`obsViewOfPage` (:197-210), `carriedObsSearch` (:235), `OBS_NAV_VIEWS` (:48). An org path builder (`orgObsPath(view)`) should live beside these.

## 4. `/forge` and `/forge/observability` today and who links there

Routes (`forgeConsoleRoutes.tsx`): `/forge` (:91, `forgeResolveSearchSchema`, `components.landing` = `ForgeLandingRoute`), `/forge/observability` (:103, `forgeObservabilitySearchSchema`, `components.legacyObservability`), plus `/forge/env/$env`, `/forge/domains`, legacy commands, and `/forge/p/$project` tree (:124+). Mounted in `routes.tsx:660-690` via `createForgeConsoleRoutes`.

`ForgeEntryRoutes.tsx`: `ForgeLandingRoute` (:39) → `ForgeResolve` → `consolePath(name,{overview})`; `LegacyObservabilityRoute` (:50) → `legacyObservabilityTarget(name, search)` (`paths.ts:300-306`, maps `view`/`envs`/`workload` to the project's obs page, `env=envScopeOfEnvs(envs)`). `ForgeResolve.tsx` resolves the current Reliant project's forge name (`?project=`/`?forgeProject=` override).

`forgeObservabilitySearchSchema` (`routeSchemas.ts:623-627`): `{project?, forgeProject?, ...legacyObsShape}`. If `/forge/observability` becomes the org page, the schema becomes the org page's (view, envs, f, groupBy, range, q, … + new `apps`), and **links carrying `?forgeProject=`** should keep redirecting to the project drill-in (back-compat for old bookmarks is not required per repo rules, but `?forgeProject=` is a cheap "app filter" mapping).

Linkers:
- Sidebar `components/Layout/Sidebar.tsx` ~1585-1605; `ModernApp.tsx:1751,1754,1790,1793` (`navigate({to:'/forge'})` / `'/forge/observability'`).
- `components/Mobile/MobileNavDrawer.tsx:68-69`.
- `components/Settings/cloud/billing.tsx:433` "Back to environment" → `/forge` (would now land on apps index — likely wants to keep project resolution or go to the app).
- `components/Forge/Console/ConsoleLayout.tsx:67` `switchTarget` → `/forge?project=<id>` for a project without forge name — relies on `/forge` resolving the current project. Must change (e.g. keep a resolve route or show "not a forge app").
- Path predicates that treat `/forge` as forge-owned (no change needed, but verify): `store/projectStore.ts:81-82`, `lib/routeParent.ts:59`, `lib/mobileRedirect.ts:78`, `components/Forge/QueuedDeployWatch.tsx:25`.
- control-plane: alert deep links go straight to `/forge/p/<project>/…` (`internal/alerting/release_regressed.go:291`, `issues.go:334`) — unaffected. Deploy holds link `{app_url}/forge/env/{env}?forgeProject={project}` (`internal/handlers/deploy/holds.go:140-156`) — legacy env route, unaffected unless removed.
- No `/forge` link found in onboarding or `reliant/e2e` Go stories.

## 5. Tests to update

- `components/Forge/Console/__tests__/consoleRouting.test.tsx` — :233-252 (`/forge` resolves to project, `?project=`), :281 (`/forge/observability?view=logs&envs=["e2e"]&q=timeout` → project page).
- `components/Forge/Console/__tests__/paths.test.ts` (:72 `consolePageOf("/forge")` null; legacy target cases).
- `components/Forge/Console/__tests__/consoleNav.test.ts` (if nav/crumbs generalized).
- `components/Layout/Sidebar.forge-nav.test.tsx`.
- `components/Mobile/__tests__/MobileNavDrawer.test.tsx:93-94` (hrefs stay the same if paths unchanged).
- `components/Settings/cloud/__tests__/billing.checkout.test.tsx:189`.
- `store/__tests__/projectStore.syncProjectUrl.test.tsx:116,239`, `lib/__tests__/routeParent.test.ts:59`, `lib/__tests__/mobileRedirect.test.ts:106` (path predicates; likely unchanged).
- `components/Forge/Observability/__tests__/*` (Delivery.test.tsx etc.) for an org `ObsPlace` kind.
- Playwright: `frontends/web/e2e/mobile.spec.ts:44-45,67`, `mobile-audit.spec.ts:26-27`.

## 6. Prior decisions in repo

- `research/console-v3/PROPOSAL.md` / `DECISIONS.md` are **referenced in code but not present in this checkout** (`git ls-files` finds none). Decisions quoted in comments:
  - "Environment is a filter, not a place (owner decision 1)" (`obsPlace.ts:5`).
  - D1: environment is the top bar's scope; nav is product nouns, not env roster (`ConsoleShell.tsx:9-25`).
  - D9: Observability views hosted as console pages; one env control on screen (`ObservabilityHost.tsx:3-19`).
  - PROPOSAL §3.1: project switcher is "the one scope that changes the nav" (`ConsoleProjectSwitcher.tsx:4`); §3.2: console addressed by FORGE project name (`routes.tsx:660`).
  - `GetDeployLiveViewRequest` comment: "Live is per project, because that is the unit a human thinks in: 'my project's environments', not 'every environment my org has'" (`deploy.proto:1727-1729`) — the new org surface partly reverses this framing for Apps/Obs; worth naming to the owner.
- `dev-docs/observability.md` exists (not scoping-specific; check before writing docs).

## Open decisions for the owner

1. Org env picker identity: `app/env` pairs vs env ids; and whether app filter is multi-select.
2. Dashboards on the org page: list org-wide (project unset) read-only with drill-in, or hide until an app is chosen.
3. Project-less environments (`project=""`): show as "Unassigned" app or hide.
4. What `/forge?project=<id>` (from `switchTarget`, billing back button) should do now.

## Suggested implementation slices (disjoint files)

1. **Data layer** — `services/forge/cloudEnvs.ts` (add `listOrgEnvironments()` calling `listEnvironments({})`), `hooks/forge-queries.ts` (add `useOrgForgeApps()` → `{apps: {project, envs: CloudEnv[]}[] , availability}`; key `forgeKeys.orgEnvs()`), tests beside them.
2. **ObsPlace org kind** — `Observability/obsPlace.ts` (add `{kind:"org", apps: {project, environments}[], projects?: string[]}`; `envIdsOf` by id), `ObservabilityPanel.tsx` (projectId only when one app filtered; subject "All apps"), `ObsScopeBar.tsx` (app + env picker, env labeled `app/env`), `AlertsView.tsx` `inView` (:91), observability `__tests__`.
3. **Org routes & paths** — `components/Forge/Console/paths.ts` (org path builders), `routeSchemas.ts` (org obs schema + `apps`), `forgeConsoleRoutes.tsx` (`/forge` → apps index component, `/forge/observability` (+ `/forge/observability/$view`?) → org obs), `ForgeEntryRoutes.tsx` (drop `ForgeLandingRoute`/`LegacyObservabilityRoute` or keep `?forgeProject=` redirect), `ForgeResolve.tsx` if no longer used by `/forge`, `consoleRouting.test.tsx`, `paths.test.ts`.
4. **Org layout & pages** — new `components/Forge/Org/` (`OrgConsoleLayout.tsx`, `orgContext.ts`, `AppsIndex.tsx`, `OrgObservabilityHost.tsx`), reusing `SidebarLayout`, `EnvScopeControl`, and optionally a generalized `ConsoleShell` (if generalized, `ConsoleShell.tsx` + `consoleNav.test.ts` belong to this slice).
5. **Entry points** — `ConsoleLayout.tsx` `switchTarget` (:67) and `ConsoleProjectSwitcher.tsx` ("All apps" link back to `/forge`), `Settings/cloud/billing.tsx:433` + test, `Sidebar.tsx`/`ModernApp.tsx` only if paths change, Playwright `e2e/mobile*.spec.ts`.

---

# OWNER DECISIONS (settled — do not re-litigate)

- **Amended 2026-10-26 (owner, option A), after #1626 landed `/apps`:** the
  org Apps index is **`/apps`** (#1626, `components/Apps`); bare `/forge`
  redirects there. This change does NOT ship its own Apps index: the
  `AppsIndex` / `OrgForgeApp` / `groupOrgApps` / `useOrgForgeApps` contract
  below (section A, slice C) was dropped. Everything that listed apps reads
  #1626's query instead (`hooks/apps-queries.ts`: `useApps` for the app
  switcher — "All apps" → `/apps` — and the org nav's nested apps, whose Apps
  entry links `/apps`; `useOrgEnvironments` for the org Observability place).
  #1626's org read drops project-less environments, so the **Unassigned bucket
  is gone from the env/app filters**; it remains only for dashboards that belong
  to no app. The decisions below that name `/forge` as the Apps index, or the
  Unassigned env bucket, are superseded by this one.
- **Org home, app drill-in.** `/forge` = Apps index: every forge app (forge
  project) the org has deployed, with its environments. `/forge/observability`
  (+ `/forge/observability/$view`) = org-wide observability, app and
  environment are FILTERS. `/forge/p/$project/...` stays exactly as is — the
  per-app drill-in (releases, secrets, domains, workloads, per-app obs).
- The per-app console's project switcher lists the org's DEPLOYED apps
  (from the control plane), not the user's local Reliant projects, plus an
  "All apps" entry back to `/forge`.
- Org env picker shows `app/env` labels (every app has a `prod`); app filter is
  multi-select.
- Dashboards on the org page are listed org-wide (project unset); opening one,
  saving or copying one goes to its app's console.
- Environments with no project (`project === ""`) are grouped as **"Unassigned"**.
- `/forge?project=<id>` and `/forge?forgeProject=<name>` still resolve to that
  app's console (ForgeResolve) — billing's back button and old links keep working.
  `/forge/observability?project=…|forgeProject=…` likewise redirects to that
  app's obs page (legacyObservabilityTarget). Bare `/forge` and bare
  `/forge/observability` are now the ORG pages (no project resolution).
- No backwards compat needed beyond the above (repo is pre-launch); delete dead
  code paths rather than keeping them.

# SHARED CONTRACT (every slice codes against these exact names)

Paths relative to `reliant/frontends/web/src/`. Work happens in the worktree
`/home/workspace/.reliant/worktrees/reliant-labs/org-console-d5af9fe2/reliant`.

## A. Data — `services/forge/cloudEnvs.ts`, `hooks/forge-queries.ts`
```ts
// cloudEnvs.ts
export interface OrgForgeApp {
  /** forge project name; "" = the Unassigned bucket (project-less envs). */
  project: string;
  /** sorted by name */
  envs: CloudEnv[];
}
/** ListEnvironments with `project` UNSET (= every env of the org). Drops rows with empty id/name. */
export async function listOrgEnvironments(): Promise<CloudEnv[]>;
/** Pure: group by env.project, apps sorted by name, "" (Unassigned) LAST. */
export function groupOrgApps(envs: readonly CloudEnv[]): OrgForgeApp[];

// forge-queries.ts
forgeKeys.orgEnvs()   // query key
export function useOrgForgeApps(): UseQueryResult<{
  availability: CloudAvailability; apps: OrgForgeApp[]; envs: CloudEnv[]; detail: string;
}>;  // same failure→availability pattern as useCloudEnvironments; enabled iff hasCloudControlPlane()
```
Every write path that invalidates `forgeKeys.cloudEnvs(...)` also invalidates `forgeKeys.orgEnvs()`.

## B. ObsPlace org kind — `components/Forge/Observability/obsPlace.ts`, `obsSearch.ts`
```ts
export interface ObsEnvironment { id: string; name: string; project?: string; declared?: ObsDeclared | null }
export type ObsPlace =
  | { kind: "env"; ... }            // unchanged
  | { kind: "project"; ... }        // unchanged
  | { kind: "org";
      /** every org env; name = orgEnvName(project, env) */
      environments: ObsEnvironment[];
      /** every app name, "" = Unassigned, in display order */
      apps: string[] };
/** "shop/prod"; a project-less env is its bare name. */
export function orgEnvName(project: string, env: string): string;
/** Display label for an app name: "" → "Unassigned". */
export function appLabel(project: string): string;
/** OrgForgeApp[] → ObsEnvironment[] for an org place (declared: null). */
export function orgEnvironments(apps: readonly OrgForgeApp[]): ObsEnvironment[];
/** env ids in scope. org: picked env names (∩ selected apps), else every env of the
 *  selected apps, else [] (= whole org). */
export function envIdsOf(place: ObsPlace, picked: readonly string[] | undefined, apps?: readonly string[]): string[];
// obsSearch.ts: ObsSearch gains `apps?: string[]` (selected app names), included in OBS_SEARCH_KEYS.
```
- Org URL state: `envs` = array of `orgEnvName`s, `apps` = array of app names.
- ObservabilityPanel: for org, `projectId` = the one selected app when exactly one
  non-"" app is selected, else undefined. When apps are selected but resolve to
  ZERO env ids, show an empty state ("no environments recorded for …") — NEVER
  fall through to an org-wide query (empty envIds means "all").
- ObsScopeBar draws an App multi-picker + env picker (envs narrowed to selected
  apps, labelled `app/env`) for an org place in BOTH chrome modes ("console"
  chrome included, since the org layout has no other env control).
- Header subject for org: "All apps", or the selected app label(s).

## C. Paths & routes — `components/Forge/Console/paths.ts`, `routeSchemas.ts`, `forgeConsoleRoutes.tsx`, `ForgeEntryRoutes.tsx`, `routes.tsx`
```ts
// paths.ts
export type OrgPage = { kind: "apps" } | { kind: "observability" } | { kind: "obs-view"; view: ObsNavView };
export function orgPath(page: OrgPage): string;   // "/forge" | "/forge/observability" | "/forge/observability/<view>"
export function orgPageOf(pathname: string): OrgPage | null;  // inverse; null outside the org pages
/** The org keys of a search to carry between org pages: carriedObsSearch + envs + apps. */
export function carriedOrgSearch(search: Record<string, unknown>): Record<string, unknown>;
```
Route tree under `_forge`:
```
_forge (ForgeLayout)
  _forgeOrg  (pathless layout, component: OrgConsoleLayout from components/Forge/Org)
    /forge                       validateSearch forgeResolveSearchSchema; component ForgeLandingRoute
    /forge/observability         validateSearch forgeOrgObsSearchSchema; component OrgObservabilityRoute
    /forge/observability/$view   same schema; beforeLoad: not isObsNavView → redirect to /forge/observability (keep search)
  /forge/env/$env, /forge/domains, legacy commands   (unchanged)
  /forge/p/$project/...                              (unchanged)
```
- `ForgeLandingRoute` (ForgeEntryRoutes.tsx): `project`/`forgeProject` set →
  `<ForgeResolve … target=overview/>`; else `<AppsIndex/>`.
- `OrgObservabilityRoute` (ForgeEntryRoutes.tsx): `project`/`forgeProject` set →
  ForgeResolve with `legacyObservabilityTarget`; `view` set (old `?view=logs`
  links, only on bare /forge/observability) → replace-redirect to
  `orgPath(pageOfObsView-equivalent)` keeping the rest; else `<OrgObservabilityPage/>`.
- `forgeOrgObsSearchSchema` = obsSearchShape + `apps` + `envs` + legacy `project`,
  `forgeProject`, `view` (incl. "workloads"), `workload`.
- `ForgeConsoleComponents` replaces `legacyObservability` with `orgLayout`,
  `orgObservability` (used for both org obs routes).

## D. Org layout & pages — NEW `components/Forge/Org/` (+ Dashboards org-wide)
Exports from `components/Forge/Org/index.ts`:
```ts
export { OrgConsoleLayout } from "./OrgConsoleLayout";   // route component; renders <Outlet/>
export { AppsIndex } from "./AppsIndex";                 // no props
export { OrgObservabilityPage } from "./OrgObservabilityPage"; // no props; view from orgPageOf(pathname)
```
- `OrgConsoleLayout` renders the org chrome (forge-ui `SidebarLayout`, like
  ConsoleShell) around `<Outlet/>`, EXCEPT when the URL carries `project` or
  `forgeProject` search (the resolve-and-redirect case) → bare `<Outlet/>`.
- Org nav (pure `buildOrgNav`, tested): Apps (with the org's apps nested,
  each → `consolePath(app,{kind:"overview"})`), Observability › errors · logs
  · traces · metrics · profiles · dashboards · alerts (alerts firing badge,
  org-wide `useAlerts(orgId)`). Every org link carries `carriedOrgSearch`.
- Dashboards org-wide: `useDashboardIndex(orgId, projectId: string | undefined, envId)`
  — undefined = org-wide (the wire field must be OMITTED, not ""). The org
  dashboards view lists rows from every app; a row links to its app's console
  dashboard page; no save/copy on the org list.

## E. Entry points — `ConsoleLayout.tsx` (switchTarget), `ConsoleProjectSwitcher.tsx`, `Settings/cloud/billing.tsx`, Sidebar/Mobile/e2e
- Switcher lists `useOrgForgeApps().apps` (non-"" ones) plus "All apps" → `/forge`.
  `switchTarget(app: string, page)` takes the forge app NAME.
- billing back button: `/forge?project=<currentProject.id>` when a current project exists, else `/forge`.
