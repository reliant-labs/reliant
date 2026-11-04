/**
 * The Workflows area's routes (research/WORKFLOW_UI.md §1.2–1.3), as a
 * factory so the route tree (routes.tsx) and its tests mount the SAME
 * definitions — paths, search schemas, the `/workflows` index redirect and
 * the retired-path redirects — with only the page components swapped. A test
 * that mirrored the tree by hand would pass against the mirror while the real
 * tree drifted.
 *
 * One area, two tabs — Workflows (every definition, with its triggers inline)
 * and Runs (every execution) — inside one layout, which owns the tab bar,
 * the exit, the project scope and project resolution (a `project` search
 * param on every page, so a hard refresh works: the area never mounts
 * ModernApp). An automation is a workflow's trigger, configured on the
 * workflow's page; the Automations tab's paths are redirects.
 *
 * Under `_authenticated`, not the app shell: the area spans projects
 * (decision 10), and the app shell is scoped to one. Pathless like `_forge`,
 * so every child carries its full path and `to: "."` resolves to the page.
 */

import { createRoute, redirect, type AnyRoute, type RouteComponent } from '@tanstack/react-router'

import { ON_WORKFLOWS_HREF, legacyWorkflowsPath } from './lib/workflowsArea'
import { librarySearchSchema, runsSearchSchema, workflowsAreaSearchSchema } from './routeSchemas'

export interface WorkflowsAreaComponents {
  layout: RouteComponent
  library: RouteComponent
  workflowDetail: RouteComponent
  runs: RouteComponent
  runDetail: RouteComponent
  /** Resolves a retired automation link: looks the trigger up, opens its workflow's page. */
  automationRedirect: RouteComponent
}

/** `target`, with the request's own search params added to any it already has. */
function withSearch(target: string, searchStr: string): string {
  if (!searchStr) return target
  if (!target.includes('?')) return `${target}${searchStr}`
  return `${target}&${searchStr.replace(/^\?/, '')}`
}

type LegacyPath = '/workflow' | '/runs' | '/runs/$runId' | '/automations' | '/automations/$triggerId'

export function createWorkflowsAreaRoutes<TParent extends AnyRoute>(
  getParentRoute: () => TParent,
  components: WorkflowsAreaComponents,
) {
  const layoutRoute = createRoute({
    getParentRoute,
    id: '_workflows',
    component: components.layout,
  })

  const indexRoute = createRoute({
    getParentRoute: () => layoutRoute,
    path: '/workflows',
    validateSearch: workflowsAreaSearchSchema,
    // The area's landing tab. The search string (`?project=`) rides along.
    beforeLoad: ({ location }) => {
      throw redirect({ href: `/workflows/library${location.searchStr}`, replace: true })
    },
  })

  // Search, source and sort are search params (librarySearchSchema).
  const libraryRoute = createRoute({
    getParentRoute: () => layoutRoute,
    path: '/workflows/library',
    validateSearch: librarySearchSchema,
    component: components.library,
  })

  // $workflowRef is the stored ref — `builtin://agent`, a project or user
  // workflow's name — URL-encoded by the router, as for the builder.
  const workflowDetailRoute = createRoute({
    getParentRoute: () => layoutRoute,
    path: '/workflows/library/$workflowRef',
    validateSearch: workflowsAreaSearchSchema,
    component: components.workflowDetail,
  })

  // Runs: every execution, whatever started it (§4–5). Filters live in the
  // search params; $runId is the run's chat id.
  const runsRoute = createRoute({
    getParentRoute: () => layoutRoute,
    path: '/workflows/runs',
    validateSearch: runsSearchSchema,
    component: components.runs,
  })

  const runDetailRoute = createRoute({
    getParentRoute: () => layoutRoute,
    path: '/workflows/runs/$runId',
    validateSearch: workflowsAreaSearchSchema,
    component: components.runDetail,
  })

  // Retired: the Automations tab. Its list was the Workflows tab narrowed to
  // what is on, in every project; an automation's page is its trigger's row
  // on the workflow page, which only the trigger can name, so that redirect
  // is a component that looks it up.
  const automationsRoute = createRoute({
    getParentRoute: () => layoutRoute,
    path: '/workflows/automations',
    beforeLoad: ({ location }) => {
      throw redirect({ href: withSearch(ON_WORKFLOWS_HREF, location.searchStr), replace: true })
    },
  })

  const automationRedirectRoute = createRoute({
    getParentRoute: () => layoutRoute,
    path: '/workflows/automations/$triggerId',
    validateSearch: workflowsAreaSearchSchema,
    component: components.automationRedirect,
  })

  // ── Retired paths: redirects, so bookmarks, notifications and old links land
  //
  //   /workflow              → /workflows/library   (the hub)
  //   /runs[/$runId]         → /workflows/runs[/$runId]
  //   /automations           → the Workflows tab, narrowed to what is on
  //   /automations/$id       → /workflows/automations/$id (which resolves it)
  //
  // The search string is carried VERBATIM (a relative href, which the router
  // re-parses), so a filtered /runs?state=... link keeps every filter.
  // `replace`, so Back does not bounce through the dead URL. The mapping lives
  // in lib/workflowsArea.ts; that module and this one are the only places the
  // old paths may be spelled (workflowsAreaLinks.test.ts).
  //
  // /workflow is a sibling of the builder's /workflow/new and
  // /workflow/$workflowName, so it matches the bare path only.
  const legacyRoute = (path: LegacyPath) =>
    createRoute({
      getParentRoute,
      path,
      beforeLoad: ({ location }) => {
        const target = legacyWorkflowsPath(location.pathname)
        if (!target) return
        const hash = location.hash ? `#${location.hash}` : ''
        throw redirect({ href: `${withSearch(target, location.searchStr)}${hash}`, replace: true })
      },
    })

  return [
    layoutRoute.addChildren([
      indexRoute,
      libraryRoute,
      workflowDetailRoute,
      runsRoute,
      runDetailRoute,
      automationsRoute,
      automationRedirectRoute,
    ]),
    legacyRoute('/workflow'),
    legacyRoute('/runs'),
    legacyRoute('/runs/$runId'),
    legacyRoute('/automations'),
    legacyRoute('/automations/$triggerId'),
  ] as const
}
