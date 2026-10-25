// Copyright (c) 2025 Reliant Labs

/**
 * ChatService has two key families, and every read belongs to exactly one:
 *
 *   - the stream-owned chat core (list, detail, selection, messages) on the
 *     hand `chatKeys` / `messageKeys`, which the stream patches in place and
 *     nothing may refetch over;
 *   - the pull reads (archived chats, search, step and workflow executions,
 *     thread inputs, a thread's mailbox) on the generated `chatServiceKeys`,
 *     which the stream only invalidates.
 *
 * Every generated ChatService mutation invalidates `chatServiceKeys.all`, so
 * that family may be refetched by any of them; the core must never be under
 * it. And every hand write names the generated scope it affects. These pin
 * each write → read pair, answered at the transport.
 */

import type { ReactNode } from "react";
import { act, fireEvent, render, renderHook, screen, waitFor } from "@testing-library/react";
import { QueryClientProvider, type QueryKey } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { api } from "@/api/client";
import {
  ChatState,
  WorkflowExecutionView,
  type GetWorkflowExecutionsRequest,
  type ListQueuedAgentMessagesRequest,
} from "@/gen/reliant/v1/chat_pb";
import { EntityType, UserUpdateType } from "@/gen/reliant/v1/streaming_pb";
import { setTransport } from "@/lib/connect";
import { queryClient } from "@/lib/query-client";
import { mockTransport } from "@/lib/test-utils";
import { QueuedMessages } from "@/components/Chat/QueuedMessages";
import { useChatStore } from "@/store/chatStore";
import { resetStreamInvalidation, useGlobalUpdatesStore } from "@/store/globalUpdatesStore";
import { useProjectStore } from "@/store/projectStore";
import { triggerRefetch } from "@/store/refetchStore";
import { STREAM_INVALIDATED_SCOPES } from "@/store/streamInvalidatedScopes";
import type { UserUpdate } from "@/types/streaming";
import { chatKeys, useArchiveChat, useArchivedChats, useUnarchiveChat } from "../chat-queries";
import { useChatSearch } from "../chat-pull-queries";
import { chatServiceKeys, useListStepExecutions } from "../chat-service-hooks_gen";
import { clearAllMessagesCache, messageKeys, useMessages } from "../message-queries";
import { useQueuedAgentMessages } from "../queued-agent-messages";
import { useAdoptRun } from "../run-queries";
import { useWorkflowExecutions } from "../useWorkflowExecutions";

const PROJECT = "p-pull";
const CHAT = "chat-pull";
const THREAD = "thread-pull";

const server = {
  listArchivedChats: vi.fn(),
  listQueuedAgentMessages: vi.fn(),
  cancelQueuedAgentMessage: vi.fn(),
  getWorkflowExecutions: vi.fn(),
  listStepExecutions: vi.fn(),
  searchChats: vi.fn(),
  updateChatState: vi.fn(),
  adoptChat: vi.fn(),
};

function calls(fake: ReturnType<typeof vi.fn>): number {
  return fake.mock.calls.length;
}

const wrapper = ({ children }: { children: ReactNode }) => (
  <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
);

/** Let React Query deliver what it scheduled, and the fakes answer. */
async function settle() {
  await act(async () => {
    for (let i = 0; i < 5; i++) await new Promise((r) => setTimeout(r, 0));
  });
}

function isUnder(key: QueryKey | undefined, root: QueryKey): boolean {
  return !!key && root.every((part, i) => JSON.stringify(part) === JSON.stringify(key[i]));
}

function stateChange(state: string, previous: string): UserUpdate {
  return {
    id: `u-${Math.random()}`,
    user_id: "user-1",
    sequence_number: 0,
    chat_id: CHAT,
    project_id: PROJECT,
    worktree_id: "",
    update_type: UserUpdateType.CHAT_STATE_CHANGE,
    entity_type: EntityType.CHAT,
    entity_id: CHAT,
    data: { state, previous_state: previous, reason: "" },
    created_at: new Date().toISOString(),
  };
}

