/**
 * The RPCs a chat screen issues when it opens.
 *
 * Opening a chat mounts the composer (ChatInput) and the reads ChatContainer
 * makes for the transcript, approvals, the pending question and the workflow
 * tree. Before Round 2 every one of those either refetched on every mount
 * (GetWorkflow, ListPresetsForWorkflow — bare effects) or after a 30s
 * staleTime (approvals, question, execution tree), so going back to a chat
 * from the chat list or the new-chat view re-issued all of them even though
 * the stream had kept every answer current.
 *
 * This renders the real composer and the real read hooks against the app's
 * real QueryClient defaults and counts what each open issues. It is also the
 * measurement behind the Round 2 table in triage/results/web-client-errors.md.
 */

import { act, cleanup, render, waitFor } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
} as unknown as typeof ResizeObserver;

vi.mock("../../../lib/toast-manager", () => ({
  toast: { error: vi.fn(), info: vi.fn(), success: vi.fn(), warning: vi.fn() },
}));
vi.mock("../../Settings/ModelPreferences", () => ({
  loadTagModelConfigs: vi.fn().mockResolvedValue({}),
}));
const NO_SLASH_COMMANDS: never[] = [];
vi.mock("../../../hooks/useSlashCommands", () => ({
  useSlashCommands: () => NO_SLASH_COMMANDS,
}));
vi.mock("../settings", () => ({ ChatSettingsPopover: () => null }));
vi.mock("../WorkflowSelector", () => ({ WorkflowSelector: () => null }));

import { api } from "../../../api/client";
import { chatGrpc } from "../../../api/chat-grpc";
import { questionGrpc } from "../../../api/question-grpc";
import { workflowGrpc } from "../../../api/workflow-grpc";
import { presetGrpc } from "../../../api/preset-grpc";
import { queryClient } from "../../../lib/query-client";
import { useProjectStore } from "../../../store/projectStore";
import { seedChatDetail, useChat } from "../../../hooks/chat-queries";
import { useMessages } from "../../../hooks/message-queries";
import {
  useApprovals,
  usePendingApprovals,
  usePendingQuestion,
} from "../../../hooks/approval-queries";
import { useWorkflowExecutions } from "../../../hooks/useWorkflowExecutions";
import { ChatInput } from "../ChatInput";

const PROJECT_ID = "p-budget";
const CHAT_A = "aaaaaaaa-0000-0000-0000-00000000000a";
const CHAT_B = "bbbbbbbb-0000-0000-0000-00000000000b";

type Counter = { name: string; spy: { mock: { calls: unknown[] } } };
let counters: Counter[] = [];

function spyRpcs() {
  const chat = (id: string) => ({ id, projectId: PROJECT_ID, title: id });
  counters = [
    { name: "GetChat", spy: vi.spyOn(api.chatsV2, "get").mockImplementation(async (id: string) => chat(id) as never) },
    {
      name: "ListMessages",
      spy: vi.spyOn(api.chatsV2, "listMessages").mockResolvedValue({
        messages: [],
        total: 0,
        hasMore: false,
        oldestSeq: 0,
      } as never),
    },
    { name: "ListApprovalsByChat", spy: vi.spyOn(api.approvals, "listByChat").mockResolvedValue([] as never) },
    { name: "GetPendingQuestion", spy: vi.spyOn(questionGrpc, "getPendingQuestion").mockResolvedValue(null as never) },
    {
      name: "GetWorkflowExecutions",
      spy: vi.spyOn(chatGrpc, "getWorkflowExecutions").mockResolvedValue({ all: [], latest: null } as never),
    },
    {
      name: "ListQueuedAgentMessages",
      spy: vi.spyOn(chatGrpc, "listQueuedAgentMessages").mockResolvedValue({ messages: [] } as never),
    },
    {
      name: "GetWorkflow",
      spy: vi.spyOn(workflowGrpc, "getWorkflow").mockResolvedValue({ workflow: undefined } as never),
    },
    {
      name: "ListPresetsForWorkflow",
      spy: vi.spyOn(presetGrpc, "listPresetsForWorkflow").mockResolvedValue([]),
    },
    { name: "GetDefaultPresets", spy: vi.spyOn(presetGrpc, "getDefaultPresets").mockResolvedValue({}) },
  ];
  // The model catalog is app-wide (prefetched once per session), not a
  // chat-open read; stub it so the composer can mount.
  vi.spyOn(api.models, "list").mockResolvedValue({ models: [], tiers: {} } as never);
}

