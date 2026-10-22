/**
 * Route shape for the two screens added in mobile iteration 1b — `/m/new` and
 * `/m/daemons`.
 *
 * Same miniature-route-tree approach as `mobileRoutes.test.tsx`: real paths,
 * stub components, so tanstack-router's matching is exercised without the
 * app's dependency graph. What's protected is that these screens nest under
 * the shell (they'd silently get DESKTOP capabilities otherwise) and that
 * `/m/new` did not capture `/m/chats/$chatId`.
 */

import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
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
function StubMobileNewChat() {
  return <div>mobile-new-chat</div>
}
function StubMobileDaemonList() {
  return <div>mobile-daemon-list</div>
}
function StubMobileDaemonScreen() {
  const { daemonId } = useParams({ strict: false })
  return <div>mobile-daemon:{daemonId}</div>
}
function StubMobileGitHubScreen() {
  return <div>mobile-github</div>
}

function buildRouteTree() {
  const rootRoute = createRootRoute({
    component: () => <Outlet />,
    notFoundComponent: () => <div>not-found</div>,
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

  const mobileNewChatRoute = createRoute({
    getParentRoute: () => mobileLayoutRoute,
    path: '/m/new',
    component: StubMobileNewChat,
  })

  const mobileDaemonsRoute = createRoute({
    getParentRoute: () => mobileLayoutRoute,
    path: '/m/daemons',
    component: StubMobileDaemonList,
  })

  const mobileDaemonRoute = createRoute({
    getParentRoute: () => mobileLayoutRoute,
    path: '/m/daemons/$daemonId',
    component: StubMobileDaemonScreen,
  })

  const mobileGitHubRoute = createRoute({
    getParentRoute: () => mobileLayoutRoute,
    path: '/m/github',
    component: StubMobileGitHubScreen,
  })

  return rootRoute.addChildren([
    mobileIndexRoute,
    mobileLayoutRoute.addChildren([
      mobileChatsRoute,
      mobileChatRoute,
      mobileNewChatRoute,
      mobileDaemonsRoute,
      mobileDaemonRoute,
      mobileGitHubRoute,
    ]),
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

describe('mobile new-chat route', () => {
  it('renders the new-chat screen at /m/new', async () => {
    renderAt('/m/new')
    expect(await screen.findByText('mobile-new-chat')).toBeInTheDocument()
  })

  it('renders it inside the mobile shell', async () => {
    // Outside the shell there is no SurfaceProvider, so the screen would read
    // desktop capabilities — attachments and workflow params become legal.
    renderAt('/m/new')
    expect(await screen.findByTestId('mobile-shell')).toBeInTheDocument()
  })

  it('does not shadow a chat id that happens to be "new"', async () => {
    // `/m/new` and `/m/chats/$chatId` live on different prefixes precisely so
    // this can't collide.
    renderAt('/m/chats/new')
    expect(await screen.findByText('mobile-chat:new')).toBeInTheDocument()
  })
})

describe('mobile daemon routes', () => {
  it('renders the daemon list at /m/daemons', async () => {
    renderAt('/m/daemons')
    expect(await screen.findByText('mobile-daemon-list')).toBeInTheDocument()
  })

  it('renders the daemon list inside the mobile shell', async () => {
    renderAt('/m/daemons')
    expect(await screen.findByTestId('mobile-shell')).toBeInTheDocument()
  })

  it('passes the daemonId param through to the detail screen', async () => {
    renderAt('/m/daemons/dm-42')
    expect(await screen.findByText('mobile-daemon:dm-42')).toBeInTheDocument()
  })

  it('renders the daemon detail screen inside the mobile shell too', async () => {
    renderAt('/m/daemons/dm-42')
    expect(await screen.findByTestId('mobile-shell')).toBeInTheDocument()
  })
})

describe('mobile github route', () => {
  it('renders GitHub management at /m/github', async () => {
    renderAt('/m/github')
    expect(await screen.findByText('mobile-github')).toBeInTheDocument()
  })

  it('renders GitHub management inside the mobile shell', async () => {
    renderAt('/m/github')
    expect(await screen.findByTestId('mobile-shell')).toBeInTheDocument()
  })
})
