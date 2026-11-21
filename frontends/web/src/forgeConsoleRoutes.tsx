/**
 * The forge console's routes, as a factory so the route tree (routes.tsx) and its tests mount the SAME
 * definitions — paths, search schemas, the redirects — with only the page
 * components swapped. A test that mirrored the tree by hand would pass
 * against the mirror while the real tree drifted.
 *
 *   _forge                                  ForgeLayout: Escape, the exit
 *     /forge                                → /apps (the Apps list); ?project= / ?forgeProject= → that app's console
 *     _forgeOrg                             OrgConsoleLayout: the org's nav (no app chosen)
 *       /forge/observability                org-wide Observability (?project= → that app's)
 *       /forge/observability/$view          errors · sessions · logs · traces · metrics · profiles · dashboards · alerts
 *     /forge/env/$env, /forge/domains,
 *     /forge/topology, …                    → the retired URLs, redirected
 *     /forge/p/$project                     ConsoleLayout: nav, top bar, scope
 *       /                                   Overview (with the workloads)
 *       /workloads/$env/$name               one workload
 *       /releases, /releases/$version
 *       /observability/$view
 *       /observability/dashboards, /observability/dashboards/$kind/$key
 *       /storage                            the storage browser (the scope's buckets)
 *       /data                               the data browser (the scope's databases)
 *       /users                              who can sign in to the scope (its managed identity)
 *       /environments, /environments/$env
 *       /secrets, /domains, /activity
 *       /experiments, /experiments/$changeId  Forge Changes (remote config, rollouts, A/B tests)
 *       /security/vulnerabilities           Security: the findings of the scope's releases
 *       /security/vulnerabilities/$advisory one advisory across the project
 *       /security/sboms                     each release artifact's SBOM and scan
 *       /security                           → /security/vulnerabilities
 *       /observability, /monitoring, /settings → retired, redirected (paths.ts)
 *
 * The path builders and the redirect mapping live in
 * components/Forge/Console/paths.ts; this file only declares the routes.
 * Every redirect here runs only outside the org tree or in a member org
 * (memberOnly, lib/routeOrg.ts): under `/o/$org` a redirect must never carry
 * a URL off the "no access" page.
 *
 * The org form (`form: 'org'`, mounted under `/o/$org` by routes.tsx; control-plane
 * docs/design/unified-shell-and-projects.md §3.1) is the same tree with the
 * two paths the design moves: the console at `/apps/$project` and the org
 * Observability at `/observability[/$view]`. The entries and retired paths
 * keep their `/forge/…` paths there: harmless, and one tree shape for both.
 * The moved paths are typed as today's, so both forms register the same
 * route types (the pages read params without a route id).
 */

import { createRoute, redirect, type AnyRoute, type RouteComponent } from '@tanstack/react-router'

import { usagePath } from './lib/paths'
import { inOrgOfPath } from './lib/paths/org'
import { memberOnly } from './lib/routeOrg'

import { isObsNavView, OBS_NAV_VIEWS, orgUsagePath } from './components/Forge/Console/paths'
import {
  forgeConsoleActivitySearchSchema,
  forgeConsoleDashboardSearchSchema,
  forgeConsoleDashboardsSearchSchema,
  forgeConsoleDataSearchSchema,
  forgeConsoleEnvSearchSchema,
  forgeConsoleExperimentsSearchSchema,
  forgeConsoleObsSearchSchema,
  forgeConsoleOverviewSearchSchema,
  forgeConsoleReleaseSearchSchema,
  forgeConsoleReleasesSearchSchema,
  forgeConsoleSearchSchema,
  forgeConsoleSbomsSearchSchema,
  forgeConsoleSecretsSearchSchema,
  forgeConsoleStorageSearchSchema,
  forgeConsoleUsersSearchSchema,
  forgeConsoleVulnerabilitiesSearchSchema,
  forgeConsoleWorkloadSearchSchema,
  forgeLegacyEnvSearchSchema,
  forgeLegacySearchSchema,
  forgeOrgObsSearchSchema,
  forgeResolveSearchSchema,
} from './routeSchemas'