beforeEach(() => {
  queryClient.clear();
  clearAllMessagesCache();
  resetStreamInvalidation();
  for (const fake of Object.values(server)) fake.mockReset();
  server.listArchivedChats.mockReturnValue({ chats: [] });
  server.listQueuedAgentMessages.mockReturnValue({ messages: [] });
  server.cancelQueuedAgentMessage.mockReturnValue({ success: true });
  server.getWorkflowExecutions.mockReturnValue({});
  server.listStepExecutions.mockReturnValue({});
  server.searchChats.mockReturnValue({ chats: [] });
  server.updateChatState.mockImplementation((req: { state: ChatState }) => ({ state: req.state }));
  server.adoptChat.mockReturnValue({ chat: { id: CHAT, projectId: PROJECT } });
  setTransport(
    mockTransport({
      "ChatService.ListArchivedChats": (req) => server.listArchivedChats(req),
      "ChatService.ListQueuedAgentMessages": (req) => server.listQueuedAgentMessages(req),
      "ChatService.CancelQueuedAgentMessage": (req) => server.cancelQueuedAgentMessage(req),
      "ChatService.GetWorkflowExecutions": (req) => server.getWorkflowExecutions(req),
      "ChatService.ListStepExecutions": (req) => server.listStepExecutions(req),
      "ChatService.SearchChats": (req) => server.searchChats(req),
      "ChatService.UpdateChatState": (req) => server.updateChatState(req),
      "ChatService.AdoptChat": (req) => server.adoptChat(req),
      // The chat list a chat search falls back on.
      "ChatService.ListChats": { response: { chats: [] } },
    }),
  );
  useProjectStore.setState({ currentProject: { id: PROJECT, name: "p", path: "/p" } } as never);
});

afterEach(() => {
  setTransport(null);
  vi.restoreAllMocks();
  queryClient.clear();
});

describe("the archived list (chatServiceKeys.archivedChat)", () => {
  it("refetches when a chat is archived or restored by hand", async () => {
    renderHook(() => useArchivedChats(), { wrapper });
    await waitFor(() => expect(calls(server.listArchivedChats)).toBe(1));

    const archive = renderHook(() => useArchiveChat(), { wrapper });
    await act(() => archive.result.current.mutateAsync(CHAT).then(() => undefined));
    await waitFor(() => expect(calls(server.listArchivedChats)).toBe(2));

    const restore = renderHook(() => useUnarchiveChat(), { wrapper });
    await act(() => restore.result.current.mutateAsync(CHAT).then(() => undefined));
    await waitFor(() => expect(calls(server.listArchivedChats)).toBe(3));
  });

  it("refetches when the stream says a chat was archived or restored", async () => {
    vi.spyOn(useChatStore.getState(), "evictChat").mockImplementation(() => {});
    renderHook(() => useArchivedChats(), { wrapper });
    await waitFor(() => expect(calls(server.listArchivedChats)).toBe(1));

    act(() => useGlobalUpdatesStore.getState().handleUpdate([stateChange("archived", "idle")]));
    await waitFor(() => expect(calls(server.listArchivedChats)).toBe(2));

    act(() => useGlobalUpdatesStore.getState().handleUpdate([stateChange("idle", "archived")]));
    await waitFor(() => expect(calls(server.listArchivedChats)).toBe(3));
  });

  it("maps each row to the chat with its workspace flattened in", async () => {
    server.listArchivedChats.mockReturnValue({
      chats: [{ chat: { id: CHAT, title: "old work" }, worktreeName: "feature-x" }],
    });
    const { result } = renderHook(() => useArchivedChats(), { wrapper });
    await waitFor(() => expect(result.current.data).toHaveLength(1));
    expect(result.current.data![0]).toMatchObject({ id: CHAT, title: "old work", worktreeName: "feature-x" });
    expect(result.current.data![0]).not.toHaveProperty("$typeName");
  });
});

