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
 *   /               → the visual build story (/forge/demo?section=built),
 *                     keeping explicit tour/view links and attribution
 *   /forge/p/<other>/… → the same page of shop (the demo's one project)
 *   /forge/p/shop/<no page> → a not-found page
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

import { createRootRoute, createRoute, createRouter, Link, Navigate, Outlet, redirect, useRouterState } from '@tanstack/react-router'

import { captureForgeEntry } from './components/ForgeShell/forgeEntry'
import { bootDemoShell } from './components/Forge/Demo/demoShellBoot'
import { demoEntryHref } from './components/Forge/Demo/demoEntry'
import { DemoTourToast } from './components/Forge/Demo/DemoTourToast'
import { DemoTips } from './components/Forge/Demo/tips'
import { createForgeDemoRedirectRoute, createForgeDemoRoute } from './components/Forge/Demo/demoRoute'
import { consolePath, isObsNavView, withSearch, type ConsolePage } from './components/Forge/Console/paths'
import { FORGE_DEMO_PATH } from './components/Forge/Demo/demoPath'
import { FIXTURE_PROJECT } from './components/Forge/Observability/__fixtures__/fixtureApi'
import { createForgeConsoleRoutes } from './forgeConsoleRoutes'
import { forgeConsolePages } from './forgeConsolePages'
import { DemoConsoleProviders } from './demoBundle/DemoConsoleProviders'
import { retainTourParam } from './components/Onboarding/tourParam'
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

/** `/forge/p/<not shop>/…` as shop's path, or null when it is not one. */
export function otherProjectPath(pathname: string): string | null {
  const match = /^\/forge\/p\/([^/]+)(\/.*)?$/.exec(pathname)
  if (!match || match[1] === FIXTURE_PROJECT) return null
  return `${consolePath(FIXTURE_PROJECT, { kind: 'overview' })}${match[2] ?? ''}`
}

/**
 * A tour link to the demo panel (`/forge/demo?view=traces&tour=…`, how every
 * tour link read before the tour moved onto the console) as the console page
 * it names, or null for any other demo URL. The tour walks the console here.
 */
export function consoleTourHref(pathname: string, search: Record<string, unknown>): string | null {
  if (pathname !== FORGE_DEMO_PATH || !search.tour || search.section || search.browser) return null
  const { view, ...rest } = search
  const page: ConsolePage = typeof view === 'string' && isObsNavView(view) ? { kind: 'obs-view', view } : { kind: 'overview' }
  return withSearch(consolePath(FIXTURE_PROJECT, page), rest)
}

/** Retired console paths a shared link may still carry; they land on the Overview. */
const LEGACY_SHOP_SECTIONS = new Set(['env', 'topology', 'status'])

/** `/forge/p/shop/<something no route serves>`, other than a retired section. */
export function isUnknownShopPage(pathname: string): boolean {
  const prefix = `${DEMO_HOME}/`
  if (!pathname.startsWith(prefix)) return false
  return !LEGACY_SHOP_SECTIONS.has(pathname.slice(prefix.length).split('/')[0])
}

function DemoNotFound() {
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  if (!isUnknownShopPage(pathname)) return <Navigate to={DEMO_HOME} search={{}} replace />
  return (
    <main className="forge-ui flex min-h-dvh items-center justify-center bg-surface-muted p-6" data-testid="demo-not-found">
      <div className="max-w-sm space-y-3 rounded-xl border border-border bg-surface p-6 text-ink">
        <h1 className="text-lg font-semibold">No page here</h1>
        <p className="text-sm text-ink-muted">The sample app has no page at this address.</p>
        <Link to={DEMO_HOME} search={{}} className="inline-flex text-sm font-medium text-primary-ink underline-offset-2 hover:underline">
          Go to the Overview
        </Link>
      </div>
    </main>
  )
}

export function buildDemoShellTree() {
  const rootRoute = createRootRoute({
    component: DemoRootShell,
    // Keep `?tour=` across navigations that don't name it (retainTourParam): the tour walks the console's own links.
    // The root has no search schema, so its middleware type is `{}`-shaped.
    search: { middlewares: [retainTourParam as never] },
    beforeLoad: ({ location }) => {
      const tourHref = consoleTourHref(location.pathname, location.search as Record<string, unknown>)
      if (tourHref) throw redirect({ href: tourHref, replace: true })
    },
    // A console URL with no page (`/forge/p/shop/nope`) says so; anything else lands on the demo.
    notFoundComponent: DemoNotFound,
    errorComponent: RouteErrorFallback,
  })

  const indexRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/',
    beforeLoad: ({ location }) => {
      throw redirect({ href: demoEntryHref(location.searchStr), replace: true })
    },
  })

  // Every other path: a link to a page this host does not have (/auth, /apps,
  // a console URL) lands on the demo rather than a blank page.
  const elsewhereRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '$',
    beforeLoad: ({ location }) => {
      // A shop URL with no page here says so, rather than silently showing the Overview.
      if (isUnknownShopPage(location.pathname)) return
      throw redirect({ href: `${DEMO_HOME}${location.searchStr}`, replace: true })
    },
    component: DemoNotFound,
  })

  // The console, unguarded: the id is the one the shared pages read by. Its
  // pages read the demo's Observability API from context (DemoConsoleProviders).
  const consoleLayoutRoute = createRoute({
    getParentRoute: () => rootRoute,
    id: '_authenticated',
    // The demo has one project: a link to another (`/forge/p/other/releases`) opens shop's same page.
    beforeLoad: ({ location }) => {
      const other = otherProjectPath(location.pathname)
      if (other !== null) throw redirect({ href: `${other}${location.searchStr}`, replace: true })
    },
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
