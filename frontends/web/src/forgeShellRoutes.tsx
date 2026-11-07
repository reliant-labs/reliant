/**
 * The forge shell's route tree: what the reliant-web bundle mounts on the
 * forge host (lib/shell.ts), in place of routes.tsx.
 *
 *   /auth, /auth/callback, /verify-email,
 *   /reset-password, /upgrade                 sign-in, the same accounts and
 *                                             screens as Reliant (forge-branded
 *                                             by AuthLayout); the session is
 *                                             this origin's own
 *   /oauth/cli, /oauth/consent                `forge login` and OAuth consent
 *   /claim                                    claim a no-signup deploy
 *                                             (components/Claim), landing in
 *                                             the app's console
 *   /forge/demo, /demo                        the forge journey's demo console
 *                                             on fixture data: public
 *   _authenticated                            ForgeAuthGuard: signed in, nothing else
 *     _forgeShell                             ForgeShellLayout: brand, nav, org,
 *                                             "Open in Reliant"
 *       /apps, /sites, /usage,
 *       /settings, /settings/$section
 *     /forge/…                                the forge console, the SAME route
 *                                             factory and pages as Reliant
 *                                             (forgeConsoleRoutes.tsx)
 *   /                                         → /apps
 *
 * Route IDS match routes.tsx where pages are shared — `/auth`, `/upgrade`,
 * `/auth/callback`, `/_authenticated/_forge/forge/p/$project/…` — because those
 * pages read their params and search by route id. That is why the guard's
 * layout keeps the id `_authenticated`.
 *
 * Nothing here reaches a daemon or Reliant's onboarding, chats or projects.
 * The pages that would (deploy, promote, the local dev stack) say how to run
 * them from a terminal instead (ForgeShell/RunFromTerminal).
 */

import type { ComponentType, ReactNode } from 'react'
import { createRootRoute, createRoute, createRouter, Navigate, Outlet, useRouterState, type AnyRoute } from '@tanstack/react-router'

import { captureForgeEntry } from './components/ForgeShell/forgeEntry'
// The workflows journey's content: its step ids still resolve here (the demo bundle leaves it out).
import './components/Onboarding/journeys/catalog'
import { ForgeAuthGuard } from './components/ForgeShell/ForgeAuthGuard'
import { ForgeLandingJourney } from './components/ForgeShell/ForgeLandingJourney'
import { ForgeShellLayout } from './components/ForgeShell/ForgeShellLayout'
import { bootForgeTour } from './components/ForgeShell/forgeTourBoot'
import { JourneyTour } from './components/Onboarding/JourneyTour'
import { AuthGuard } from './components/AuthGuard'
import { createForgeDemoRedirectRoute, createForgeDemoRoute } from './components/Forge/Demo/demoRoute'
import { ActiveOrgBinding } from './components/Orgs/ActiveOrgBinding'
import { PageVisitTracker } from './components/PageVisitTracker'
import { ModalLayer } from './components/Modals/ModalLayer'
import { NewBuildPrompt } from './components/NewBuildPrompt'
import { reportCaughtError, RouteErrorFallback } from './components/ErrorBoundary'
import { createForgeConsoleRoutes } from './forgeConsoleRoutes'
import { getIsDev } from './lib/constants'
import { lazyRoute } from './lib/lazyRoute'
import { SurfaceProvider } from './lib/surfaceContext'
import { Toaster } from './lib/toast'
import { authSearchSchema, oauthCallbackSearchSchema, upgradeSearchSchema } from './routeSchemas'

// Taken before the router exists: the guard's sign-in redirect replaces the
// URL the landing handoff arrived on (forgeEntry.ts).
const entry = captureForgeEntry()
// Before anything renders the tour: this shell, and progress in localStorage.
bootForgeTour()

const AuthScreen = lazyRoute(() => import('./components/AuthScreen'), 'AuthScreen')
const OAuthCallback = lazyRoute(() => import('./components/OAuthCallback'), 'OAuthCallback')
const ResetPasswordScreen = lazyRoute(() => import('./components/ResetPasswordScreen'), 'ResetPasswordScreen')
const EmailVerification = lazyRoute(() => import('./components/EmailVerification'), 'EmailVerification')
const UpgradeAccount = lazyRoute(() => import('./components/UpgradeAccount'), 'UpgradeAccount')
const ClaimPage = lazyRoute(() => import('./components/Claim/ClaimPage'), 'ClaimPage')
const CliLogin = lazyRoute(() => import('./components/Auth/CliLogin'), 'CliLogin')
const OAuthConsent = lazyRoute(() => import('./components/Auth/OAuthConsent'), 'OAuthConsent')

