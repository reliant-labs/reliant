// Copyright (c) 2025 Reliant Labs

/**
 * Shared harness for the Runs component tests: a memory router with every
 * route a Runs surface links to, a fresh QueryClient, and a run builder.
 */

import type { ReactNode } from "react";
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

import { RunDisplayState } from "@/gen/reliant/v1/run_pb";
import { ChatActivity, WorkflowState, WorkflowStopReason } from "@/gen/reliant/v1/chat_pb";
import type { RunSummary } from "@/api/run-grpc";
import { runsSearchSchema } from "@/routeSchemas";

export function renderRunsAt(ui: ReactNode, path = "/workflows/runs") {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const rootRoute = createRootRoute({ component: () => <Outlet /> });
  const isDetail = path.startsWith("/workflows/runs/");
  const pageRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: isDetail ? "/workflows/runs/$runId" : "/workflows/runs",
    validateSearch: isDetail ? undefined : runsSearchSchema,
    component: () => <>{ui}</>,
  });
  const otherRoutes = [
    createRoute({ getParentRoute: () => rootRoute, path: "/" }),
    createRoute({ getParentRoute: () => rootRoute, path: "/project/$projectId" }),
    createRoute({ getParentRoute: () => rootRoute, path: "/workflows/automations" }),
    createRoute({ getParentRoute: () => rootRoute, path: "/workflows/automations/$triggerId" }),
    createRoute({ getParentRoute: () => rootRoute, path: "/workflows/library" }),
    isDetail
      ? createRoute({
          getParentRoute: () => rootRoute,
          path: "/workflows/runs",
          validateSearch: runsSearchSchema,
        })
      : createRoute({ getParentRoute: () => rootRoute, path: "/workflows/runs/$runId" }),
  ];
  const router = createRouter({
    routeTree: rootRoute.addChildren([pageRoute, ...otherRoutes]),
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  const result = render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return { ...result, router, queryClient };
}

export const MINUTE = 60_000;

export function buildRun(overrides: Partial<RunSummary> = {}): RunSummary {
  const id = overrides.chatId ?? "chat-1";
  return {
    runId: `wf-${id}`,
    chatId: id,
    title: "Refactor auth",
    workflowName: "builtin://agent",
    projectId: "proj-1",
    launchKind: "chat.start",
    triggerId: "",
    triggerName: "",
    daemonId: "",
    state: WorkflowState.STOPPED,
    stopReason: WorkflowStopReason.COMPLETED,
    activity: ChatActivity.IDLE,
    displayState: RunDisplayState.COMPLETED,
    outcome: "",
    createdAt: Date.now() - 10 * MINUTE,
    completedAt: Date.now() - 5 * MINUTE,
    ...overrides,
  };
}
