/**
 * The `/o/$org` layout's PAGES (orgRoutes.tsx), rendered: the layout over
 * stub children and a fake membership list. The org resolves before a page
 * renders, an id becomes the slug, a stranger's org is the one "no access"
 * page (and so is an unknown path under it), and requests name the route's
 * org. Where URLs land on the production trees of both hosts is
 * __tests__/orgRoutes.production.test.ts.
 */

import { act, render, screen, waitFor } from '@testing-library/react'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
  type AnyRouter,
} from '@tanstack/react-router'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { requestOrgId } from './api/orgInterceptor'
import { createOrgLayoutRoute } from './orgRoutes'
import { routeOrgScopeHooks, useRouteOrgScope } from './lib/routeOrg'
import type { OrgRoutingDeps } from './lib/orgRouting'
import type { MyOrg } from './services/controlPlane/orgs'
import { useActiveOrgStore } from './store/activeOrgStore'

const org = (id: string, slug: string, name: string): MyOrg => ({ id, slug, name, role: 'owner', isOwner: true, isDefault: false })
const deps: OrgRoutingDeps = {
  signedInUser: async () => 'alice',
  hasOrgs: () => true,
  listOrgs: async () => ({ orgs: [org('org-a', 'acme', 'Acme'), org('org-b', 'side', 'Side Project')], defaultOrgId: 'org-a' }),
}

const PassThrough = ({ children }: { children: ReactNode }) => <>{children}</>

/** Every page names the org requests would carry while it is on screen. */
function Page({ name }: { name: string }) {
  return <div data-testid="page">{`${name} in ${requestOrgId()}`}</div>
}

function reliantTree() {
  const root = createRootRoute({ component: () => <Outlet />, ...routeOrgScopeHooks })
  const orgLayout = createOrgLayoutRoute(() => root, { Guard: PassThrough, deps })
  const authenticated = createRoute({ getParentRoute: () => orgLayout, id: '_authenticated', component: () => <Outlet /> })
  const app = createRoute({ getParentRoute: () => orgLayout, id: '_app', component: () => <Outlet /> })
  const home = createRoute({ getParentRoute: () => app, path: '/', component: () => <Page name="home" /> })
  const inbox = createRoute({ getParentRoute: () => authenticated, path: '/inbox', component: () => <Page name="inbox" /> })
  const legacyInbox = createRoute({ getParentRoute: () => root, path: '/inbox', component: () => <Page name="legacy inbox" /> })
  return root.addChildren([legacyInbox, orgLayout.addChildren([authenticated.addChildren([inbox]), app.addChildren([home])])])
}

let router: AnyRouter | null = null

function open(tree: ReturnType<typeof reliantTree>, url: string) {
  router = createRouter({ routeTree: tree, history: createMemoryHistory({ initialEntries: [url] }) })
  ;(globalThis as { __RELIANT_ROUTER?: unknown }).__RELIANT_ROUTER = router
  render(<RouterProvider router={router} />)
  return router
}

beforeEach(() => {
  sessionStorage.clear()
  localStorage.clear()
  useActiveOrgStore.setState({ userId: null, chosenId: null, effectiveId: null, resolved: false, failure: null, segments: {} })
  useRouteOrgScope.setState({ scope: { kind: 'legacy' } })
})

afterEach(() => {
  delete (globalThis as { __RELIANT_ROUTER?: unknown }).__RELIANT_ROUTER
  router = null
})

describe('the /o/$org layout', () => {
  it('resolves the org before a page renders, and requests name it', async () => {
    open(reliantTree(), '/o/side/inbox')
    expect(await screen.findByTestId('page')).toHaveTextContent('inbox in org-b')
    expect(useActiveOrgStore.getState().chosenId).toBe('org-b')
  })

  it("opens the org's home at /o/<slug>", async () => {
    open(reliantTree(), '/o/acme')
    expect(await screen.findByTestId('page')).toHaveTextContent('home in org-a')
  })

  it('redirects an org id to the slug form', async () => {
    const r = open(reliantTree(), '/o/org-b/inbox?x=1')
    expect(await screen.findByTestId('page')).toHaveTextContent('inbox in org-b')
    expect(r.state.location.href).toBe('/o/side/inbox?x=1')
  })

  it("shows a stranger's org (or no org) as the one no-access page, with the person's own orgs", async () => {
    open(reliantTree(), '/o/not-mine/inbox')
    expect(await screen.findByTestId('org-access-denied')).toHaveTextContent("You don't have access to this organization")
    expect(screen.queryByTestId('page')).toBeNull()
    expect(screen.getByTestId('org-access-denied-acme')).toHaveAttribute('href', '/o/acme')
    expect(screen.getByTestId('org-access-denied-side')).toHaveTextContent('Side Project')
    // Nothing moved the tab.
    expect(useActiveOrgStore.getState().chosenId).toBeNull()
  })

  it('moves requests with the route when the person goes to another org', async () => {
    const r = open(reliantTree(), '/o/acme/inbox')
    expect(await screen.findByTestId('page')).toHaveTextContent('inbox in org-a')
    await act(() => r.navigate({ href: '/o/side/inbox' }))
    // Read when a request goes out, not when the page rendered.
    await waitFor(() => expect(requestOrgId()).toBe('org-b'))
    expect(r.state.location.pathname).toBe('/o/side/inbox')
    expect(useActiveOrgStore.getState().chosenId).toBe('org-b')
  })

  it('leaves the org-less page to the tab org (the mirror)', async () => {
    useActiveOrgStore.setState({ effectiveId: 'org-a' })
    open(reliantTree(), '/inbox')
    expect(await screen.findByTestId('page')).toHaveTextContent('legacy inbox in org-a')
  })

  it("shows an unknown path under a stranger's org as the no-access page, not a redirect", async () => {
    const r = open(reliantTree(), '/o/not-mine/nowhere')
    expect(await screen.findByTestId('org-access-denied')).toBeInTheDocument()
    expect(r.state.location.pathname).toBe('/o/not-mine/nowhere')
  })

  it("sends an unknown path under a member's org to that org's home", async () => {
    const r = open(reliantTree(), '/o/acme/nowhere')
    expect(await screen.findByTestId('page')).toHaveTextContent('home in org-a')
    expect(r.state.location.pathname).toBe('/o/acme')
  })
})
