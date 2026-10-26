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

import { invalidatePresets } from "../../../hooks/preset-queries";
import { setTransport } from "../../../lib/connect";
import { queryClient } from "../../../lib/query-client";
import { mockTransport } from "../../../lib/test-utils";
import { useProjectStore } from "../../../store/projectStore";
import {
  markChatSelectionsUnmaintained,
  seedChatDetail,
  useChat,
} from "../../../hooks/chat-queries";
import { useMessages } from "../../../hooks/message-queries";
import { useApprovals, usePendingApprovals } from "../../../hooks/approval-queries";
import { usePendingQuestions } from "../../../hooks/question-queries";
import { useWorkflowExecutions } from "../../../hooks/useWorkflowExecutions";
import { ChatInput } from "../ChatInput";

const PROJECT_ID = "p-budget";
const CHAT_A = "aaaaaaaa-0000-0000-0000-00000000000a";
const CHAT_B = "bbbbbbbb-0000-0000-0000-00000000000b";

type Counter = { name: string; spy: { mock: { calls: unknown[] } } };
let counters: Counter[] = [];

function spyRpcs() {
  const chat = (id: string) => ({ id, projectId: PROJECT_ID, title: id });
  // Services on their generated hooks are counted where they reach the wire.
  const listPendingQuestions = vi.fn(() => ({ questions: [] }));
  const listApprovalsByChat = vi.fn(() => ({ approvals: [] }));
  const listPresetsForWorkflow = vi.fn(() => ({ presets: [], invalidPresets: [] }));
  const getDefaultPresetsBatch = vi.fn(() => ({ presetsByWorkflow: {} }));
  const getWorkflow = vi.fn(() => ({}));
  const getWorkflowExecutions = vi.fn(() => ({}));
  const listQueuedAgentMessages = vi.fn(() => ({ messages: [] }));
  const getChat = vi.fn((req: { chatId: string }) => ({ chat: chat(req.chatId) }));
  const listMessages = vi.fn(() => ({ messages: [], total: 0, hasMore: false }));
  setTransport(
    mockTransport({
      "QuestionService.ListPendingQuestions": listPendingQuestions,
      "ApprovalService.ListApprovalsByChat": listApprovalsByChat,
      "PresetService.ListPresetsForWorkflow": listPresetsForWorkflow,
      "PresetService.GetDefaultPresetsBatch": getDefaultPresetsBatch,
      "WorkflowService.GetWorkflow": getWorkflow,
      "ChatService.GetWorkflowExecutions": getWorkflowExecutions,
      "ChatService.ListQueuedAgentMessages": listQueuedAgentMessages,
      "ChatService.GetChat": (req: unknown) => getChat(req as { chatId: string }),
      "ChatService.ListMessages": listMessages,
      // The model catalog is app-wide (prefetched once per session), not a
      // chat-open read; answer it empty so the composer can mount.
      "CatalogService.ListModels": { response: {} },
      "CatalogService.ListAvailableModels": { response: {} },
    }),
  );
  counters = [
    { name: "GetChat", spy: getChat },
    { name: "ListMessages", spy: listMessages },
    { name: "ListApprovalsByChat", spy: listApprovalsByChat },
    { name: "ListPendingQuestions", spy: listPendingQuestions },
    { name: "GetWorkflowExecutions", spy: getWorkflowExecutions },
    { name: "ListQueuedAgentMessages", spy: listQueuedAgentMessages },
    { name: "GetWorkflow", spy: getWorkflow },
    { name: "ListPresetsForWorkflow", spy: listPresetsForWorkflow },
    { name: "GetDefaultPresets", spy: getDefaultPresetsBatch },
  ];
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
  usePendingQuestions(chatId);
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

    expect(issued).toEqual({
      ListMessages: 1,
      ListApprovalsByChat: 1,
      ListPendingQuestions: 1,
      GetWorkflowExecutions: 1,
      GetWorkflow: 1,
      ListPresetsForWorkflow: 1,
      // The chat list seeded the detail, but the list omits the chat's
      // server-side selection (its model and params), so the composer reads it
      // once — the only GetChat a chat costs until the stream stops vouching
      // for it.
      GetChat: 1,
    });
    // An existing chat's default presets are the server's to apply (the
    // composer no longer writes them back as a pick), so it does not read them.
    expect(issued.GetDefaultPresets).toBeUndefined();
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
    // What every preset write does (hooks/preset-queries).
    await invalidatePresets();
    const issued = await openChat(CHAT_A);

    expect(issued.ListPresetsForWorkflow).toBe(1);
    // Only a new chat applies default presets client-side.
    expect(issued.GetDefaultPresets).toBeUndefined();
  });

  it("re-reads a selection once the stream stops maintaining it", async () => {
    await openChat(CHAT_A);
    // The stream closed (a project switch re-scopes it): nothing delivered the
    // selection's changes meanwhile.
    markChatSelectionsUnmaintained();

    const issued = await openChat(CHAT_A);

    expect(issued).toEqual({ GetChat: 1 });
  });
});
