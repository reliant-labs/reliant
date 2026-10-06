/**
 * A row arriving in a mailbox is pushed, not polled for.
 *
 * The server announces every enqueue (the composer, spawn_send, a spawn's
 * report, the reconciler) as a chat-scoped `agent_mailbox` refetch on the
 * chat's update stream. The strip must re-read on it — and it must do so
 * whether or not the agent is running, because a report can land in an idle
 * parent's mailbox and the old poll only ran while the agent worked.
 *
 * These drive the announcement through the chat store's real stream reducer,
 * so they cover the whole client half: stream update → refetch bus → query.
 */

import type { ReactNode } from "react";
import { act, renderHook } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createTestQueryClient } from "../../test/renderWithQuery";
import type { ChatUpdate } from "../../types/streaming";

const CHAT_ID = "chat-abc";
const THREAD_ID = "thread-xyz";

const { listQueuedAgentMessagesMock } = vi.hoisted(() => ({
  listQueuedAgentMessagesMock: vi.fn(),
}));

vi.mock("../../api/chat-grpc", () => ({
  chatGrpc: { listQueuedAgentMessages: listQueuedAgentMessagesMock },
  QUEUED_SENDER_KIND_HUMAN: 5,
}));

import { useQueuedAgentMessages } from "../queued-agent-messages";
import { useChatStore } from "../../store/chatStore";

function mailboxRefetch(): ChatUpdate {
  // What streaming-grpc hands the store for a REFETCH chat update: the JSON
  // payload spread onto the update.
  return {
    update_type: "refetch",
    id: `refetch-${Math.random()}`,
    type: "agent_mailbox",
  } as unknown as ChatUpdate;
}

function renderQueue(isRunning: boolean) {
  const queryClient = createTestQueryClient();
  return renderHook(() => useQueuedAgentMessages(CHAT_ID, THREAD_ID, isRunning), {
    wrapper: ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    ),
  });
}

async function settle(ms = 0) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

beforeEach(() => {
  vi.useFakeTimers();
  listQueuedAgentMessagesMock.mockReset();
  listQueuedAgentMessagesMock.mockResolvedValue({ messages: [] });
});

afterEach(() => {
  vi.useRealTimers();
});

describe("queued mailbox push", () => {
  it.each([
    ["running", true],
    ["idle", false],
  ])("re-reads the mailbox when this chat announces an enqueue (agent %s)", async (_label, running) => {
    renderQueue(running);
    await settle();
    expect(listQueuedAgentMessagesMock).toHaveBeenCalledTimes(1);

    listQueuedAgentMessagesMock.mockResolvedValue({
      messages: [
        { id: "m1", body: "report", created_at: new Date().toISOString(), sender_kind: 1, attachments: [] },
      ],
    });
    useChatStore.getState().processChatStreamUpdates(CHAT_ID, [mailboxRefetch()]);
    // The refetch bus debounces each (type, chat) by 300ms.
    await settle(350);

    expect(listQueuedAgentMessagesMock).toHaveBeenCalledTimes(2);
  });

  it("ignores another chat's announcement", async () => {
    renderQueue(true);
    await settle();

    useChatStore.getState().processChatStreamUpdates("chat-other", [mailboxRefetch()]);
    await settle(350);

    expect(listQueuedAgentMessagesMock).toHaveBeenCalledTimes(1);
  });

  it("does not re-read on a timer between announcements", async () => {
    renderQueue(true);
    await settle();

    // Well past the old 2.5s poll, short of the 60s fallback.
    await settle(30_000);

    expect(listQueuedAgentMessagesMock).toHaveBeenCalledTimes(1);
  });
});