function snapshotCounts(): Record<string, number> {
  return Object.fromEntries(counters.map((c) => [c.name, c.spy.mock.calls.length]));
}

function diff(before: Record<string, number>, after: Record<string, number>) {
  const out: Record<string, number> = {};
  for (const [name, n] of Object.entries(after)) {
    if (n - (before[name] ?? 0) > 0) out[name] = n - (before[name] ?? 0);
  }
  return out;
}

/** What ChatContainer reads for the chat it renders. */
function ChatScreenReads({ chatId }: { chatId: string }) {
  useChat(chatId);
  useMessages(chatId);
  useApprovals(chatId);
  usePendingApprovals(chatId);
  usePendingQuestion(chatId);
  useWorkflowExecutions(chatId);
  return null;
}

function Providers({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

/** Open a chat screen, let its reads settle, close it; returns what it issued. */
async function openChat(chatId: string): Promise<Record<string, number>> {
  const before = snapshotCounts();
  const view = render(
    <Providers>
      <ChatScreenReads chatId={chatId} />
      <ChatInput chatId={chatId} onSend={vi.fn()} />
    </Providers>,
  );
  // Settled: every query this mount started has finished.
  await waitFor(() => expect(queryClient.isFetching()).toBe(0));
  await act(async () => undefined);
  view.unmount();
  return diff(before, snapshotCounts());
}

/** Let wall-clock time pass, as React Query sees it. */
function elapse(ms: number) {
  vi.setSystemTime(Date.now() + ms);
}

beforeEach(() => {
  queryClient.clear();
  useProjectStore.setState({ currentProject: { id: PROJECT_ID } as never });
  spyRpcs();
  // The chat list has loaded (loadChats seeds every chat's detail).
  seedChatDetail({ id: CHAT_A, projectId: PROJECT_ID, title: "A" } as never);
  seedChatDetail({ id: CHAT_B, projectId: PROJECT_ID, title: "B" } as never);
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
  queryClient.clear();
});

describe("RPCs issued by opening a chat", () => {
  it("first open of a cold chat reads what it has not got", async () => {
    const issued = await openChat(CHAT_A);
    console.info("[rpc-budget] cold open", JSON.stringify(issued));

    expect(issued).toMatchObject({
      ListMessages: 1,
      ListApprovalsByChat: 1,
      GetPendingQuestion: 1,
      GetWorkflowExecutions: 1,
      GetWorkflow: 1,
      ListPresetsForWorkflow: 1,
      GetDefaultPresets: 1,
    });
    // The chat list seeded the detail.
    expect(issued.GetChat).toBeUndefined();
  });

  it("returning to the same chat a minute later issues nothing the stream already keeps current", async () => {
    await openChat(CHAT_A);
    elapse(60_000);

    const issued = await openChat(CHAT_A);
    console.info("[rpc-budget] reopen after 60s", JSON.stringify(issued));

    // At most a background revalidation of the chat record itself, which
    // renders from cache meanwhile — nothing that gates the screen.
    expect(Object.keys(issued).filter((rpc) => rpc !== "GetChat")).toEqual([]);
  });

  it("switching A → B → A within seconds re-reads nothing for A", async () => {
    await openChat(CHAT_A);
    elapse(5_000);
    await openChat(CHAT_B);
    elapse(5_000);

    const issued = await openChat(CHAT_A);
    console.info("[rpc-budget] A→B→A", JSON.stringify(issued));

    expect(issued).toEqual({});
  });

  it("a preset change still reaches the composer", async () => {
    await openChat(CHAT_A);
    const { useGlobalDataStore } = await import("../../../store/globalDataStore");
    vi.spyOn(presetGrpc, "listPresets").mockResolvedValue([]);

    await useGlobalDataStore.getState().refetchPresets(PROJECT_ID);
    const issued = await openChat(CHAT_A);

    expect(issued.ListPresetsForWorkflow).toBe(1);
    expect(issued.GetDefaultPresets).toBe(1);
  });
});
