// Copyright (c) 2025 Reliant Labs

/**
 * Shared harness for the Inbox component tests: a memory router with every
 * route an Inbox row links to, a fresh QueryClient, and item builders.
 */

import type { ReactNode } from "react";
import { render } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { create } from "@bufbuild/protobuf";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router";

import { InboxItemKind, InboxItemSchema, InboxStorageSchema, type InboxItem, type InboxStorage } from "@/gen/reliant/v1/inbox_pb";
import { TriggerHealthSchema, TriggerHealthStatus } from "@/gen/reliant/v1/trigger_pb";
import { ApprovalType } from "@/gen/reliant/v1/approval_pb";

export function renderInboxAt(ui: ReactNode, path = "/inbox") {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const rootRoute = createRootRoute({ component: () => <Outlet /> });
  const routes = [
    createRoute({ getParentRoute: () => rootRoute, path: "/inbox", component: () => <>{ui}</> }),
    createRoute({ getParentRoute: () => rootRoute, path: "/" }),
    createRoute({ getParentRoute: () => rootRoute, path: "/workflows/runs/$runId" }),
    createRoute({ getParentRoute: () => rootRoute, path: "/workflows/automations/$triggerId" }),
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

const base = {
  chatId: "chat-1",
  runId: "wf-chat-1",
  projectId: "proj-1",
  projectName: "reliant",
  workflowName: "builtin://agent",
  chatTitle: "Nightly triage",
  waitingSince: new Date(Date.now() - 20 * 60_000).toISOString(),
};

export function approvalItem(overrides: Partial<InboxItem> = {}, id = "appr-1"): InboxItem {
  return create(InboxItemSchema, {
    ...base,
    kind: InboxItemKind.APPROVAL,
    itemId: `approval:${id}`,
    payload: {
      case: "approval",
      value: {
        approvalId: id,
        approvalType: ApprovalType.TOOL,
        title: "Run git push",
        toolName: "bash",
        argumentSummary: "git push origin main",
      },
    },
    ...overrides,
  });
}

export function questionItem(overrides: Partial<InboxItem> = {}): InboxItem {
  return create(InboxItemSchema, {
    ...base,
    kind: InboxItemKind.QUESTION,
    itemId: "question:q-1",
    payload: {
      case: "question",
      value: {
        questionId: "q-1",
        threadId: "t-1",
        prompt: "Which branch?",
        metadata: JSON.stringify({
          type: "ask_user",
          questions: [{ question: "Which branch?", options: [{ label: "main", description: "" }] }],
        }),
      },
    },
    ...overrides,
  });
}

export function waitingItem(overrides: Partial<InboxItem> = {}): InboxItem {
  return create(InboxItemSchema, {
    ...base,
    kind: InboxItemKind.WAITING_FOR_MACHINE,
    itemId: "waiting_for_machine:chat-1@1",
    payload: { case: "waitingForMachine", value: { daemonId: "d-1", daemonName: "MacBook" } },
    ...overrides,
  });
}

export function failingItem(overrides: Partial<InboxItem> = {}): InboxItem {
  return create(InboxItemSchema, {
    kind: InboxItemKind.AUTOMATION_FAILING,
    itemId: "automation_failing:evt-9",
    triggerId: "trg-1",
    triggerName: "Nightly triage",
    projectId: "proj-1",
    projectName: "reliant",
    workflowName: "builtin://agent",
    waitingSince: new Date(Date.now() - 60 * 60_000).toISOString(),
    payload: {
      case: "automationFailing",
      value: {
        health: create(TriggerHealthSchema, {
          status: TriggerHealthStatus.FAILING,
          consecutiveFailures: 3,
          lastFailureDetail: "tool error",
        }),
        lastRunChatId: "chat-last",
      },
    },
    ...overrides,
  });
}

export function launchFailedItem(overrides: Partial<InboxItem> = {}): InboxItem {
  return create(InboxItemSchema, {
    kind: InboxItemKind.AUTOMATION_LAUNCH_FAILED,
    itemId: "automation_launch_failed:evt-5",
    triggerId: "trg-2",
    triggerName: "Weekly report",
    projectId: "proj-1",
    projectName: "reliant",
    workflowName: "builtin://agent",
    waitingSince: new Date(Date.now() - 90 * 60_000).toISOString(),
    payload: {
      case: "automationLaunchFailed",
      value: { reason: "machine was deleted", eventId: "evt-5", consecutiveFailures: 1 },
    },
    ...overrides,
  });
}

export function runFinishedItem(overrides: Partial<InboxItem> = {}): InboxItem {
  return create(InboxItemSchema, {
    ...base,
    kind: InboxItemKind.RUN_FINISHED,
    itemId: "run_finished:chat-1",
    triggerId: "trg-3",
    triggerName: "Nightly triage",
    payload: { case: "runFinished", value: {} },
    ...overrides,
  });
}

export function storageItem(
  overrides: Partial<InboxItem> = {},
  storage: Partial<InboxStorage> = {},
): InboxItem {
  return create(InboxItemSchema, {
    kind: InboxItemKind.STORAGE,
    itemId: "storage:d-1:ab12cd34",
    waitingSince: new Date(Date.now() - 3 * 3_600_000).toISOString(),
    payload: {
      case: "storage",
      value: create(InboxStorageSchema, {
        daemonId: "d-1",
        daemonName: "MacBook",
        diskFreeBytes: 12_000_000_000n,
        diskTotalBytes: 500_000_000_000n,
        diskLow: true,
        online: true,
        held: [
          {
            worktreeId: "wt-1",
            name: "fix-login",
            projectName: "reliant",
            path: "/Users/me/.reliant/worktrees/reliant/fix-login",
            reason: "dirty",
            detail: "3 changed or untracked file(s)",
            sizeBytes: 2_400_000_000n,
            removable: true,
          },
          {
            worktreeId: "wt-2",
            name: "spike",
            projectName: "reliant",
            path: "/Users/me/.reliant/worktrees/reliant/spike",
            reason: "unpushed",
            detail: "HEAD is not on any remote branch and not merged into main",
            sizeBytes: 800_000_000n,
            removable: true,
          },
          {
            worktreeId: "wt-3",
            name: "taxes",
            projectName: "reliant",
            path: "/Users/me/.reliant/worktrees/reliant/taxes",
            reason: "files-outside-checkout",
            detail: "4 file(s) sit outside every checkout",
            sizeBytes: 90_000_000n,
            removable: false,
          },
        ],
        ...storage,
      }),
    },
    ...overrides,
  });
}