export interface ForgeConsoleComponents {
  /** The pathless `_forge` layout. */
  layout: RouteComponent
  /** /forge/p/$project's layout. */
  console: RouteComponent
  overview: RouteComponent
  /** One workload in one environment (the Overview's detail). */
  workload: RouteComponent
  releases: RouteComponent
  release: RouteComponent
  observabilityView: RouteComponent
  /** Observability › Dashboards, and one dashboard. */
  dashboards: RouteComponent
  dashboard: RouteComponent
  /** Storage: the scoped environment's buckets, read-only. */
  storage: RouteComponent
  /** Data: the scoped environment's databases, read-only. */
  data: RouteComponent
  /** Users: who can sign in to the scoped environment (its managed identity). */
  users: RouteComponent
  environments: RouteComponent
  environment: RouteComponent
  secrets: RouteComponent
  domains: RouteComponent
  activity: RouteComponent
  /** Experiments: the project's Forge Changes, and one change. */
  experiments: RouteComponent
  experiment: RouteComponent
  /** Security › Vulnerabilities: the findings of the scope's releases. */
  vulnerabilities: RouteComponent
  /** One advisory in the project. */
  vulnerability: RouteComponent
  /** Security › SBOMs: each release artifact's SBOM and its scan. */
  sboms: RouteComponent
  /** The pathless `_forgeOrg` layout: the org console's chrome (components/Forge/Org). */
  orgLayout: RouteComponent
  /** /forge/observability and /forge/observability/$view: org-wide Observability. */
  orgObservability: RouteComponent
  /** /forge: to the Apps list, or the entry that resolves a project and redirects (ForgeEntryRoutes.tsx). */
  landing: RouteComponent
  legacyEnv: RouteComponent
  legacyDomains: RouteComponent
  legacyCommand: RouteComponent
}

type LegacyCommandPath = '/forge/topology' | '/forge/environments' | '/forge/status' | '/forge/secrets'

export interface ForgeConsoleRouteOptions {
  /** Today's paths (`/forge/p/$project`, the default), or the org form under `/o/$org` (`/apps/$project`). */
  form?: 'legacy' | 'org'
}

