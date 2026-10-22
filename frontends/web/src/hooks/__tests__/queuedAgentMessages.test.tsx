/**
 * The queued-mailbox hook's cache, and the poll that races it.
 *
 * The strip renders whatever this hook has in cache, so "the message I just
 * sent is showing twice" is a cache question, not a rendering one. The shape
 * of the bug: a poll is already in flight when the user claims a message. The
 * claim succeeds, the row is dropped from cache, the message is sent as a real
 * turn — and then the poll, which asked the server BEFORE the claim, resolves
 * with a snapshot that still contains it and writes that snapshot straight
 * into the cache. The row comes back, now alongside its own transcript entry.
 *
 * Dropping a message therefore has to outlive the responses that predate it,
 * which is what the tombstone set does. These tests pin that: a stale response
 * cannot resurrect a dropped id, and the tombstone lets go once the server has
 * caught up so the set cannot grow without bound.
 */

import type { ReactNode } from "react";
import { renderHook, waitFor, act } from "@testing-library/react";
import { describe, expect, it, vi, beforeEach } from "vitest";
import { QueryClientProvider } from "@tanstack/react-query";
import { createTestQueryClient } from "../../test/renderWithQuery";
import type { QueuedAgentMessageView } from "../../api/chat-grpc";

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
import { initEventBus } from "../../lib/events";

// The hook subscribes through the same singleton, which is idempotent — this
// is the bus the hook will use, not a second one.
const getEventBus = initEventBus;

const HUMAN = 5;

function queued(id: string, body = "check the migration file"): QueuedAgentMessageView {
  return {
    id,
    body,
    created_at: new Date(Date.now() - 3_000).toISOString(),
    sender_kind: HUMAN,
    attachments: [],
  };
}

/** A response whose resolution the test controls, so the race is deterministic. */
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

/**
 * One client for the whole render, not one per render. Building it inside the
 * wrapper's body would hand every re-render a fresh cache, which quietly turns
 * every cache assertion below into a test of nothing.
 */
function renderQueue(isRunning = true) {
  const queryClient = createTestQueryClient();
  return renderHook(
    () => useQueuedAgentMessages(CHAT_ID, THREAD_ID, isRunning),
    {
      wrapper: ({ children }: { children: ReactNode }) => (
        <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
      ),
    },
  );
}

