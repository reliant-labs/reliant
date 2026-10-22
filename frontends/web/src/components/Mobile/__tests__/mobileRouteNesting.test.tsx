/**
 * Regression tests for the route-nesting bugs that made the mobile surface
 * unreachable.
 *
 * `mobileRoutes.test.tsx` covers path *shape* with stub components. It could
 * not have caught either bug below, because its miniature tree omits the
 * `_authenticated` / `_mobile` layout parents that change a route's id — which
 * is precisely what broke. These tests reproduce the real nesting.
 */

import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
  useParams,
  useRouterState,
} from '@tanstack/react-router'
import { SurfaceProvider, useSurface } from '../../../lib/surfaceContext'
import { surfaceForPath } from '../../../lib/surface'

/**
 * Reproduces `MobileChatScreen`'s param read under the real nesting depth.
 *
 * The original code passed `from: "/m/chats/$chatId"` — the route's *path*.
 * Under `_authenticated` → `_mobile` the registered id is
 * `/_authenticated/_mobile/m/chats/$chatId`, so the lookup threw
 * "Invariant failed: Could not find an active match" and the error boundary
 * replaced the entire app on every chat open.
 */
function ChatScreenParamProbe() {
  const { chatId } = useParams({ strict: false })
  return <div>chat:{chatId}</div>
}

/** Stands in for a root overlay (OnboardingWizard, ContextualTipsLayer, …). */
function OverlayProbe() {
  const surface = useSurface()
  if (surface !== 'desktop') return null
  return <div data-testid="desktop-overlay">overlay</div>
}

/**
 * Mirrors the real `RootShell`: derives the surface from the router's own
 * location (not `window.location`, which memory-history routing never
 * updates) and wraps BOTH the outlet and the overlays.
 */
function RootShellProbe() {
  const { pathname } = useRouterState({ select: (s) => s.location })
  return (
    <SurfaceProvider surface={surfaceForPath(pathname)}>
      <Outlet />
      <OverlayProbe />
    </SurfaceProvider>
  )
}

function buildTree() {
  const rootRoute = createRootRoute({
    component: RootShellProbe,
  })

  const authenticatedLayoutRoute = createRoute({
    getParentRoute: () => rootRoute,
    id: '_authenticated',
    component: () => <Outlet />,
  })

  const mobileLayoutRoute = createRoute({
    getParentRoute: () => authenticatedLayoutRoute,
    id: '_mobile',
    component: () => <Outlet />,
  })

  const mobileChatRoute = createRoute({
    getParentRoute: () => mobileLayoutRoute,
    path: '/m/chats/$chatId',
    component: ChatScreenParamProbe,
  })

  const mobileAccountRoute = createRoute({
    getParentRoute: () => mobileLayoutRoute,
    path: '/m/account',
    component: () => <div>account-screen</div>,
  })

  const indexRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/',
    component: () => <div>desktop-index</div>,
  })

  return rootRoute.addChildren([
    indexRoute,
    authenticatedLayoutRoute.addChildren([
      mobileLayoutRoute.addChildren([mobileChatRoute, mobileAccountRoute]),
    ]),
  ])
}

function renderAt(path: string) {
  const router = createRouter({
    routeTree: buildTree(),
    history: createMemoryHistory({ initialEntries: [path] }),
  })
  render(<RouterProvider router={router} />)
  return router
}

describe('mobile route nesting', () => {
  it('reads the chatId param through two layout parents', async () => {
    // The exact scenario that crashed: a param read from a route nested under
    // `_authenticated` → `_mobile`.
    renderAt('/m/chats/abc-123')
    expect(await screen.findByText('chat:abc-123')).toBeInTheDocument()
  })

  it('registers the chat route under its full layout-qualified id', () => {
    const router = renderAt('/m/chats/abc-123')
    const ids = Object.keys(router.routesById)

    // The bare path is NOT a valid id — passing it to useParams({ from })
    // is what threw. Pinning both halves documents why `strict: false` is used.
    expect(ids).toContain('/_authenticated/_mobile/m/chats/$chatId')
    expect(ids).not.toContain('/m/chats/$chatId')
  })

  it('renders the account screen under the mobile shell', async () => {
    // Nested under `_mobile` and not a sibling of it, so the screen inherits
    // the surface provider — an account screen outside it would read desktop
    // capabilities and, more importantly, would not get the mobile shell's
    // onboarding gate or project fallback.
    renderAt('/m/account')
    expect(await screen.findByText('account-screen')).toBeInTheDocument()
  })

  it('registers the account route under its full layout-qualified id', () => {
    const router = renderAt('/m/account')
    const ids = Object.keys(router.routesById)
    expect(ids).toContain('/_authenticated/_mobile/m/account')
    expect(ids).not.toContain('/m/account')
  })

  it('suppresses desktop overlays on a mobile route', async () => {
    // The overlays are siblings of <Outlet/>. Before the provider was hoisted
    // to the root they sat above it and read the "desktop" default, so their
    // mobile guards never fired and the onboarding checklist FAB floated over
    // the phone UI intercepting taps.
    renderAt('/m/chats/abc-123')
    // Wait for the route to actually resolve before asserting an absence —
    // otherwise this passes on an empty tree and proves nothing.
    expect(await screen.findByText('chat:abc-123')).toBeInTheDocument()
    expect(screen.queryByTestId('desktop-overlay')).not.toBeInTheDocument()
  })

  it('still renders desktop overlays on a desktop route', async () => {
    renderAt('/')
    expect(await screen.findByTestId('desktop-overlay')).toBeInTheDocument()
  })
})

describe('surfaceForPath', () => {
  // syncProjectUrl (store/projectStore.ts) force-navigates to
  // /project/$projectId and skips an allowlist of paths. `/m/*` was missing
  // from it, so MobileShell's own selectProject call evicted the user into the
  // desktop shell and made every /m/* URL unbookmarkable.
  //
  // The allowlist is inside a module-private function with a router
  // dependency, so it is covered by the store's own tests rather than by
  // asserting on source text here. What this suite pins is the shared
  // predicate both places rely on: which paths count as mobile.
  it('agrees with the routes the mobile tree registers', () => {
    expect(surfaceForPath('/m')).toBe('mobile')
    expect(surfaceForPath('/m/chats')).toBe('mobile')
    expect(surfaceForPath('/m/chats/abc-123')).toBe('mobile')
    expect(surfaceForPath('/project/abc')).toBe('desktop')
    expect(surfaceForPath('/migrate')).toBe('desktop')
  })
})
