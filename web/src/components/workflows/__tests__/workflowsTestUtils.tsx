// Copyright (c) 2025 Reliant Labs

/**
 * Shared harness for the Workflows-area page tests: a memory router holding
 * every route the pages link to, and proto fixtures shaped as the RPCs
 * return them so the real client-side mapping runs.
 */

import type { ReactNode } from "react";
import { create } from "@bufbuild/protobuf";
import { render } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router";

import { runsSearchSchema, workflowsAreaSearchSchema } from "@/routeSchemas";
import { ChatActivity, WorkflowState, WorkflowStopReason } from "@/gen/reliant/v1/chat_pb";
import { RunDisplayState, RunSchema } from "@/gen/reliant/v1/run_pb";
import { WorkflowDraftStatus } from "@/gen/reliant/v1/workflow_pb";
import {
  ScheduleSourceSchema,
  TriggerHealthSchema,
  TriggerHealthStatus,
  TriggerSchema,
} from "@/gen/reliant/v1/trigger_pb";

/** Render `ui` as the page at `path`; `pattern` is the route path it mounts on. */
export function renderWorkflowsPage(ui: ReactNode, path: string, pattern: string) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } },
  });
  const rootRoute = createRootRoute({ component: () => <Outlet /> });
  const linkTargets = [
    "/",
    "/workflow/new",
    "/workflow/$workflowName",
    "/workflows/library",
    "/workflows/library/$workflowRef",
    "/workflows/runs",
    "/workflows/runs/$runId",
    "/workflows/automations",
    "/workflows/automations/$triggerId",
    "/project/$projectId",
  ].filter((target) => target !== pattern);
  const routes = [
    createRoute({
      getParentRoute: () => rootRoute,
      path: pattern,
      validateSearch: pattern === "/workflows/runs" ? runsSearchSchema : workflowsAreaSearchSchema,
      component: () => <>{ui}</>,
    }),
    ...linkTargets.map((target) =>
      createRoute({
        getParentRoute: () => rootRoute,
        path: target,
        validateSearch: target === "/workflows/runs" ? runsSearchSchema : undefined,
        component: () => <div data-testid={`at-${target}`} />,
      }),
    ),
  ];
  const router = createRouter({
    routeTree: rootRoute.addChildren(routes),
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  const result = render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return { ...result, router, queryClient };
}

export function protoRun(workflowName: string, chatId: string, displayState = RunDisplayState.COMPLETED) {
  return create(RunSchema, {
    id: `wf-${chatId}`,
    sessionId: chatId,
    title: `Run of ${workflowName}`,
    workflowName,
    projectId: "proj-1",
    launchKind: "chat.start",
    state: WorkflowState.STOPPED,
    stopReason: WorkflowStopReason.COMPLETED,
    activity: ChatActivity.IDLE,
    displayState,
    createdAtMs: BigInt(Date.now() - 2 * 60 * 60 * 1000),
  });
}

export function protoTrigger(id: string, workflow: string, name = `Automation ${id}`) {
  return create(TriggerSchema, {
    id,
    name,
    projectId: "proj-1",
    projectName: "Reliant",
    daemonId: "daemon-1",
    daemonName: "MacBook",
    enabled: true,
    workflow,
    message: "Go",
    health: create(TriggerHealthSchema, { status: TriggerHealthStatus.HEALTHY }),
    source: {
      case: "schedule",
      value: create(ScheduleSourceSchema, { cron: ["0 9 * * 1-5"], timezone: "UTC" }),
    },
  });
}

/** A ListWorkflows response: a builtin, a project workflow, a user draft and a broken file. */
export function libraryResponse() {
  return {
    workflows: [
      { name: "builtin://agent", filename: "agent", description: "General-purpose coding agent", source: "builtin", stepCount: 1, nodes: [], edges: [], status: WorkflowDraftStatus.COMPLETE, validationErrors: [] },
      { name: "triage", filename: "triage", description: "Triage new issues", source: "project", stepCount: 2, nodes: [], edges: [], status: WorkflowDraftStatus.COMPLETE, validationErrors: [] },
      { name: "my-draft", filename: "my-draft", description: "Half done", source: "user", stepCount: 1, nodes: [], edges: [], status: WorkflowDraftStatus.DRAFT, validationErrors: [{ type: "missing_entry", message: "missing entry" }] },
    ],
    invalidWorkflows: [{ name: "broken", source: "project", path: ".reliant/workflows/broken.yaml", errors: ["yaml: line 3: bad indent"] }],
  };
}
