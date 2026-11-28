/**
 * The public demo's Agents and Inbox (demoShellRoutes.tsx): the app's own
 * pages for chats, the Inbox, Workflows (library and runs) and Connectors,
 * mounted under the demo shell's unguarded `_authenticated` layout and
 * answered by the shop's app layer (src/fixtures/shop/app.ts) over the demo
 * transport, which refuses writes.
 *
 *   /project/$projectId          a project's chats (the Agents drill-in's first page)
 *   /c/$chatId[/t/$threadId]     a chat's transcript, or one thread of it
 *   /new                         the new-chat view (sending is refused: read-only)
 *   /inbox                       everything waiting on someone
 *   /workflows/…                 the Workflows area: library, a workflow, runs, a run
 *   /connectors                  integrations and MCP servers
 *   /settings/machines[/$machineId]
 *                                Agent sandboxes: the machines list and one
 *                                machine (Settings' machines section, mounted
 *                                alone: the rest of the Settings page is the
 *                                account's, and not in the demo)
 *   /machines                    the app's alias for Agent sandboxes
 *
 * The same paths and pages as routes.tsx's org-less tree (createOrgScopedRoutes),
 * each page a lazy chunk: the demo's first page never downloads the chat
 * client. `/` stays the demo's landing (the chat home is the project's chats,
 * which is where the demo's nav sends Agents). No /o/$org tree: the demo has
 * one org, the shop's.
 */

import { createRoute, type AnyRoute } from '@tanstack/react-router'

import { ShellPageHeader } from './components/Layout/shell/shellFrame'
import { lazyRoute } from './lib/lazyRoute'
import { chatViewSearchSchema, newChatSearchSchema, settingsSearchSchema } from './routeSchemas'
import { createSandboxesAliasRoute } from './sandboxesAliasRoute'
import { createWorkflowsAreaRoutes } from './workflowsAreaRoutes'

const App = lazyRoute(() => import('./App'), 'default')
const InboxPage = lazyRoute(() => import('./components/inbox/InboxPage'), 'InboxPage')
const ConnectorsPage = lazyRoute(() => import('./components/Connectors/ConnectorsPage'), 'ConnectorsPage')
const WorkflowsLayout = lazyRoute(() => import('./components/workflows/WorkflowsShell'), 'WorkflowsLayout')
const WorkflowsListPage = lazyRoute(() => import('./components/workflows/list/WorkflowsListPage'), 'WorkflowsListPage')
const WorkflowDetailPage = lazyRoute(() => import('./components/workflows/detail/WorkflowDetailPage'), 'WorkflowDetailPage')
const RunsPage = lazyRoute(() => import('./components/runs/RunsPage'), 'RunsPage')
const RunDetailPage = lazyRoute(() => import('./components/runs/RunDetailPage'), 'RunDetailPage')
const AutomationRedirectPage = lazyRoute(() => import('./components/workflows/AutomationRedirectPage'), 'AutomationRedirectPage')
const MachinesSection = lazyRoute(() => import('./components/Settings/cloud/machines'), 'MachinesSection')

/** Agent sandboxes in the frame Settings draws its sections in (SettingsContent.tsx SettingsLayout). */
function SandboxesPage() {
  return (
    <div className="flex h-full min-h-0 flex-col bg-background">
      <ShellPageHeader>{null}</ShellPageHeader>
      <div className="overflow-auto px-4 py-6 sm:px-6 lg:px-8 lg:py-8">
        <div className="mx-auto w-full max-w-4xl" data-testid="demo-sandboxes">
          <MachinesSection />
        </div>
      </div>
    </div>
  )
}

export function createDemoAgentsRoutes<TParent extends AnyRoute>(getParentRoute: () => TParent) {
  const projectRoute = createRoute({ getParentRoute, path: 'project/$projectId', component: App })
  const chatRoute = createRoute({ getParentRoute, path: 'c/$chatId', validateSearch: chatViewSearchSchema, component: App })
  const threadRoute = createRoute({ getParentRoute, path: 'c/$chatId/t/$threadId', validateSearch: chatViewSearchSchema, component: App })
  const newChatRoute = createRoute({ getParentRoute, path: 'new', validateSearch: newChatSearchSchema, component: App })
  const inboxRoute = createRoute({ getParentRoute, path: '/inbox', component: InboxPage })
  const connectorsRoute = createRoute({ getParentRoute, path: '/connectors', component: ConnectorsPage })
  // The section reads `machineId` and `?daemon=`/`?from=` loosely (strict: false), as under Settings' routes.
  const sandboxesRoute = createRoute({ getParentRoute, path: '/settings/machines', validateSearch: settingsSearchSchema, component: SandboxesPage })
  const sandboxRoute = createRoute({ getParentRoute, path: '/settings/machines/$machineId', validateSearch: settingsSearchSchema, component: SandboxesPage })
  const workflowsAreaRoutes = createWorkflowsAreaRoutes(
    getParentRoute,
    {
      layout: WorkflowsLayout,
      library: WorkflowsListPage,
      workflowDetail: WorkflowDetailPage,
      runs: RunsPage,
      runDetail: RunDetailPage,
      automationRedirect: AutomationRedirectPage,
    },
    { retiredPaths: false },
  )
  return [
    projectRoute,
    chatRoute,
    threadRoute,
    newChatRoute,
    inboxRoute,
    connectorsRoute,
    sandboxesRoute,
    sandboxRoute,
    createSandboxesAliasRoute(getParentRoute),
    ...workflowsAreaRoutes,
  ] as const
}
