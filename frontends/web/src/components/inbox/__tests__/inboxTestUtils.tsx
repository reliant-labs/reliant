// Copyright (c) 2025 Reliant Labs

/**
 * Shared harness for the Inbox component tests: a memory router with every
 * route an Inbox row links to, a fresh QueryClient, and item builders.
 */

import type { ReactNode } from "react";
import { render } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router";

import { InboxItemKind, InboxItemSchema, InboxStorageSchema, type InboxItem } from "@/gen/reliant/v1/inbox_pb";
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

/** A fixture's overrides: any fields but the message's own type tag. */
type Overrides<T> = Partial<Omit<T, "$typeName">>;

export function approvalItem(overrides: Overrides<InboxItem> = {}, id = "appr-1"): InboxItem {
  return create(InboxItemSchema, {
    ...base,
    kind: InboxItemKind.APPROVAL,
    itemId: `approval:${id}`,
    payload: {
      case: "approval",
      value: {
        approvalId: id,
        approvalType: ApprovalType.WORKFLOW_STEP,
        title: "Deploy to production?",
      },
    },
    ...overrides,
  });
}

export function questionItem(overrides: Overrides<InboxItem> = {}): InboxItem {
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

export function waitingItem(overrides: Overrides<InboxItem> = {}): InboxItem {
  return create(InboxItemSchema, {
    ...base,
    kind: InboxItemKind.WAITING_FOR_MACHINE,
    itemId: "waiting_for_machine:chat-1@1",
    payload: { case: "waitingForMachine", value: { daemonId: "d-1", daemonName: "MacBook" } },
    ...overrides,
  });
}

export function failingItem(overrides: Overrides<InboxItem> = {}): InboxItem {
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

export function launchFailedItem(overrides: Overrides<InboxItem> = {}): InboxItem {
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

export function needsSetupItem(overrides: Overrides<InboxItem> = {}): InboxItem {
  return create(InboxItemSchema, {
    kind: InboxItemKind.AUTOMATION_NEEDS_SETUP,
    itemId: "automation_needs_setup:trg-4:repos",
    triggerId: "trg-4",
    triggerName: "Autopilot · PR review",
    projectId: "proj-1",
    projectName: "reliant",
    workflowName: "builtin://autopilot-pr-review",
    waitingSince: new Date(Date.now() - 30 * 60_000).toISOString(),
    payload: {
      case: "automationNeedsSetup",
      value: { issues: [{ input: "repos", message: "repos is required" }] },
    },
    ...overrides,
  });
}

export function runFinishedItem(overrides: Overrides<InboxItem> = {}): InboxItem {
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

export function runPausedItem(overrides: Overrides<InboxItem> = {}): InboxItem {
  return create(InboxItemSchema, {
    ...base,
    kind: InboxItemKind.AUTOMATION_RUN_PAUSED,
    itemId: "automation_run_paused:chat-1@20261030T082500.000000",
    triggerId: "trg-4",
    triggerName: "cp-merge",
    payload: {
      case: "automationRunPaused",
      value: { reason: "machine e164d47d did not answer the gate command" },
    },
    ...overrides,
  });
}

export function storageItem(
  overrides: Overrides<InboxItem> = {},
  // An init shape, not Overrides: its nested messages (volumes, held,
  // processes.top) are written as plain objects, which create() fills in.
  storage: MessageInitShape<typeof InboxStorageSchema> = {},
): InboxItem {
  return create(InboxItemSchema, {
    kind: InboxItemKind.STORAGE,
    itemId: "storage:d-1:disk:1760000000000:critical",
    waitingSince: new Date(Date.now() - 3 * 3_600_000).toISOString(),
    payload: {
      case: "storage",
      value: create(InboxStorageSchema, {
        daemonId: "d-1",
        daemonName: "MacBook",
        diskLow: true,
        // The cloud-machine shape: the build-cache & docker volume is full,
        // the home volume is not. Fullest first, as the server sends them.
        volumes: [
          {
            label: "build caches & docker",
            paths: ["/var/cache/workspace/go-build", "/var/lib/docker"],
            totalBytes: 40_000_000_000n,
            freeBytes: 2_000_000_000n,
            pressure: true,
            consumersMeasuredAtMs: BigInt(Date.now()),
            consumers: [
              { kind: "docker_images", label: "Docker images", category: "docker", bytes: 13_000_000_000n, reclaimableBytes: 11_000_000_000n },
              { kind: "go_build_cache", label: "Go build cache", category: "caches", path: "/var/cache/workspace/go-build", bytes: 9_000_000_000n, reclaimableBytes: 6_000_000_000n, clearable: true },
              { kind: "go_mod_cache", label: "Go module cache", category: "caches", path: "/var/cache/workspace/go-mod", bytes: 6_000_000_000n },
              { kind: "docker_volumes", label: "Docker volumes", category: "docker", bytes: 5_000_000_000n, reclaimableBytes: 4_500_000_000n },
              { kind: "docker_build_cache", label: "Docker build cache", category: "docker", bytes: 3_000_000_000n, reclaimableBytes: 1_000_000_000n, clearable: true },
            ],
          },
          { label: "home", paths: ["/home/workspace"], totalBytes: 157_000_000_000n, freeBytes: 124_000_000_000n },
        ],
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
