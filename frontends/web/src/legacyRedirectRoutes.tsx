/**
 * The retired pages' routes (lib/paths/legacy.ts RETIRED_PAGES), as a factory
 * so the route tree (routes.tsx) and its tests mount the same definitions.
 * Each is a route that only redirects: it renders nothing, and a link,
 * bookmark or older Electron build that names the old path lands on the page
 * that replaced it instead of a not-found.
 *
 * In the request's own form: `/apps` → `/projects?show=apps`, and
 * `/o/<org>/apps` → `/o/<org>/projects?show=apps`. The request's query is
 * kept (under the target's own keys) and so is the hash. `replace`, so Back
 * does not bounce through the dead URL. Under the org tree a redirect runs only
 * in a member org (memberOnly): someone else's org shows its no-access page in
 * place.
 */

import { createRoute, redirect, type AnyRoute } from '@tanstack/react-router'

import { RETIRED_PAGES, type RetiredPage } from '@/lib/paths/legacy'
import { inOrgOfPath } from '@/lib/paths/org'
import { withSearch } from '@/lib/paths'
import { memberOnly } from '@/lib/routeOrg'

/** The retired pages of one scope, each as a redirect-only route under `getParentRoute`. */
export function createRetiredPageRoutes<TParent extends AnyRoute>(getParentRoute: () => TParent, scope: RetiredPage['scope']) {
  return RETIRED_PAGES.filter((page) => page.scope === scope).map((page) =>
    createRoute({
      getParentRoute,
      path: page.from,
      beforeLoad: memberOnly(({ location }) => {
        const search = { ...(location.search as Record<string, unknown>), ...page.search }
        const hash = location.hash ? `#${location.hash}` : ''
        throw redirect({ href: `${withSearch(inOrgOfPath(location.pathname, page.to()), search)}${hash}`, replace: true })
      }),
    }),
  )
}