describe("useQueuedAgentMessages", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    listQueuedAgentMessagesMock.mockResolvedValue({ messages: [] });
  });

  it("keeps a dropped message dropped when a poll from before the drop lands after it", async () => {
    // Poll #1 populates the strip.
    listQueuedAgentMessagesMock.mockResolvedValueOnce({ messages: [queued("q-1")] });

    const { result } = renderQueue();
    await waitFor(() => expect(result.current.messages).toHaveLength(1));

    // Poll #2 is now in flight, asking the server about a queue that still
    // contains q-1. It will not answer until this test says so.
    const inFlight = deferred<{ messages: QueuedAgentMessageView[] }>();
    listQueuedAgentMessagesMock.mockReturnValueOnce(inFlight.promise);
    act(() => {
      void result.current.refresh();
    });

    // The user claims q-1 and it is sent as a real turn. The strip must let go
    // of it right away — that part already worked.
    act(() => {
      result.current.forget("q-1");
    });
    await waitFor(() => expect(result.current.messages).toHaveLength(0));

    // The in-flight poll answers with its pre-claim snapshot. This is the
    // moment the row used to reappear, next to its own transcript entry.
    await act(async () => {
      inFlight.resolve({ messages: [queued("q-1")] });
      await inFlight.promise;
    });

    await waitFor(() => expect(result.current.messages).toHaveLength(0));
    expect(result.current.messages.map((m) => m.id)).not.toContain("q-1");
  });

  it("lets go of the tombstone once the server has caught up", async () => {
    // A tombstone that never expires is a leak: every claimed id in a long
    // session would be remembered forever. Once a response no longer mentions
    // the id, the server agrees it is gone and the tombstone has done its job.
    listQueuedAgentMessagesMock.mockResolvedValueOnce({ messages: [queued("q-1")] });

    const { result } = renderQueue();
    await waitFor(() => expect(result.current.messages).toHaveLength(1));

    act(() => {
      result.current.forget("q-1");
    });

    // The server has caught up: q-1 is gone from its answer.
    listQueuedAgentMessagesMock.mockResolvedValueOnce({ messages: [] });
    await act(async () => {
      await result.current.refresh();
    });
    await waitFor(() => expect(result.current.messages).toHaveLength(0));

    // With the tombstone released, nothing is filtering that id any more.
    listQueuedAgentMessagesMock.mockResolvedValueOnce({
      messages: [queued("q-1", "a genuinely new row")],
    });
    await act(async () => {
      await result.current.refresh();
    });
    await waitFor(() => expect(result.current.messages).toHaveLength(1));
  });

  // The bug the drain announcement exists for.
  //
  // The agent taking a message off the queue is the ORDINARY way a row leaves
  // the mailbox, and it used to be the one way that produced no signal at all.
  // The message became a transcript entry immediately; the strip kept showing
  // it until some later poll happened to answer without it. For that whole
  // window the user saw their message twice, in two different places.
  //
  // Without the drain subscription these three tests fail: the row is still in
  // the strip after the announcement, because only a poll could remove it.
  it("drops a message the moment the agent announces it drained it", async () => {
    listQueuedAgentMessagesMock.mockResolvedValue({ messages: [queued("q-1")] });

    const { result } = renderQueue();
    await waitFor(() => expect(result.current.messages).toHaveLength(1));

    // The drain committed. No poll has run since, and none needs to.
    act(() => {
      getEventBus().emit("agentMailbox:drained", {
        chatId: CHAT_ID,
        thread: THREAD_ID,
        messageIds: ["q-1"],
      });
    });

    await waitFor(() => expect(result.current.messages).toHaveLength(0));
  });

  // The drain races polls exactly the way a user claim does, so it needs the
  // same durability: a poll that asked before the drain must not resurrect the
  // row when it answers after it.
  it("keeps a drained message dropped when a poll from before the drain lands after it", async () => {
    listQueuedAgentMessagesMock.mockResolvedValueOnce({ messages: [queued("q-1")] });

    const { result } = renderQueue();
    await waitFor(() => expect(result.current.messages).toHaveLength(1));

    const inFlight = deferred<{ messages: QueuedAgentMessageView[] }>();
    listQueuedAgentMessagesMock.mockReturnValueOnce(inFlight.promise);
    act(() => {
      void result.current.refresh();
    });

    act(() => {
      getEventBus().emit("agentMailbox:drained", {
        chatId: CHAT_ID,
        thread: THREAD_ID,
        messageIds: ["q-1"],
      });
    });
    await waitFor(() => expect(result.current.messages).toHaveLength(0));

    await act(async () => {
      inFlight.resolve({ messages: [queued("q-1")] });
      await inFlight.promise;
    });

    await waitFor(() => expect(result.current.messages).toHaveLength(0));
    expect(result.current.messages.map((m) => m.id)).not.toContain("q-1");
  });

  // A drain announcement is addressed to one thread's mailbox. Applying
  // another's ids would tombstone rows this queue never had — and since a
  // tombstone is only released by a response that OMITS the id, a stray one
  // would sit in the set for the life of the thread.
  it("ignores a drain announcement addressed to another thread or chat", async () => {
    listQueuedAgentMessagesMock.mockResolvedValue({ messages: [queued("q-1")] });

    const { result } = renderQueue();
    await waitFor(() => expect(result.current.messages).toHaveLength(1));

    act(() => {
      getEventBus().emit("agentMailbox:drained", {
        chatId: CHAT_ID,
        thread: "some-other-thread",
        messageIds: ["q-1"],
      });
      getEventBus().emit("agentMailbox:drained", {
        chatId: "some-other-chat",
        thread: THREAD_ID,
        messageIds: ["q-1"],
      });
    });

    await waitFor(() => expect(result.current.messages).toHaveLength(1));
  });

  it("still drops a message that no poll ever raced", async () => {
    listQueuedAgentMessagesMock.mockResolvedValue({
      messages: [queued("q-1"), queued("q-2", "second")],
    });

    const { result } = renderQueue();
    await waitFor(() => expect(result.current.messages).toHaveLength(2));

    act(() => {
      result.current.forget("q-1");
    });

    await waitFor(() =>
      expect(result.current.messages.map((m) => m.id)).toEqual(["q-2"]),
    );
  });
});