const ForgeAppsPage = lazyRoute(() => import('./components/ForgeShell/ForgeAppsPage'), 'ForgeAppsPage')
const ForgeSitesPage = lazyRoute(() => import('./components/ForgeShell/ForgeSitesPage'), 'ForgeSitesPage')
const ForgeUsagePage = lazyRoute(() => import('./components/ForgeShell/ForgeUsagePage'), 'ForgeUsagePage')
const ForgeSettingsPage = lazyRoute(() => import('./components/ForgeShell/ForgeSettingsPage'), 'ForgeSettingsPage')

const ForgeLayout = lazyRoute(() => import('./components/Forge/ForgeLayout'), 'ForgeLayout')
const ConsoleLayout = lazyRoute(() => import('./components/Forge/Console/ConsoleLayout'), 'ConsoleLayout')

function ForgeRootShell() {
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  return (
    <SurfaceProvider surface="desktop">
      <div data-shell="forge" data-pathname={pathname} className="contents">
        <ActiveOrgBinding />
        <PageVisitTracker firstVisitProps={entry} />
        <Outlet />
        <ModalLayer />
        <Toaster />
        <NewBuildPrompt />
        {/* The journeys (Onboarding/JOURNEYS.md): at the root, so a step
            follows the visitor between the public demo and signed-in pages. */}
        <JourneyTour />
        <ForgeLandingJourney />
      </div>
    </SurfaceProvider>
  )
}

/**
 * The tree, as a factory so the dev preview (ForgeShell/__preview__) mounts
 * the SAME routes and pages against fixtures, with the guard swapped for a
 * pass-through. `extraRoutes` adds root-level routes (the preview itself).
 */
