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

export function renderRunsAt(ui: ReactNode, path = "/runs") {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const rootRoute = createRootRoute({ component: () => <Outlet /> });
  const isDetail = path.startsWith("/runs/");
  const pageRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: isDetail ? "/runs/$runId" : "/runs",
    validateSearch: isDetail ? undefined : runsSearchSchema,
    component: () => <>{ui}</>,
  });
  const otherRoutes = [
    createRoute({ getParentRoute: () => rootRoute, path: "/" }),
    createRoute({ getParentRoute: () => rootRoute, path: "/project/$projectId" }),
    createRoute({ getParentRoute: () => rootRoute, path: "/automations" }),
    createRoute({ getParentRoute: () => rootRoute, path: "/automations/$triggerId" }),
    createRoute({ getParentRoute: () => rootRoute, path: "/workflow" }),
    isDetail
      ? createRoute({
          getParentRoute: () => rootRoute,
          path: "/runs",
          validateSearch: runsSearchSchema,
        })
      : createRoute({ getParentRoute: () => rootRoute, path: "/runs/$runId" }),
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
