/**
 * The demo shell's route tree: what the reliant-web bundle mounts on the demo
 * host (`demo.reliantlabs.io`, lib/shell.ts), in place of routes.tsx.
 *
 *   /forge/demo     the forge demo console on sample data (Forge/Demo), free
 *                   to explore, including legacy ?view=…&tour=… links
 *                   (compact current-page explanations in Demo/tips)
 *   /forge/…        the forge console: every page the forge shell mounts
 *                   (forgeConsoleRoutes.tsx, forgeConsolePages.ts), so every
 *                   tab of its nav opens, on the demo transport's sample data
 *   /demo           → /forge/demo, keeping the query
 *   /               → the console's overview (/forge/p/shop), keeping the
 *                     query (`?tour=`, `rid`, `utm_*`)
 *   anything else   → the console's overview, keeping the query
 *
 * A tree of its own rather than a flag on the forge shell's: there is no
 * /auth, /oauth or /claim route here, so nothing on this host can sign in.
 * The console's pages sit under a pathless `_authenticated` layout with NO
 * guard: the pages read their params and search by route id
 * (`/_authenticated/_forge/forge/p/$project/…`, as forgeShellRoutes.tsx
 * explains), and every RPC they make is answered by the demo's transport
 * (demoShellBoot.ts), which refuses writes. No ActiveOrgBinding and no
 * landing-journey handoff: each page offers an optional tour without auto-start.
 */

import { createRootRoute, createRoute, createRouter, Navigate, Outlet, redirect, useRouterState } from '@tanstack/react-router'

import { captureForgeEntry } from './components/ForgeShell/forgeEntry'
import { bootDemoShell } from './components/Forge/Demo/demoShellBoot'
import { DemoTourToast } from './components/Forge/Demo/DemoTourToast'
import { DemoTips } from './components/Forge/Demo/tips'
import { createForgeDemoRedirectRoute, createForgeDemoRoute } from './components/Forge/Demo/demoRoute'
import { consolePath } from './components/Forge/Console/paths'
import { FIXTURE_PROJECT } from './components/Forge/Observability/__fixtures__/fixtureApi'
import { createForgeConsoleRoutes } from './forgeConsoleRoutes'
import { forgeConsolePages } from './forgeConsolePages'
import { DemoConsoleProviders } from './demoBundle/DemoConsoleProviders'
import { JourneyTour } from './components/Onboarding/JourneyTour'
import { PageVisitTracker } from './components/PageVisitTracker'
import { ObsReadOnlyProvider } from './components/Forge/Observability/obsReadOnly'
import { NewBuildPrompt } from './components/NewBuildPrompt'
import { reportCaughtError, RouteErrorFallback } from './components/ErrorBoundary'
import { SurfaceProvider } from './lib/surfaceContext'
import { Toaster } from './lib/toast'

// Before the router replaces the URL: the landing handoff (`rid`, `utm_*`) it arrived with.
const entry = captureForgeEntry()
// Before anything renders the tour: forge copy, memory-only progress, anonymous telemetry.
bootDemoShell()

/** Where the demo lands: the sample app's console, whose nav opens every real page. */
export const DEMO_HOME = consolePath(FIXTURE_PROJECT, { kind: 'overview' })

const READ_ONLY = { readOnly: true, reason: 'Read-only demo' } as const

function DemoRootShell() {
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  return (
    <SurfaceProvider surface="desktop">
      {/* Every page's write controls disabled, with this reason (obsReadOnly.tsx). */}
      <ObsReadOnlyProvider value={READ_ONLY}>
        <div data-shell="demo" data-pathname={pathname} className="contents">
          <PageVisitTracker firstVisitProps={entry} />
          <Outlet />
          {/* No ModalLayer: its modals are Reliant's (API key, upgrade, out of date). */}
          <Toaster />
          <NewBuildPrompt />
          <JourneyTour />
          <DemoTips />
          <DemoTourToast />
        </div>
      </ObsReadOnlyProvider>
    </SurfaceProvider>
  )
}

export function buildDemoShellTree() {
  const rootRoute = createRootRoute({
    component: DemoRootShell,
    notFoundComponent: () => <Navigate to={DEMO_HOME} search={{}} replace />,
    errorComponent: RouteErrorFallback,
  })

  const indexRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/',
    beforeLoad: ({ location }) => {
      throw redirect({ href: `${DEMO_HOME}${location.searchStr}`, replace: true })
    },
  })

  // Every other path: a link to a page this host does not have (/auth, /apps,
  // a console URL) lands on the demo rather than a blank page.
  const elsewhereRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '$',
    beforeLoad: ({ location }) => {
      throw redirect({ href: `${DEMO_HOME}${location.searchStr}`, replace: true })
    },
  })

  // The console, unguarded: the id is the one the shared pages read by. Its
  // pages read the demo's Observability API from context (DemoConsoleProviders).
  const consoleLayoutRoute = createRoute({
    getParentRoute: () => rootRoute,
    id: '_authenticated',
    component: () => (
      <DemoConsoleProviders>
        <Outlet />
      </DemoConsoleProviders>
    ),
  })

  return rootRoute.addChildren([
    indexRoute,
    elsewhereRoute,
    createForgeDemoRoute(() => rootRoute),
    createForgeDemoRedirectRoute(() => rootRoute),
    consoleLayoutRoute.addChildren([createForgeConsoleRoutes(() => consoleLayoutRoute, forgeConsolePages)]),
  ])
}

export const demoRouteTree = buildDemoShellTree()

export const router = createRouter({
  routeTree: demoRouteTree,
  defaultErrorComponent: RouteErrorFallback,
  defaultOnCatch: (error, errorInfo) => reportCaughtError(error, errorInfo.componentStack),
})

// Stores that drive the URL without importing a route module read the router
// from here (see routes.tsx); in the demo shell it is this one.
;(globalThis as { __RELIANT_ROUTER?: typeof router }).__RELIANT_ROUTER = router
