/**
 * The demo shell's route tree: what the reliant-web bundle mounts on the demo
 * host (`demo.reliantlabs.io`, lib/shell.ts), in place of routes.tsx.
 *
 *   /forge/demo     the forge demo console on sample data (Forge/Demo), free
 *                   to explore: a toast offers the guided tour (DemoTourToast)
 *                   and each view has a one-time tip (Demo/tips)
 *   /demo           → /forge/demo, keeping the query
 *   /               → /forge/demo, keeping the query (`?tour=`, `rid`, `utm_*`)
 *   anything else   → /forge/demo, keeping the query
 *
 * Deliberately tiny, and a tree of its own rather than a flag on the forge
 * shell's: there is no /auth, /oauth, /claim or console route here, so nothing
 * on this host can sign in, and therefore nothing can change anything — by
 * construction, beside the read-only layer the console carries
 * (Observability/readOnlyApi.ts). No ActiveOrgBinding (an org call) and no
 * landing-journey handoff: the toast offers the tour.
 */

import { createRootRoute, createRoute, createRouter, Navigate, Outlet, redirect, useRouterState } from '@tanstack/react-router'

import { captureForgeEntry } from './components/ForgeShell/forgeEntry'
import { bootDemoShell } from './components/Forge/Demo/demoShellBoot'
import { DemoTourToast } from './components/Forge/Demo/DemoTourToast'
import { DemoTips } from './components/Forge/Demo/tips'
import { createForgeDemoRedirectRoute, createForgeDemoRoute, FORGE_DEMO_PATH } from './components/Forge/Demo/demoRoute'
import { JourneyTour } from './components/Onboarding/JourneyTour'
import { PageVisitTracker } from './components/PageVisitTracker'
import { ModalLayer } from './components/Modals/ModalLayer'
import { NewBuildPrompt } from './components/NewBuildPrompt'
import { reportCaughtError, RouteErrorFallback } from './components/ErrorBoundary'
import { SurfaceProvider } from './lib/surfaceContext'
import { Toaster } from './lib/toast'

// Before the router replaces the URL: the landing handoff (`rid`, `utm_*`) it arrived with.
const entry = captureForgeEntry()
// Before anything renders the tour: forge copy, memory-only progress, anonymous telemetry.
bootDemoShell()

function DemoRootShell() {
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  return (
    <SurfaceProvider surface="desktop">
      <div data-shell="demo" data-pathname={pathname} className="contents">
        <PageVisitTracker firstVisitProps={entry} />
        <Outlet />
        <ModalLayer />
        <Toaster />
        <NewBuildPrompt />
        <JourneyTour />
        <DemoTips />
        <DemoTourToast />
      </div>
    </SurfaceProvider>
  )
}

export function buildDemoShellTree() {
  const rootRoute = createRootRoute({
    component: DemoRootShell,
    notFoundComponent: () => <Navigate to={FORGE_DEMO_PATH} search={{}} replace />,
    errorComponent: RouteErrorFallback,
  })

  const indexRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/',
    beforeLoad: ({ location }) => {
      throw redirect({ href: `${FORGE_DEMO_PATH}${location.searchStr}`, replace: true })
    },
  })

  // Every other path: a link to a page this host does not have (/auth, /apps,
  // a console URL) lands on the demo rather than a blank page.
  const elsewhereRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '$',
    beforeLoad: ({ location }) => {
      throw redirect({ href: `${FORGE_DEMO_PATH}${location.searchStr}`, replace: true })
    },
  })

  return rootRoute.addChildren([
    indexRoute,
    elsewhereRoute,
    createForgeDemoRoute(() => rootRoute),
    createForgeDemoRedirectRoute(() => rootRoute),
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
