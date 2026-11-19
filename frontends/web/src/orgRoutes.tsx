/**
 * The `/o/$org` layout route (control-plane
 * docs/design/unified-shell-and-projects.md §3), as a factory so the route
 * tree (routes.tsx, on both hosts) and its tests mount the SAME definition:
 * its path, its beforeLoad, its pages for every answer.
 *
 *   /o/$org                 beforeLoad: resolveOrgRoute (lib/orgRouting.ts) —
 *                           the org, before any child loads; an org id → the
 *                           slug form; anyone else's or no such org → the one
 *                           "no access" page. Nothing takes effect until the
 *                           navigation commits (lib/routeOrg.ts).
 *     …                     the org's pages (routes.tsx, createOrgScopedRoutes)
 *
 * Always mounted, whatever the flag `web.org_routes` says (D12): an org URL
 * never 404s, and turning the flag off is the rollback.
 *
 * No access, signed out, or a membership list that failed: the layout renders
 * its own page and no child, and every child `beforeLoad` under it is
 * memberOnly (lib/routeOrg.ts), so nothing below can redirect the URL away.
 */

import { type ComponentType, type ReactNode } from 'react'
import { createRoute, Navigate, useRouterState, type AnyRoute } from '@tanstack/react-router'

import { LoadingSpinner } from './components/Layout/LoadingSpinner'
import { OrgLayout } from './components/Orgs/OrgLayout'
import { defaultOrgRoutingDeps, resolveOrgRoute, type OrgRoutingDeps } from './lib/orgRouting'
import { homePath } from './lib/paths'
import { inOrgOfPath } from './lib/paths/org'
import { ORG_ROUTE_ID, type OrgRouteState } from './lib/routeOrg'

export interface OrgLayoutRouteOptions {
  /** The shell's sign-in guard, around the pages the layout renders itself. */
  Guard: ComponentType<{ children: ReactNode }>
  /** What the resolution reads; tests swap it. */
  deps?: OrgRoutingDeps
}

export function createOrgLayoutRoute<TParent extends AnyRoute>(getParentRoute: () => TParent, { Guard, deps = defaultOrgRoutingDeps }: OrgLayoutRouteOptions) {
  const route = createRoute({
    getParentRoute,
    path: ORG_ROUTE_ID,
    beforeLoad: ({ params, location, preload }) => resolveOrgRoute(params.org, location, { preload, deps }),
    pendingComponent: LoadingSpinner,
    component: function OrgLayoutRoute() {
      const { orgRoute } = route.useRouteContext() as { orgRoute: OrgRouteState }
      return <OrgLayout state={orgRoute} Guard={Guard} />
    },
    // A path under the org that this host does not mount: the org's home in a
    // member org; the layout's own page (no access, sign in) otherwise, never
    // a redirect out of it.
    notFoundComponent: function OrgNotFound() {
      const { orgRoute } = route.useRouteContext() as { orgRoute: OrgRouteState }
      const pathname = useRouterState({ select: (s) => s.location.pathname })
      if (orgRoute.kind === 'member') return <Navigate to={inOrgOfPath(pathname, homePath())} replace />
      return <OrgLayout state={orgRoute} Guard={Guard} />
    },
  })
  return route
}
