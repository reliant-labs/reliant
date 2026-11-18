/**
 * The forge console's pages, as the forge shell (forgeShellRoutes.tsx) and the
 * forge demo (demoShellRoutes.tsx) mount them: the same lazy chunks for every
 * route createForgeConsoleRoutes declares. Reliant's tree (routes.tsx) wraps
 * its own in ForgeGate, so it keeps its own map.
 */

import type { ForgeConsoleComponents } from './forgeConsoleRoutes'
import { lazyRoute } from './lib/lazyRoute'

export const forgeConsolePages: ForgeConsoleComponents = {
  layout: lazyRoute(() => import('./components/Forge/ForgeLayout'), 'ForgeLayout'),
  console: lazyRoute(() => import('./components/Forge/Console/ConsoleLayout'), 'ConsoleLayout'),
  overview: lazyRoute(() => import('./components/Forge/Workloads/WorkloadPages'), 'OverviewPage'),
  workload: lazyRoute(() => import('./components/Forge/Workloads/WorkloadPages'), 'WorkloadPage'),
  releases: lazyRoute(() => import('./components/Forge/Releases/ReleasesPages'), 'ReleasesPage'),
  release: lazyRoute(() => import('./components/Forge/Releases/ReleasesPages'), 'ReleasePage'),
  observabilityView: lazyRoute(() => import('./components/Forge/Console/ConsolePages'), 'ObservabilityViewPage'),
  dashboards: lazyRoute(() => import('./components/Forge/Observability/Dashboards/DashboardsPages'), 'DashboardsPage'),
  dashboard: lazyRoute(() => import('./components/Forge/Observability/Dashboards/DashboardsPages'), 'DashboardPage'),
  storage: lazyRoute(() => import('./components/Forge/Storage/StoragePage'), 'StoragePage'),
  environments: lazyRoute(() => import('./components/Forge/Overview/EnvironmentsPage'), 'EnvironmentsPage'),
  environment: lazyRoute(() => import('./components/Forge/EnvPage/ForgeEnvPage'), 'ForgeEnvPage'),
  secrets: lazyRoute(() => import('./components/Forge/Console/SecretsPage'), 'SecretsPage'),
  domains: lazyRoute(() => import('./components/Forge/Domains/ForgeDomainsPage'), 'ForgeDomainsPage'),
  activity: lazyRoute(() => import('./components/Forge/Console/ActivityPage'), 'ActivityPage'),
  data: lazyRoute(() => import('./components/Forge/Data/DataPage'), 'DataPage'),
  users: lazyRoute(() => import('./components/Forge/Users/UsersPage'), 'UsersPage'),
  orgLayout: lazyRoute(() => import('./components/Forge/Org'), 'OrgConsoleLayout'),
  orgObservability: lazyRoute(() => import('./components/Forge/Console/ForgeEntryRoutes'), 'OrgObservabilityRoute'),
  landing: lazyRoute(() => import('./components/Forge/Console/ForgeEntryRoutes'), 'ForgeLandingRoute'),
  legacyEnv: lazyRoute(() => import('./components/Forge/Console/ForgeEntryRoutes'), 'LegacyEnvRoute'),
  legacyDomains: lazyRoute(() => import('./components/Forge/Console/ForgeEntryRoutes'), 'LegacyDomainsRoute'),
  legacyCommand: lazyRoute(() => import('./components/Forge/Console/ForgeEntryRoutes'), 'LegacyCommandRoute'),
}

/**
 * Warm, one per idle moment, the chunks a console visit opens first: the
 * frame, the Overview, an Observability view and the views' panel. A host
 * calls it after its first paint, so the first tap into the console renders
 * instead of downloading. Nothing is shown and nothing reloads: a chunk a
 * deploy removed is found when its page is opened (lazyRoute.tsx).
 */
export function preloadForgeConsole(): void {
  const saveData = (navigator as Navigator & { connection?: { saveData?: boolean } }).connection?.saveData
  if (saveData) return
  const loads: (() => Promise<unknown> | undefined)[] = [
    () => forgeConsolePages.layout.preload?.(),
    () => forgeConsolePages.console.preload?.(),
    () => forgeConsolePages.overview.preload?.(),
    () => forgeConsolePages.observabilityView.preload?.(),
    () => import('./components/Forge/Observability/ObservabilityPanel'),
  ]
  const whenIdle = (fn: () => void) =>
    typeof requestIdleCallback === 'function' ? requestIdleCallback(fn, { timeout: 5_000 }) : setTimeout(fn, 300)
  const next = () => {
    const load = loads.shift()
    if (!load) return
    void Promise.resolve()
      .then(load)
      .catch(() => {})
      .finally(() => whenIdle(next))
  }
  whenIdle(next)
}
