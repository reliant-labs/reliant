/**
 * Smoke tests for the tanstack-router setup defined in `routes.tsx`.
 *
 * We can't import the full route tree from `routes.tsx` (it transitively
 * pulls in App, Monaco, Sentry, gRPC clients, etc. and turns the test into
 * an integration harness). But we DO import the search schemas directly so
 * a schema-drift regression in routes.tsx is caught here without us having
 * to re-mirror anything. The structural pieces — layout-route nesting,
 * redirect target — are mirrored in `makeRouteTree` below; if you change
 * that structure in routes.tsx, mirror the change here.
 *
 * Regressions covered:
 *   1. github_connected is parsed as a boolean (JSON.parse runs on values).
 *   2. useSearch({ from: '/_app' }) works under /project/$projectId because
 *      the layout route owns the schema.
 *   3. The `plan` object round-trips through navigate() without manual
 *      JSON.stringify/encodeURIComponent (no double encoding).
 *   4. /workflow/$workflowName accepts ?drill=attempt as a string.
 *   5. /auth/callback search params round-trip through the OAuth schema.
 *   6. /project (no id) redirects to /.
 */

import { describe, it, expect } from 'vitest'
import {
  createRootRoute,
  createRoute,
  createRouter,
  createMemoryHistory,
  Outlet,
  Navigate,
  RouterProvider,
  useSearch,
  useParams,
} from '@tanstack/react-router'
import { render, screen, waitFor } from '@testing-library/react'
import {
  indexSearchSchema,
  oauthCallbackSearchSchema,
  workflowSearchSchema,
} from './routeSchemas'

// ─── Stub components ─────────────────────────────────────────────────────

function StubLayout() {
  return (
    <div data-testid="layout-shell">
      <Outlet />
    </div>
  )
}

function StubIndex() {
  const search = useSearch({ from: '/_app' })
  return (
    <div data-testid="index">
      <pre data-testid="index-search">{JSON.stringify(search)}</pre>
    </div>
  )
}

function StubProject() {
  // The whole point of regression #2: this must NOT throw under
  // /project/$projectId. Before the layout-route refactor, calling
  // useSearch({ from: '/_app' }) here would fail because the schema lived
  // on '/'.
  const search = useSearch({ from: '/_app' })
  const params = useParams({ from: '/_app/project/$projectId' })
  return (
    <div data-testid="project">
      <span data-testid="project-id">{params.projectId}</span>
      <pre data-testid="project-search">{JSON.stringify(search)}</pre>
    </div>
  )
}

function StubOAuth() {
  const search = useSearch({ from: '/auth/callback' })
  return <pre data-testid="oauth-search">{JSON.stringify(search)}</pre>
}

function StubWorkflowBuilder() {
  const params = useParams({ from: '/workflow/$workflowName' })
  const search = useSearch({ from: '/workflow/$workflowName' })
  return (
    <div data-testid="workflow-builder">
      <span data-testid="workflow-name">{params.workflowName}</span>
      <pre data-testid="workflow-search">{JSON.stringify(search)}</pre>
    </div>
  )
}

// ─── Route tree (mirrors routes.tsx structure) ────────────────────────────

function makeRouteTree() {
  const rootRoute = createRootRoute({
    component: () => <Outlet />,
    notFoundComponent: () => <Navigate to="/" search={{}} />,
  })

  const oauthCallbackRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/auth/callback',
    validateSearch: oauthCallbackSearchSchema,
    component: StubOAuth,
  })

  const appLayoutRoute = createRoute({
    getParentRoute: () => rootRoute,
    id: '_app',
    validateSearch: indexSearchSchema,
    component: StubLayout,
  })

  const indexRoute = createRoute({
    getParentRoute: () => appLayoutRoute,
    path: '/',
    component: StubIndex,
  })

  const projectRoute = createRoute({
    getParentRoute: () => appLayoutRoute,
    path: 'project/$projectId',
    component: StubProject,
  })

  const projectPickerRedirectRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/project',
    component: () => <Navigate to="/" search={{}} />,
  })

  const workflowBuilderRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/workflow/$workflowName',
    validateSearch: workflowSearchSchema,
    component: StubWorkflowBuilder,
  })

  return rootRoute.addChildren([
    oauthCallbackRoute,
    workflowBuilderRoute,
    projectPickerRedirectRoute,
    appLayoutRoute.addChildren([indexRoute, projectRoute]),
  ])
}

function makeRouter(initialEntries: string[]) {
  return createRouter({
    routeTree: makeRouteTree(),
    history: createMemoryHistory({ initialEntries }),
  })
}

// ─── Tests ───────────────────────────────────────────────────────────────

