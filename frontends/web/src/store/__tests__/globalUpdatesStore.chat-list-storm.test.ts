import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryObserver } from "@tanstack/react-query";
import { UserUpdateType, EntityType } from "../../gen/reliant/v1/streaming_pb";
import { ChatActivity, ChatState } from "../../gen/reliant/v1/chat_pb";
import type { UserUpdate } from "../../types/streaming";

// The chat-list storm (prod, 2026-10-29 and 2026-10-30): an automation-heavy
// user's stream delivers bursts of chat_created, chats going idle and
// automation runs archiving themselves, and a reconnect replays them all.
// Each one refetched the sidebar's ListChats, the archived list or the
// approvals AT ONCE, and ListChats ignored the abort that superseded it, so
// one tab kept dozens of 2s ListChats running on the server at a time. The
// api-server's database pool filled and every chat opened in those minutes
// waited behind them (GetChat p50 67ms outside, 2.3s inside).

vi.mock("../../api/streaming-grpc", () => ({
  UserStreamingService: vi.fn().mockImplementation(() => ({
    isConnected: () => false,
    disconnect: vi.fn(),
  })),
}));

vi.mock("../../lib/notifications", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/notifications")>();
  return {
    ...actual,
    showWorkflowCompletionNotification: vi.fn(),
    showApprovalRequiredNotification: vi.fn(),
    getNotificationPermission: () => "denied" as const,
  };
});

import { queryClient } from "../../lib/query-client";
import * as chatActions from "../../hooks/chat-actions";
import { chatKeys, useChatListWithWorkspaces } from "../../hooks/chat-queries";
import { chatServiceKeys } from "../../hooks/chat-service-hooks_gen";
import { approvalServiceKeys } from "../../hooks/approval-service-hooks_gen";
import {
  AUTOMATION_SUCCEEDED_REASON,
  STREAM_REFETCH_INTERVAL_MS,
  resetStreamInvalidation,
  useGlobalUpdatesStore,
} from "../globalUpdatesStore";
import { renderHook } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import { createElement, type ReactNode } from "react";

function update(seq: number, update_type: UserUpdateType, chatId: string, data: Record<string, unknown>): UserUpdate {
  return {
    id: `u-${seq}`,
    user_id: "user-1",
    sequence_number: seq,
    chat_id: chatId,
    project_id: "p1",
    update_type,
    entity_type: EntityType.CHAT,
    entity_id: chatId,
    data,
    created_at: new Date().toISOString(),
  };
}

/** An automation run's life on the user stream: created, idle, archived. */
function automationRuns(count: number): UserUpdate[] {
  const out: UserUpdate[] = [];
  for (let i = 0; i < count; i++) {
    const chatId = `c-run-${i}`;
    const seq = i * 3;
    out.push(update(seq + 1, UserUpdateType.CHAT_CREATED, chatId, { chat_id: chatId, title: `Run ${i}` }));
    out.push(update(seq + 2, UserUpdateType.CHAT_ACTIVITY_CHANGED, chatId, { chat_id: chatId, activity: ChatActivity.IDLE }));
    out.push(
      update(seq + 3, UserUpdateType.CHAT_STATE_CHANGE, chatId, {
        state: ChatState.ARCHIVED,
        previous_state: ChatState.IDLE,
        reason: AUTOMATION_SUCCEEDED_REASON,
      }),
    );
  }
  return out;
}

const emptyList = () => ({ chats: [], lastUserUpdateSequence: 0, mainWorktrees: [] });

const chatListKey = chatKeys.list("p1");
const archivedKey = chatServiceKeys.listArchivedChats({});
const approvalsKey = approvalServiceKeys.listApprovalsByChat({ chatId: "c-open" });

beforeEach(() => {
  queryClient.clear();
});

afterEach(() => {
  resetStreamInvalidation();
  vi.restoreAllMocks();
  vi.useRealTimers();
  queryClient.clear();
});