describe("a thread's mailbox (chatServiceKeys.queuedAgentMessage)", () => {
  it("cancelling a queued message drops it at once, then re-reads the mailbox", async () => {
    const row = { id: "q-1", body: "stop and check the tests", createdAt: new Date().toISOString(), senderKind: 5 };
    server.listQueuedAgentMessages.mockReturnValue({ messages: [row] });
    // The cancel patches the entry first; the re-read comes after.
    let rereadBeforeDrop = true;

    function Strip() {
      const queue = useQueuedAgentMessages(CHAT, THREAD, true);
      return (
        <QueuedMessages
          chatId={CHAT}
          threadId={THREAD}
          messages={queue.messages}
          onRefresh={queue.refresh}
          onForget={queue.forget}
        />
      );
    }
    render(<Strip />, { wrapper });
    expect(await screen.findByText("stop and check the tests")).toBeInTheDocument();
    const reads = calls(server.listQueuedAgentMessages);

    // The server still lists the row for the re-read; the tombstone keeps it out.
    const key = chatServiceKeys.listQueuedAgentMessages({ chatId: CHAT, threadId: THREAD });
    server.listQueuedAgentMessages.mockImplementation(() => {
      rereadBeforeDrop = !!queryClient.getQueryData(key)?.messages.some((m) => m.id === "q-1");
      return { messages: [row] };
    });
    fireEvent.click(screen.getByRole("button", { name: "Cancel queued message" }));

    await waitFor(() => expect(calls(server.listQueuedAgentMessages)).toBe(reads + 1));
    expect(rereadBeforeDrop).toBe(false);
    expect(server.cancelQueuedAgentMessage).toHaveBeenCalledWith(
      expect.objectContaining({ chatId: CHAT, messageId: "q-1" }),
    );
    await settle();
    expect(screen.queryByText("stop and check the tests")).not.toBeInTheDocument();
  });

  it("reads the mailbox of the thread it was asked for", async () => {
    renderHook(() => useQueuedAgentMessages(CHAT, THREAD, false), { wrapper });
    await waitFor(() => expect(calls(server.listQueuedAgentMessages)).toBe(1));
    const request = server.listQueuedAgentMessages.mock.calls[0]![0] as ListQueuedAgentMessagesRequest;
    expect([request.chatId, request.threadId]).toEqual([CHAT, THREAD]);
  });
});

describe("a chat's execution tree", () => {
  it("one workflow_executions pulse refetches once however many readers are mounted", async () => {
    for (let i = 0; i < 4; i++) renderHook(() => useWorkflowExecutions(CHAT), { wrapper });
    await waitFor(() => expect(calls(server.getWorkflowExecutions)).toBe(1));

    triggerRefetch("workflow_executions");
    await waitFor(() => expect(calls(server.getWorkflowExecutions)).toBe(2), { timeout: 2000 });
    await new Promise((r) => setTimeout(r, 50));
    expect(calls(server.getWorkflowExecutions)).toBe(2);
    const request = server.getWorkflowExecutions.mock.calls[1]![0] as GetWorkflowExecutionsRequest;
    expect([request.chatId, request.view]).toEqual([CHAT, WorkflowExecutionView.BASIC]);
  });
});

