/**
 * The forge console's routes (research/console-v3/PROPOSAL.md §3.2), as a
 * factory so the route tree (routes.tsx) and its tests mount the SAME
 * definitions — paths, search schemas, the redirects — with only the page
 * components swapped. A test that mirrored the tree by hand would pass
 * against the mirror while the real tree drifted.
 *
 *   _forge                                  ForgeLayout: Escape, the exit
 *     /forge                                → resolve the project, redirect
 *     /forge/env/$env, /forge/observability,
 *     /forge/domains, /forge/topology, …    → the retired URLs, redirected
 *     /forge/p/$project                     ConsoleLayout: nav, top bar, scope
 *       /                                   Overview
 *       /releases, /releases/$version
 *       /observability, /observability/$view
 *       /environments, /environments/$env
 *       /secrets, /domains, /monitoring, /settings, /activity
 *
 * The path builders and the redirect mapping live in
 * components/Forge/Console/paths.ts; this file only declares the routes.
 */

import { createRoute, redirect, type AnyRoute, type RouteComponent } from '@tanstack/react-router'

import { isObsNavView } from './components/Forge/Console/paths'
import {
  forgeConsoleActivitySearchSchema,
  forgeConsoleEnvSearchSchema,
  forgeConsoleObsSearchSchema,
  forgeConsoleReleaseSearchSchema,
  forgeConsoleReleasesSearchSchema,
  forgeConsoleSearchSchema,
  forgeConsoleSecretsSearchSchema,
  forgeLegacyEnvSearchSchema,
  forgeLegacySearchSchema,
  forgeObservabilitySearchSchema,
  forgeResolveSearchSchema,
} from './routeSchemas'

export interface ForgeConsoleComponents {
  /** The pathless `_forge` layout. */
  layout: RouteComponent
  /** /forge/p/$project's layout. */
  console: RouteComponent
  overview: RouteComponent
  releases: RouteComponent
  release: RouteComponent
  observability: RouteComponent
  observabilityView: RouteComponent
  environments: RouteComponent
  environment: RouteComponent
  secrets: RouteComponent
  domains: RouteComponent
  monitoring: RouteComponent
  settings: RouteComponent
  activity: RouteComponent
  /** The entries that resolve a project and redirect (ForgeEntryRoutes.tsx). */
  landing: RouteComponent
  legacyEnv: RouteComponent
  legacyObservability: RouteComponent
  legacyDomains: RouteComponent
  legacyCommand: RouteComponent
}

type LegacyCommandPath = '/forge/topology' | '/forge/environments' | '/forge/status' | '/forge/secrets'

export function createForgeConsoleRoutes<TParent extends AnyRoute>(
  getParentRoute: () => TParent,
  components: ForgeConsoleComponents,
) {
  const layoutRoute = createRoute({
    getParentRoute,
    id: '_forge',
    component: components.layout,
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
  const legacyObservabilityRoute = createRoute({
    getParentRoute: () => layoutRoute,
    path: '/forge/observability',
    validateSearch: forgeObservabilitySearchSchema,
    component: components.legacyObservability,
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
    path: '/forge/p/$project',
    component: components.console,
  })
  const overviewRoute = createRoute({
    getParentRoute: () => consoleRoute,
    path: '/',
    validateSearch: forgeConsoleObsSearchSchema,
    component: components.overview,
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
  const observabilityRoute = createRoute({
    getParentRoute: () => consoleRoute,
    path: 'observability',
    validateSearch: forgeConsoleObsSearchSchema,
    component: components.observability,
  })
  // A view that is not a nav entry (a hand-edited URL) lands on the
  // Observability overview rather than an empty page.
  const observabilityViewRoute = createRoute({
    getParentRoute: () => consoleRoute,
    path: 'observability/$view',
    validateSearch: forgeConsoleObsSearchSchema,
    beforeLoad: ({ params, location }) => {
      if (isObsNavView(params.view)) return
      throw redirect({ href: `${location.pathname.replace(/\/[^/]*\/?$/, '')}${location.searchStr}`, replace: true })
    },
    component: components.observabilityView,
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
  const monitoringRoute = createRoute({
    getParentRoute: () => consoleRoute,
    path: 'monitoring',
    validateSearch: forgeConsoleSearchSchema,
    component: components.monitoring,
  })
  const activityRoute = createRoute({
    getParentRoute: () => consoleRoute,
    path: 'activity',
    validateSearch: forgeConsoleActivitySearchSchema,
    component: components.activity,
  })
  const settingsRoute = createRoute({
    getParentRoute: () => consoleRoute,
    path: 'settings',
    validateSearch: forgeConsoleObsSearchSchema,
    component: components.settings,
  })

  return layoutRoute.addChildren([
    landingRoute,
    legacyEnvRoute,
    legacyObservabilityRoute,
    legacyDomainsRoute,
    legacyCommandRoute('/forge/topology'),
    legacyCommandRoute('/forge/environments'),
    legacyCommandRoute('/forge/status'),
    legacyCommandRoute('/forge/secrets'),
    consoleRoute.addChildren([
      overviewRoute,
      releasesRoute,
      releaseRoute,
      observabilityRoute,
      observabilityViewRoute,
      environmentsRoute,
      environmentRoute,
      secretsRoute,
      domainsRoute,
      monitoringRoute,
      settingsRoute,
      activityRoute,
    ]),
  ])
}
