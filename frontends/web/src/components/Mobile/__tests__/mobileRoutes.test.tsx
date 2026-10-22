/**
 * Route-shape tests for the mobile surface.
 *
 * Mirrors the approach in `routes.test.tsx`: build a miniature route tree with
 * the same *paths* as production and stub components, so we exercise
 * tanstack-router's matching without dragging in the app's full dependency
 * graph (gRPC clients, Monaco, stores).
 *
 * What's protected here is path *shape* — the `/m` alias, the nested
 * `$chatId` param, and (most importantly) that adding `/m` did not capture
 * desktop routes that merely start with the letter m.
 */

import { describe, expect, it } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Navigate,
  Outlet,
  RouterProvider,
  useParams,
} from '@tanstack/react-router'

function StubIndex() {
  return <div>desktop-index</div>
}
function StubMigrate() {
  return <div>desktop-migrate</div>
}
function StubMobileShell() {
  return (
    <div data-testid="mobile-shell">
      <Outlet />
    </div>
  )
}
function StubMobileChatList() {
  return <div>mobile-chat-list</div>
}
function StubMobileChatScreen() {
  const { chatId } = useParams({ strict: false })
  return <div>mobile-chat:{chatId}</div>
}

function buildRouteTree() {
  const rootRoute = createRootRoute({
    component: () => <Outlet />,
    notFoundComponent: () => <div>not-found</div>,
  })

  const indexRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/',
    component: StubIndex,
  })

  // A desktop route beginning with "/m" — the trap a naive prefix match falls into.
  const migrateRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/migrate',
    component: StubMigrate,
  })

  const mobileIndexRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/m',
    component: () => <Navigate to="/m/chats" />,
  })

  const mobileLayoutRoute = createRoute({
    getParentRoute: () => rootRoute,
    id: '_mobile',
    component: StubMobileShell,
  })

  const mobileChatsRoute = createRoute({
    getParentRoute: () => mobileLayoutRoute,
    path: '/m/chats',
    component: StubMobileChatList,
  })

  const mobileChatRoute = createRoute({
    getParentRoute: () => mobileLayoutRoute,
    path: '/m/chats/$chatId',
    component: StubMobileChatScreen,
  })

  return rootRoute.addChildren([
    indexRoute,
    migrateRoute,
    mobileIndexRoute,
    mobileLayoutRoute.addChildren([mobileChatsRoute, mobileChatRoute]),
  ])
}

function renderAt(path: string) {
  const router = createRouter({
    routeTree: buildRouteTree(),
    history: createMemoryHistory({ initialEntries: [path] }),
  })
  render(<RouterProvider router={router} />)
  return router
}

describe('mobile routes', () => {
  it('renders the chat list at /m/chats', async () => {
    renderAt('/m/chats')
    expect(await screen.findByText('mobile-chat-list')).toBeInTheDocument()
  })

  it('renders the chat list inside the mobile shell', async () => {
    // The shell owns SurfaceProvider; a chat screen rendered outside it would
    // silently get desktop capabilities and expanded tool calls.
    renderAt('/m/chats')
    expect(await screen.findByTestId('mobile-shell')).toBeInTheDocument()
  })

  it('passes the chatId param through to the chat screen', async () => {
    renderAt('/m/chats/abc-123')
    expect(await screen.findByText('mobile-chat:abc-123')).toBeInTheDocument()
  })

  it('renders the chat screen inside the mobile shell too', async () => {
    renderAt('/m/chats/abc-123')
    expect(await screen.findByTestId('mobile-shell')).toBeInTheDocument()
  })

  it('redirects /m to the chat list', async () => {
    renderAt('/m')
    expect(await screen.findByText('mobile-chat-list')).toBeInTheDocument()
  })

  it('does not capture desktop routes that start with the letter m', async () => {
    // `/migrate` must stay desktop. This is the concrete failure mode of
    // matching on a bare "/m" prefix instead of a path segment.
    renderAt('/migrate')
    expect(await screen.findByText('desktop-migrate')).toBeInTheDocument()
    expect(screen.queryByTestId('mobile-shell')).not.toBeInTheDocument()
  })

  it('leaves the desktop index alone', async () => {
    renderAt('/')
    expect(await screen.findByText('desktop-index')).toBeInTheDocument()
    await waitFor(() => {
      expect(screen.queryByTestId('mobile-shell')).not.toBeInTheDocument()
    })
  })
})
