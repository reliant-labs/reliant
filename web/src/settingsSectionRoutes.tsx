/**
 * The /settings/$section routes, as a factory so the route tree (routes.tsx)
 * and its tests mount the SAME definitions with only the page swapped — the
 * pattern settingsConnectorRoutes.tsx and workflowsAreaRoutes.tsx follow.
 *
 *   /settings/environments/$machineId  One machine's detail view (and its
 *                                      Access card). A real URL, so Back
 *                                      returns to the list, a refresh keeps
 *                                      the machine open, and docs can link it.
 *                                      Static prefix, so the router ranks it
 *                                      above /settings/$section.
 *   /settings/$section                 Any section. A slug that is not a
 *                                      section never reaches the page:
 *                                        - an alias (`machines`, the nav label
 *                                          of the `environments` section)
 *                                          redirects to the real slug;
 *                                        - anything else redirects to bare
 *                                          /settings with `notFound=<slug>`,
 *                                          which the page shows as a notice.
 *                                      Both use `replace`, so Back does not
 *                                      bounce through the bad URL.
 *
 * The old behaviour, parseParams with a throwing enum, did not fall through to
 * the root notFoundComponent: a ZodError from parseParams is a route ERROR, so
 * a mistyped URL crashed the whole app to the error screen with a raw Zod dump.
 */

import { createRoute, redirect, type AnyRoute, type RouteComponent } from '@tanstack/react-router'

import { SETTINGS_SECTION_ALIASES, isSettingsSection, settingsSearchSchema } from './routeSchemas'

export interface SettingsSectionComponents {
  page: RouteComponent
}

export function createSettingsSectionRoutes<TParent extends AnyRoute>(
  getParentRoute: () => TParent,
  components: SettingsSectionComponents,
) {
  const machineDetailRoute = createRoute({
    getParentRoute,
    path: '/settings/environments/$machineId',
    validateSearch: settingsSearchSchema,
    component: components.page,
  })

  const sectionRoute = createRoute({
    getParentRoute,
    path: '/settings/$section',
    // Redirects by `href`, like the sibling factories: a typed `to` cannot be
    // resolved against the registered tree from inside a generic factory.
    beforeLoad: ({ params, location }) => {
      const slug = params.section
      const alias = SETTINGS_SECTION_ALIASES[slug.toLowerCase()]
      if (alias) {
        throw redirect({ href: `/settings/${alias}${location.searchStr}`, replace: true })
      }
      if (!isSettingsSection(slug)) {
        const query = new URLSearchParams({ notFound: slug })
        throw redirect({ href: `/settings?${query}`, replace: true })
      }
      // The pre-path deep link into one machine (`?daemon=<id>`), still sent
      // by older links and bookmarks: move it onto the path.
      const query = new URLSearchParams(location.searchStr)
      const daemon = query.get('daemon')
      if (slug === 'environments' && daemon) {
        query.delete('daemon')
        const rest = query.toString()
        throw redirect({
          href: `/settings/environments/${encodeURIComponent(daemon)}${rest ? `?${rest}` : ''}`,
          replace: true,
        })
      }
    },
    // Billing's sub-tab and Stripe's return marker live here, not in component
    // state — see settingsSearchSchema for why the tab had to become addressable.
    validateSearch: settingsSearchSchema,
    component: components.page,
  })

  return [machineDetailRoute, sectionRoute]
}