describe("user-stream bursts do not storm the list reads", () => {
  it("a replay of 50 automation runs refetches each observed list once, not once per update", async () => {
    vi.useFakeTimers();
    const reads = {
      chats: vi.fn(emptyList),
      archived: vi.fn(() => ({ chats: [] })),
      approvals: vi.fn(() => ({ approvals: [], total: 0 })),
    };
    const observe = (queryKey: readonly unknown[], fn: () => unknown) =>
      new QueryObserver(queryClient, { queryKey, queryFn: async () => fn(), staleTime: Infinity }).subscribe(
        () => {},
      );
    const unsubscribe = [
      observe(chatListKey, reads.chats),
      observe(archivedKey, reads.archived),
      observe(approvalsKey, reads.approvals),
    ];
    await vi.advanceTimersByTimeAsync(0);
    for (const read of Object.values(reads)) read.mockClear();

    useGlobalUpdatesStore.getState().handleUpdate(automationRuns(50));
    await vi.advanceTimersByTimeAsync(STREAM_REFETCH_INTERVAL_MS * 3);

    // Before: 50 ListChats, 50 ListArchivedChats, 50 ListApprovalsByChat.
    expect(reads.chats).toHaveBeenCalledTimes(1);
    expect(reads.archived).toHaveBeenCalledTimes(1);
    expect(reads.approvals).toHaveBeenCalledTimes(1);
    for (const stop of unsubscribe) stop();
  });

  it("updates arriving one by one over the interval still settle on the final list", async () => {
    vi.useFakeTimers();
    const listChats = vi.fn(async () => emptyList());
    const stop = new QueryObserver(queryClient, {
      queryKey: chatListKey,
      queryFn: listChats,
      staleTime: Infinity,
    }).subscribe(() => {});
    await vi.advanceTimersByTimeAsync(0);
    listChats.mockClear();

    // Two seconds of a replay trickling in, one update every 20ms (the
    // cadence the storm arrived at).
    const replay = automationRuns(34).filter((u) => u.update_type === UserUpdateType.CHAT_CREATED);
    for (const u of replay) {
      useGlobalUpdatesStore.getState().handleUpdate([u]);
      await vi.advanceTimersByTimeAsync(20);
    }
    await vi.advanceTimersByTimeAsync(STREAM_REFETCH_INTERVAL_MS * 3);

    // The first update refetches at once and the rest collapse into one
    // trailing refetch that started after the last of them.
    expect(listChats.mock.calls.length).toBeLessThanOrEqual(2);
    expect(queryClient.getQueryState(chatListKey)?.isInvalidated).toBe(false);
    stop();
  });

  it("the sidebar's ListChats is aborted when a refetch supersedes it", async () => {
    const signals: AbortSignal[] = [];
    vi.spyOn(chatActions, "listChats").mockImplementation((_projectId, options) => {
      if (options?.signal) signals.push(options.signal);
      return new Promise(() => {}); // never settles: still in flight when superseded
    });
    // The sidebar already holds a list (React Query only cancels a fetch it
    // is replacing when the query has data), read long enough ago to be
    // stale, so mounting refetches it.
    queryClient.setQueryData(chatListKey, emptyList(), { updatedAt: 1 });
    const wrapper = ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client: queryClient }, children);
    renderHook(() => useChatListWithWorkspaces("p1"), { wrapper });
    await vi.waitFor(() => expect(signals).toHaveLength(1));

    // Not awaited: the refetch it starts never settles either.
    void queryClient.invalidateQueries({ queryKey: chatKeys.lists() }, { cancelRefetch: true }).catch(() => {});
    await vi.waitFor(() => expect(signals).toHaveLength(2));

    // The superseded request is aborted, so the server stops reading for it.
    expect(signals[0]!.aborted).toBe(true);
    expect(signals[1]!.aborted).toBe(false);
  });
});