export function buildForgeShellTree({
  Guard,
  extraRoutes = () => [],
}: {
  Guard: ComponentType<{ children: ReactNode }>
  extraRoutes?: (root: AnyRoute) => AnyRoute[]
}) {
  const rootRoute = createRootRoute({
    component: ForgeRootShell,
    notFoundComponent: () => <Navigate to="/apps" search={{}} />,
    errorComponent: RouteErrorFallback,
  })

  const authRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/auth',
    validateSearch: authSearchSchema,
    component: () => (
      <AuthGuard requireAuth={false}>
        <AuthScreen />
      </AuthGuard>
    ),
  })

  const oauthCallbackRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/auth/callback',
    validateSearch: oauthCallbackSearchSchema,
    component: OAuthCallback,
  })

  const oauthConsentRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/oauth/consent',
    component: OAuthConsent,
  })

  // `forge login` on a CLI configured for this host. Unguarded: the page signs
  // the user in itself and must keep the whole query string across that detour.
  const cliLoginRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/oauth/cli',
    component: CliLogin,
  })

  const resetPasswordRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/reset-password',
    component: () => (
      <AuthGuard requireAuth={true}>
        <ResetPasswordScreen />
      </AuthGuard>
    ),
  })

  const verifyEmailRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/verify-email',
    component: () => (
      <AuthGuard requireAuth={true} requireEmailVerification={false}>
        <EmailVerification />
      </AuthGuard>
    ),
  })

  // The claim flow's sign-up: an anonymous session gains an email and comes back to /claim.
  const upgradeRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/upgrade',
    validateSearch: upgradeSearchSchema,
    component: () => (
      <AuthGuard requireAuth={true} requireEmailVerification={false}>
        <UpgradeAccount />
      </AuthGuard>
    ),
  })

  // Unguarded: the page previews the claim to someone with no session. The
  // token never reaches the router (lib/claimLinkBoot runs first).
  const claimRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/claim',
    component: ClaimPage,
  })

  // The forge journey's demo console, public: fixture observability data, no
  // daemon, no chat, no account. Root-level, outside the guard, so it is
  // reached signed out; `/demo` points at it (a landing link's short form).
  const forgeDemoRoute = createForgeDemoRoute(() => rootRoute)
  const demoRedirectRoute = createForgeDemoRedirectRoute(() => rootRoute)

  const indexRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/',
    component: () => <Navigate to="/apps" search={{}} replace />,
  })

  const authenticatedLayoutRoute = createRoute({
    getParentRoute: () => rootRoute,
    id: '_authenticated',
    component: () => (
      <Guard>
        <Outlet />
      </Guard>
    ),
  })

  const shellLayoutRoute = createRoute({
    getParentRoute: () => authenticatedLayoutRoute,
    id: '_forgeShell',
    component: () => <ForgeShellLayout />,
  })

  const appsRoute = createRoute({
    getParentRoute: () => shellLayoutRoute,
    path: '/apps',
    component: ForgeAppsPage,
  })

  const sitesRoute = createRoute({
    getParentRoute: () => shellLayoutRoute,
    path: '/sites',
    component: ForgeSitesPage,
  })

  const usageRoute = createRoute({
    getParentRoute: () => shellLayoutRoute,
    path: '/usage',
    component: ForgeUsagePage,
  })

  const settingsRoute = createRoute({
    getParentRoute: () => shellLayoutRoute,
    path: '/settings',
    component: ForgeSettingsPage,
  })

  const settingsSectionRoute = createRoute({
    getParentRoute: () => shellLayoutRoute,
    path: '/settings/$section',
    component: ForgeSettingsPage,
  })

  // The console: every page as Reliant mounts it, minus ForgeGate (the forge
  // shell IS the feature) — the pages themselves hide what needs a daemon.
  const forgeConsoleRoutes = createForgeConsoleRoutes(() => authenticatedLayoutRoute, {
    layout: ForgeLayout,
    console: ConsoleLayout,
    overview: lazyRoute(() => import('./components/Forge/Workloads/WorkloadPages'), 'OverviewPage'),
    workload: lazyRoute(() => import('./components/Forge/Workloads/WorkloadPages'), 'WorkloadPage'),
    releases: lazyRoute(() => import('./components/Forge/Releases/ReleasesPages'), 'ReleasesPage'),
    release: lazyRoute(() => import('./components/Forge/Releases/ReleasesPages'), 'ReleasePage'),
    observabilityView: lazyRoute(() => import('./components/Forge/Console/ConsolePages'), 'ObservabilityViewPage'),
    dashboards: lazyRoute(() => import('./components/Forge/Observability/Dashboards/DashboardsPages'), 'DashboardsPage'),
    dashboard: lazyRoute(() => import('./components/Forge/Observability/Dashboards/DashboardsPages'), 'DashboardPage'),
    data: lazyRoute(() => import('./components/Forge/Data/DataPage'), 'DataPage'),
    environments: lazyRoute(() => import('./components/Forge/Overview/EnvironmentsPage'), 'EnvironmentsPage'),
    environment: lazyRoute(() => import('./components/Forge/EnvPage/ForgeEnvPage'), 'ForgeEnvPage'),
    secrets: lazyRoute(() => import('./components/Forge/Console/ConsolePages'), 'SecretsPage'),
    domains: lazyRoute(() => import('./components/Forge/Domains/ForgeDomainsPage'), 'ForgeDomainsPage'),
    activity: lazyRoute(() => import('./components/Forge/Console/ConsolePages'), 'ActivityPage'),
    orgLayout: lazyRoute(() => import('./components/Forge/Org'), 'OrgConsoleLayout'),
    orgObservability: lazyRoute(() => import('./components/Forge/Console/ForgeEntryRoutes'), 'OrgObservabilityRoute'),
    landing: lazyRoute(() => import('./components/Forge/Console/ForgeEntryRoutes'), 'ForgeLandingRoute'),
    legacyEnv: lazyRoute(() => import('./components/Forge/Console/ForgeEntryRoutes'), 'LegacyEnvRoute'),
    legacyDomains: lazyRoute(() => import('./components/Forge/Console/ForgeEntryRoutes'), 'LegacyDomainsRoute'),
    legacyCommand: lazyRoute(() => import('./components/Forge/Console/ForgeEntryRoutes'), 'LegacyCommandRoute'),
  })

  return rootRoute.addChildren([
    authRoute,
    oauthCallbackRoute,
    oauthConsentRoute,
    cliLoginRoute,
    resetPasswordRoute,
    verifyEmailRoute,
    upgradeRoute,
    claimRoute,
    forgeDemoRoute,
    demoRedirectRoute,
    indexRoute,
    ...extraRoutes(rootRoute),
    authenticatedLayoutRoute.addChildren([
      shellLayoutRoute.addChildren([appsRoute, sitesRoute, usageRoute, settingsRoute, settingsSectionRoute]),
      forgeConsoleRoutes,
    ]),
  ])
}

// DEV-BUILD ONLY: the forge shell against fixtures, signed in as a fake user,
// for screenshots and visual review — /forge-shell-preview?path=/apps.
const ForgeShellPreview = lazyRoute(() => import('./components/ForgeShell/__preview__/ForgeShellPreview'), 'ForgeShellPreview')

export const forgeRouteTree = buildForgeShellTree({
  Guard: ForgeAuthGuard,
  extraRoutes: (root) => [
    createRoute({
      getParentRoute: () => root,
      path: '/forge-shell-preview',
      component: () => (getIsDev() ? <ForgeShellPreview /> : <Navigate to="/apps" search={{}} />),
    }),
  ],
})

export const router = createRouter({
  routeTree: forgeRouteTree,
  defaultErrorComponent: RouteErrorFallback,
  defaultOnCatch: (error, errorInfo) => reportCaughtError(error, errorInfo.componentStack),
})

// Stores that drive the URL without importing a route module read the router
// from here (see routes.tsx); in the forge shell it is this one.
;(globalThis as { __RELIANT_ROUTER?: typeof router }).__RELIANT_ROUTER = router