describe("the two key families stay apart", () => {
  /** One mounted reader of every moved read, and the stream-owned envelope. */
  async function mountEveryRead() {
    renderHook(() => useArchivedChats(), { wrapper });
    renderHook(() => useQueuedAgentMessages(CHAT, THREAD, false), { wrapper });
    renderHook(() => useWorkflowExecutions(CHAT), { wrapper });
    renderHook(() => useListStepExecutions({ chatId: CHAT }), { wrapper });
    renderHook(() => useChatSearch(PROJECT, "deploy"), { wrapper });
    // The envelope the stream maintains: seeded, as a chat open leaves it.
    queryClient.setQueryData(messageKeys.list(CHAT), { messages: [], total: 0, hasMore: false, oldestSeq: 0 });
    renderHook(() => useMessages(CHAT), { wrapper });
    await waitFor(() => {
      for (const fake of [
        server.listArchivedChats,
        server.listQueuedAgentMessages,
        server.getWorkflowExecutions,
        server.listStepExecutions,
        server.searchChats,
      ]) {
        expect(calls(fake)).toBe(1);
      }
    });
  }

  function movedReadCalls() {
    return [
      calls(server.listArchivedChats),
      calls(server.listQueuedAgentMessages),
      calls(server.getWorkflowExecutions),
      calls(server.listStepExecutions),
      calls(server.searchChats),
    ];
  }

  it("a SendMessage refetches none of the moved reads", async () => {
    await mountEveryRead();
    queryClient.setQueryData(chatKeys.detail(CHAT), { id: CHAT, projectId: PROJECT, title: "t", workflowId: THREAD });
    vi.spyOn(api.chatsV2, "sendMessage").mockResolvedValue({
      chat_id: CHAT,
      workflow_id: THREAD,
      run_id: "run-1",
      status: "running",
      message_id: "m-1",
      queued: false,
    } as never);
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const before = movedReadCalls();

    await act(() => useChatStore.getState().sendMessage(CHAT, "ship it", undefined, {}));
    await settle();

    expect(api.chatsV2.sendMessage).toHaveBeenCalled();
    expect(movedReadCalls()).toEqual(before);
    const invalidated = invalidate.mock.calls.map(([filters]) => filters?.queryKey);
    expect(invalidated.filter((key) => isUnder(key, chatServiceKeys.all))).toEqual([]);
  });

  it("a generated mutation (AdoptChat) refetches the pull reads, never the message envelope", async () => {
    await mountEveryRead();
    const before = movedReadCalls();
    const adopt = renderHook(() => useAdoptRun(), { wrapper });

    await act(() => adopt.result.current.mutateAsync({ chatId: CHAT }).then(() => undefined));
    await settle();

    // chatServiceKeys.all reached every mounted pull read …
    await waitFor(() => {
      const after = movedReadCalls();
      after.forEach((n, i) => expect(n).toBeGreaterThan(before[i]!));
    });
    // … and not the stream-owned envelope.
    expect(queryClient.getQueryState(messageKeys.list(CHAT))?.isInvalidated).toBe(false);
    expect(isUnder(messageKeys.list(CHAT), chatServiceKeys.all)).toBe(false);
    expect(isUnder(chatKeys.detail(CHAT), chatServiceKeys.all)).toBe(false);
  });
});

describe("the reconnect sweep", () => {
  it("is registered for chatServiceKeys.all and re-reads the pull reads read before an outage", async () => {
    expect(STREAM_INVALIDATED_SCOPES.chatService).toEqual(chatServiceKeys.all);
    vi.spyOn(useChatStore.getState(), "loadChats").mockResolvedValue(undefined as never);
    renderHook(() => useArchivedChats(), { wrapper });
    renderHook(() => useWorkflowExecutions(CHAT), { wrapper });
    await waitFor(() => {
      expect(calls(server.listArchivedChats)).toBe(1);
      expect(calls(server.getWorkflowExecutions)).toBe(1);
    });
    await new Promise((r) => setTimeout(r, 5));

    const store = useGlobalUpdatesStore.getState();
    act(() => {
      store.handleStatusChange("error");
      store.handleStatusChange("connecting");
      store.handleStatusChange("connected");
    });

    await waitFor(() => {
      expect(calls(server.listArchivedChats)).toBe(2);
      expect(calls(server.getWorkflowExecutions)).toBe(2);
    });
  });
});