export function createForgeConsoleRoutes<TParent extends AnyRoute>(
  getParentRoute: () => TParent,
  components: ForgeConsoleComponents,
  { form = 'legacy' }: ForgeConsoleRouteOptions = {},
) {
  // The org form's two moved paths are typed as today's (see the header).
  const inOrg = form === 'org'

  const layoutRoute = createRoute({
    getParentRoute,
    id: '_forge',
    component: components.layout,
  })

  // ── The org console: org-wide Observability ──
  const orgLayoutRoute = createRoute({
    getParentRoute: () => layoutRoute,
    id: '_forgeOrg',
    component: components.orgLayout,
  })
  const orgObservabilityRoute = createRoute({
    getParentRoute: () => orgLayoutRoute,
    path: (inOrg ? '/observability' : '/forge/observability') as '/forge/observability',
    validateSearch: forgeOrgObsSearchSchema,
    component: components.orgObservability,
  })
  // A view that is not a nav entry (a hand-edited URL) lands on the org Observability overview.
  const orgObservabilityViewRoute = createRoute({
    getParentRoute: () => orgLayoutRoute,
    path: (inOrg ? '/observability/$view' : '/forge/observability/$view') as '/forge/observability/$view',
    validateSearch: forgeOrgObsSearchSchema,
    beforeLoad: memberOnly(({ params, location }) => {
      if (isObsNavView(params.view)) return
      throw redirect({ href: `${location.pathname.replace(/\/[^/]*\/?$/, '')}${location.searchStr}`, replace: true })
    }),
    component: components.orgObservability,
  })

  // ── Entries that resolve a project and redirect ──
  const landingRoute = createRoute({
    getParentRoute: () => layoutRoute,
    path: '/forge',
    validateSearch: forgeResolveSearchSchema,
    component: components.landing,
  })
  const legacyEnvRoute = createRoute({
    getParentRoute: () => layoutRoute,
    path: '/forge/env/$env',
    validateSearch: forgeLegacyEnvSearchSchema,
    component: components.legacyEnv,
  })
  const legacyDomainsRoute = createRoute({
    getParentRoute: () => layoutRoute,
    path: '/forge/domains',
    validateSearch: forgeResolveSearchSchema,
    component: components.legacyDomains,
  })
  const legacyCommandRoute = (path: LegacyCommandPath) =>
    createRoute({
      getParentRoute: () => layoutRoute,
      path,
      validateSearch: forgeLegacySearchSchema,
      component: components.legacyCommand,
    })

  // ── The console ──
  const consoleRoute = createRoute({
    getParentRoute: () => layoutRoute,
    path: (inOrg ? '/apps/$project' : '/forge/p/$project') as '/forge/p/$project',
    component: components.console,
  })
  const overviewRoute = createRoute({
    getParentRoute: () => consoleRoute,
    path: '/',
    validateSearch: forgeConsoleOverviewSearchSchema,
    component: components.overview,
  })
  const workloadRoute = createRoute({
    getParentRoute: () => consoleRoute,
    path: 'workloads/$env/$name',
    validateSearch: forgeConsoleWorkloadSearchSchema,
    component: components.workload,
  })
  const releasesRoute = createRoute({
    getParentRoute: () => consoleRoute,
    path: 'releases',
    validateSearch: forgeConsoleReleasesSearchSchema,
    component: components.releases,
  })
  const releaseRoute = createRoute({
    getParentRoute: () => consoleRoute,
    path: 'releases/$version',
    validateSearch: forgeConsoleReleaseSearchSchema,
    component: components.release,
  })
  // The retired Observability home: its health strip, services and recent
  // releases were the Overview's, so the Overview is where it lands.
  const observabilityRoute = createRoute({
    getParentRoute: () => consoleRoute,
    path: 'observability',
    beforeLoad: memberOnly(({ location }) => {
      throw redirect({ href: `${location.pathname.replace(/\/observability\/?$/, '')}${location.searchStr}`, replace: true })
    }),
  })
  // A view that is not a nav entry (a hand-edited URL) lands on the first
  // view rather than an empty page.
  const observabilityViewRoute = createRoute({
    getParentRoute: () => consoleRoute,
    path: 'observability/$view',
    validateSearch: forgeConsoleObsSearchSchema,
    beforeLoad: memberOnly(({ params, location }) => {
      if (isObsNavView(params.view)) return
      throw redirect({ href: `${location.pathname.replace(/\/[^/]*\/?$/, `/${OBS_NAV_VIEWS[0]}`)}${location.searchStr}`, replace: true })
    }),
    component: components.observabilityView,
  })
  // Static, so it wins over observability/$view: the index has search state of its own.
  const dashboardsRoute = createRoute({
    getParentRoute: () => consoleRoute,
    path: 'observability/dashboards',
    validateSearch: forgeConsoleDashboardsSearchSchema,
    component: components.dashboards,
  })
  // A kind that is neither release nor workspace (a hand-edited URL) lands on the index.
  const dashboardRoute = createRoute({
    getParentRoute: () => consoleRoute,
    path: 'observability/dashboards/$kind/$key',
    validateSearch: forgeConsoleDashboardSearchSchema,
    beforeLoad: memberOnly(({ params, location }) => {
      if (params.kind === 'release' || params.kind === 'workspace') return
      throw redirect({ href: `${location.pathname.replace(/\/[^/]*\/[^/]*\/?$/, '')}${location.searchStr}`, replace: true })
    }),
    component: components.dashboard,
  })
  // The storage browser: the scoped environment's buckets (components/Forge/Storage).
  const storageRoute = createRoute({
    getParentRoute: () => consoleRoute,
    path: 'storage',
    validateSearch: forgeConsoleStorageSearchSchema,
    component: components.storage,
  })
  // The data browser: the scoped environment's databases (components/Forge/Data).
  const dataRoute = createRoute({
    getParentRoute: () => consoleRoute,
    path: 'data',
    validateSearch: forgeConsoleDataSearchSchema,
    component: components.data,
  })
  // Who can sign in to the scoped environment (components/Forge/Users).
  const usersRoute = createRoute({
    getParentRoute: () => consoleRoute,
    path: 'users',
    validateSearch: forgeConsoleUsersSearchSchema,
    component: components.users,
  })
  const environmentsRoute = createRoute({
    getParentRoute: () => consoleRoute,
    path: 'environments',
    validateSearch: forgeConsoleSearchSchema,
    component: components.environments,
  })
  const environmentRoute = createRoute({
    getParentRoute: () => consoleRoute,
    path: 'environments/$env',
    validateSearch: forgeConsoleEnvSearchSchema,
    component: components.environment,
  })
  const secretsRoute = createRoute({
    getParentRoute: () => consoleRoute,
    path: 'secrets',
    validateSearch: forgeConsoleSecretsSearchSchema,
    component: components.secrets,
  })
  const domainsRoute = createRoute({
    getParentRoute: () => consoleRoute,
    path: 'domains',
    validateSearch: forgeConsoleSearchSchema,
    component: components.domains,
  })
  // The retired "Monitoring settings" placeholder: alert routing and silences
  // are Observability › Alerts.
  const monitoringRoute = createRoute({
    getParentRoute: () => consoleRoute,
    path: 'monitoring',
    beforeLoad: memberOnly(({ location }) => {
      throw redirect({ href: `${location.pathname.replace(/\/monitoring\/?$/, '/observability/alerts')}${location.searchStr}`, replace: true })
    }),
  })
  const activityRoute = createRoute({
    getParentRoute: () => consoleRoute,
    path: 'activity',
    validateSearch: forgeConsoleActivitySearchSchema,
    component: components.activity,
  })
  // Forge Changes: remote config, gradual rollouts and A/B tests
  // (components/Forge/Experiments). The nav row is behind the web config's
  // CHANGES_UI; the routes are always mounted.
  const experimentsRoute = createRoute({
    getParentRoute: () => consoleRoute,
    path: 'experiments',
    validateSearch: forgeConsoleExperimentsSearchSchema,
    component: components.experiments,
  })
  const experimentRoute = createRoute({
    getParentRoute: () => consoleRoute,
    path: 'experiments/$changeId',
    validateSearch: forgeConsoleExperimentsSearchSchema,
    component: components.experiment,
  })
  // Security (components/Forge/Security). The section has no page of its
  // own: its first page is where /security lands.
  const securityRoute = createRoute({
    getParentRoute: () => consoleRoute,
    path: 'security',
    beforeLoad: memberOnly(({ location }) => {
      throw redirect({ href: `${location.pathname.replace(/\/security\/?$/, '/security/vulnerabilities')}${location.searchStr}`, replace: true })
    }),
  })
  const vulnerabilitiesRoute = createRoute({
    getParentRoute: () => consoleRoute,
    path: 'security/vulnerabilities',
    validateSearch: forgeConsoleVulnerabilitiesSearchSchema,
    component: components.vulnerabilities,
  })
  const vulnerabilityRoute = createRoute({
    getParentRoute: () => consoleRoute,
    path: 'security/vulnerabilities/$advisory',
    validateSearch: forgeConsoleSearchSchema,
    component: components.vulnerability,
  })
  const sbomsRoute = createRoute({
    getParentRoute: () => consoleRoute,
    path: 'security/sboms',
    validateSearch: forgeConsoleSbomsSearchSchema,
    component: components.sboms,
  })
  // The retired "Project settings": it held only the organization's usage,
  // which the app's Settings › Billing shows.
  const settingsRoute = createRoute({
    getParentRoute: () => consoleRoute,
    path: 'settings',
    beforeLoad: memberOnly(({ location }) => {
      // Keep the destination's org in the URL until it commits. Org-less
      // Settings would read the previously committed tab's org instead.
      const href = inOrg ? inOrgOfPath(location.pathname, usagePath()) : orgUsagePath()
      throw redirect({ href, replace: true })
    }),
  })

  return layoutRoute.addChildren([
    landingRoute,
    orgLayoutRoute.addChildren([orgObservabilityRoute, orgObservabilityViewRoute]),
    legacyEnvRoute,
    legacyDomainsRoute,
    legacyCommandRoute('/forge/topology'),
    legacyCommandRoute('/forge/environments'),
    legacyCommandRoute('/forge/status'),
    legacyCommandRoute('/forge/secrets'),
    consoleRoute.addChildren([
      overviewRoute,
      workloadRoute,
      releasesRoute,
      releaseRoute,
      observabilityRoute,
      observabilityViewRoute,
      dashboardsRoute,
      dashboardRoute,
      storageRoute,
      dataRoute,
      usersRoute,
      environmentsRoute,
      environmentRoute,
      secretsRoute,
      domainsRoute,
      monitoringRoute,
      settingsRoute,
      activityRoute,
      experimentsRoute,
      experimentRoute,
      securityRoute,
      vulnerabilitiesRoute,
      vulnerabilityRoute,
      sbomsRoute,
    ]),
  ])
}
