/**
 * The `/o/$org` layout route (control-plane
 * docs/design/unified-shell-and-projects.md §3), as a factory so the route
 * trees (routes.tsx, forgeShellRoutes.tsx) and their tests mount the SAME
 * definition: its path, its beforeLoad, its pages for every answer.
 *
 *   /o/$org                 beforeLoad: resolveOrgRoute (lib/orgRouting.ts) —
 *                           the org, before any child renders; an org id →
 *                           the slug form; anyone else's or no such org → the
 *                           one "no access" page
 *     …                     the org's pages (routes.tsx mounts them; the
 *                           forge shell mounts createOrgEntryRoutes instead)
 *
 * Always mounted, whatever the flag `web.org_routes` says (D12): an org URL
 * never 404s, and turning the flag off is the rollback.
 */

import { type ComponentType, type ReactNode } from 'react'
import { createRoute, redirect, type AnyRoute } from '@tanstack/react-router'

import { LoadingSpinner } from './components/Layout/LoadingSpinner'
import { OrgLayout } from './components/Orgs/OrgLayout'
import { defaultOrgRoutingDeps, resolveOrgRoute, type OrgRoutingDeps } from './lib/orgRouting'
import { orgFreePath } from './lib/paths/org'
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
  })
  return route
}

/**
 * The forge shell's org routes until the one shell (U6) mounts its pages under
 * the org: an org URL resolves the org like anywhere else (a member's org
 * becomes the tab's), then opens the org-less page it names. Not a member, or
 * signed out: the layout's own pages, as in Reliant.
 */
export function createOrgEntryRoutes<TParent extends AnyRoute>(getParentRoute: () => TParent, options: OrgLayoutRouteOptions) {
  const layoutRoute = createOrgLayoutRoute(getParentRoute, options)
  const toOrgFreePage = ({ context, location }: { context: unknown; location: { pathname: string; searchStr: string; hash: string } }) => {
    if ((context as { orgRoute?: OrgRouteState }).orgRoute?.kind !== 'member') return
    throw redirect({ href: `${orgFreePath(location.pathname)}${location.searchStr}${location.hash ? `#${location.hash}` : ''}`, replace: true })
  }
  const indexRoute = createRoute({ getParentRoute: () => layoutRoute, path: '/', beforeLoad: toOrgFreePage })
  const restRoute = createRoute({ getParentRoute: () => layoutRoute, path: '$', beforeLoad: toOrgFreePage })
  return layoutRoute.addChildren([indexRoute, restRoute])
}