describe('routes: search-schema parsing', () => {
  it('parses github_connected=true as a boolean (regression #1)', async () => {
    // tanstack-router's default parseSearch runs JSON.parse on each value, so
    // ?github_connected=true arrives as boolean `true`. The schema must
    // declare it as z.boolean() — z.string() would reject the parsed value
    // and the user would silently lose the success signal after OAuth.
    const router = makeRouter(['/?github_connected=true'])
    render(<RouterProvider router={router} />)
    await waitFor(() => expect(screen.getByTestId('index-search')).toBeInTheDocument())
    const search = JSON.parse(screen.getByTestId('index-search').textContent || '{}')
    expect(search.github_connected).toBe(true)
    expect(typeof search.github_connected).toBe('boolean')
  })

  it('useSearch({ from: "/_app" }) works under /project/$projectId (regression #2)', async () => {
    // Before the layout-route refactor the schema lived on `/`, so navigating
    // to /project/abc and calling useSearch({ from: '/' }) threw
    // "Could not find an active match from /". Putting the schema on the
    // pathless `_app` layout makes search available to both children.
    const router = makeRouter(['/project/abc?github_connected=true'])
    render(<RouterProvider router={router} />)
    await waitFor(() => expect(screen.getByTestId('project')).toBeInTheDocument())
    expect(screen.getByTestId('project-id').textContent).toBe('abc')
    const search = JSON.parse(screen.getByTestId('project-search').textContent || '{}')
    expect(search.github_connected).toBe(true)
  })

  it('round-trips the `plan` object via navigate() without double-encoding (regression #3)', async () => {
    // Previously useOnboardingPlan ran JSON.stringify(plan) then
    // encodeURIComponent before calling navigate(), and tanstack-router would
    // encode again — yielding a search value that no longer JSON.parsed back
    // to an object. With `plan` typed as an object in the Zod schema,
    // navigate({ search: { plan } }) must round-trip exactly once.
    const router = makeRouter(['/'])
    render(<RouterProvider router={router} />)
    await waitFor(() => expect(screen.getByTestId('index')).toBeInTheDocument())

    const plan = {
      intent: 'build_app' as const,
      compute: 'cloud_free_trial' as const,
      projectName: 'my project / with slashes',
      repo: {
        provider: 'github' as const,
        url: 'https://github.com/owner/repo',
        branch: 'main',
      },
      launchTour: true,
    }

    await router.navigate({ to: '/', search: { plan } })

    await waitFor(() => {
      const search = JSON.parse(
        screen.getByTestId('index-search').textContent || '{}'
      )
      expect(search.plan).toEqual(plan)
    })
  })
})

describe('routes: workflow builder', () => {
  it('decodes $workflowName and reads ?drill=attempt as a string (regression #4)', async () => {
    // The onboarding tour appends ?drill=attempt to auto-drill into a
    // specific node after the builder loads. It must survive validateSearch
    // as a string (JSON.parse fails on the bare word "attempt" so it stays
    // a string — but only if the schema is z.string()).
    const encodedName = encodeURIComponent('builtin://get-it-right')
    const router = makeRouter([`/workflow/${encodedName}?drill=attempt`])
    render(<RouterProvider router={router} />)
    await waitFor(() => expect(screen.getByTestId('workflow-builder')).toBeInTheDocument())
    expect(screen.getByTestId('workflow-name').textContent).toBe('builtin://get-it-right')
    const search = JSON.parse(screen.getByTestId('workflow-search').textContent || '{}')
    expect(search.drill).toBe('attempt')
  })
})

describe('routes: OAuth callback', () => {
  it('parses code, source and returnTo through the callback schema (regression #5)', async () => {
    const router = makeRouter([
      '/auth/callback?code=xyz&source=link&returnTo=%2Fsettings%2Faccount',
    ])
    render(<RouterProvider router={router} />)
    await waitFor(() => expect(screen.getByTestId('oauth-search')).toBeInTheDocument())
    const search = JSON.parse(screen.getByTestId('oauth-search').textContent || '{}')
    expect(search).toMatchObject({
      code: 'xyz',
      source: 'link',
      returnTo: '/settings/account',
    })
  })

  it('rejects an invalid `source` enum value', () => {
    // Smoke check that the enum is actually enforced — if someone widens it
    // to z.string() this test will pass silently and an invalid value will
    // sneak through.
    expect(() =>
      oauthCallbackSearchSchema.parse({ source: 'not-a-real-source' })
    ).toThrow()
  })
})

describe('routes: /project redirect', () => {
  it('navigates from /project to / (regression #6)', async () => {
    const router = makeRouter(['/project'])
    render(<RouterProvider router={router} />)
    // <Navigate /> renders, then redirects; the index view should appear.
    await waitFor(() =>
      expect(screen.getByTestId('index')).toBeInTheDocument()
    )
    expect(router.state.location.pathname).toBe('/')
  })
})
