/**
 * The routes under /settings/connectors, as a factory so the route tree
 * (routes.tsx) and its tests mount the SAME definitions with only the page
 * component swapped. A test that mirrored them by hand would pass against the
 * mirror while the real tree drifted.
 *
 *   /settings/connectors/authorize  The OAuth consent page for third-party MCP
 *                                   clients. Third parties redirect to this
 *                                   exact URL (internal/mcpserver ConsentPath),
 *                                   so its path is a contract and never moves.
 *   /settings/connectors            The retired Connectors settings section.
 *                                   App access now lives on each machine
 *                                   (Settings → Machines → a machine → Access),
 *                                   so a bookmark or old link redirects there
 *                                   instead of 404ing — with `from=connectors`,
 *                                   which Machines turns into a notice saying
 *                                   where Connectors went. `replace`, so Back
 *                                   does not bounce through the dead URL.
 *
 * Both are static paths, so the router ranks them above the generic
 * /settings/$section — which no longer accepts "connectors" at all.
 */

import { createRoute, redirect, type AnyRoute, type RouteComponent } from '@tanstack/react-router'

export interface SettingsConnectorComponents {
  consent: RouteComponent
}

export function createSettingsConnectorRoutes<TParent extends AnyRoute>(
  getParentRoute: () => TParent,
  components: SettingsConnectorComponents,
) {
  // OAuth consent: where a third-party application's user chooses which
  // connector it may act through. Under the authenticated layout, so the page
  // already knows who the user is from the existing session — no new
  // browser-auth path is needed.
  const consentRoute = createRoute({
    getParentRoute,
    path: '/settings/connectors/authorize',
    component: components.consent,
  })

  const retiredSectionRoute = createRoute({
    getParentRoute,
    path: '/settings/connectors',
    beforeLoad: () => {
      throw redirect({ href: '/settings/environments?from=connectors', replace: true })
    },
  })

  return [consentRoute, retiredSectionRoute]
}
