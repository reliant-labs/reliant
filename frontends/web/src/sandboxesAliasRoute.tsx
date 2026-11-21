/**
 * `/machines` (and `/o/<org>/machines`): an alias for Agent sandboxes, the
 * machines page in Settings (lib/paths sandboxesPath). Machines were renamed
 * Agent sandboxes in the UI (U6c); the old noun keeps a short URL that opens
 * the page. Settings is not org-scoped yet, so both forms land on the same
 * org-less `/settings/machines`, with the request's query and hash kept.
 * `replace`, so Back does not bounce through the alias. Under the org tree it
 * redirects only in a member org (memberOnly), like the retired pages'.
 */

import { createRoute, redirect, type AnyRoute } from '@tanstack/react-router'

import { sandboxesPath, withSearch } from '@/lib/paths'
import { memberOnly } from '@/lib/routeOrg'

export function createSandboxesAliasRoute<TParent extends AnyRoute>(getParentRoute: () => TParent) {
  return createRoute({
    getParentRoute,
    path: '/machines',
    beforeLoad: memberOnly(({ location }) => {
      const hash = location.hash ? `#${location.hash}` : ''
      throw redirect({ href: `${withSearch(sandboxesPath(), location.search as Record<string, unknown>)}${hash}`, replace: true })
    }),
  })
}
