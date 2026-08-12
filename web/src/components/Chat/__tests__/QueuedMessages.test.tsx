/**
 * The pending-queue strip above the composer.
 *
 * A queued message lives in the agent's mailbox, not the transcript, so the
 * strip is the only place the user can see it — and the only place they can
 * take it back. The behaviors pinned here are the ones where getting it wrong
 * costs the user something real:
 *
 *  - a queued message is VISIBLE, with a live age, so "queued for delivery"
 *    is verifiable rather than a claim a toast made once.
 *  - Cancel that SUCCEEDS removes the entry.
 *  - Cancel that LOSES the race (success: false — the agent already drained
 *    it) does NOT remove the entry and says why. Pretending it worked would
 *    tell the user their message was revoked while the agent acts on it.
 *  - "Send now" / "Send all" CLAIM the messages in one atomic call and then
 *    resend exactly what came back. This used to be cancel-then-send from the
 *    client, which left a window between the two calls; a bulk version would
 *    have multiplied that window by the size of the queue. Resending the
 *    locally-rendered list instead of the claim's result is the specific
 *    mistake that lands a user's text in the conversation twice.
 *
 * The manual-send cases pass isRunning, and it is not decoration: those
 * buttons exist for a queue the agent is still going to read. An idle queue
 * flushes itself (queuedAutoFlush.test.tsx), so mounting one of these idle
 * would be testing the click against a strip that had already emptied itself.
 */

import { fireEvent, screen, waitFor, act } from "@testing-library/react";
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { renderWithQuery } from "../../../test/renderWithQuery";
import type { QueuedAgentMessageView } from "../../../api/chat-grpc";

const CHAT_ID = "chat-abc";
const THREAD_ID = "thread-xyz";

const {
  cancelQueuedAgentMessageMock,
  claimQueuedAgentMessagesMock,
  toastInfoMock,
  toastErrorMock,
} = vi.hoisted(() => ({
  cancelQueuedAgentMessageMock: vi.fn(),
  claimQueuedAgentMessagesMock: vi.fn(),
  toastInfoMock: vi.fn(),
  toastErrorMock: vi.fn(),
}));

// QUEUED_SENDER_KIND_HUMAN is a plain constant the component filters on, so
// the mock has to carry it through or every row would be filtered out and the
// tests would pass vacuously against an empty strip.
vi.mock("../../../api/chat-grpc", () => ({
  chatGrpc: {
    cancelQueuedAgentMessage: cancelQueuedAgentMessageMock,
    claimQueuedAgentMessages: claimQueuedAgentMessagesMock,
  },
  QUEUED_SENDER_KIND_HUMAN: 5,
}));

vi.mock("../../../lib/toast-manager", () => ({
  toast: {
    info: toastInfoMock,
    error: toastErrorMock,
    success: vi.fn(),
    warning: vi.fn(),
  },
}));

import { QueuedMessages } from "../QueuedMessages";

const HUMAN = 5;
const AGENT_TO_AGENT = 1;

function queued(
  overrides: Partial<QueuedAgentMessageView> = {},
): QueuedAgentMessageView {
  return {
    id: "queued-1",
    body: "check the migration file",
    created_at: new Date(Date.now() - 12_000).toISOString(),
    sender_kind: HUMAN,
    attachments: [],
    ...overrides,
  };
}

describe("QueuedMessages", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("renders nothing at all when the queue is empty", () => {
    const { container } = renderWithQuery(
      <QueuedMessages chatId={CHAT_ID} messages={[]} onRefresh={vi.fn()} />,
    );
    // Not "renders an empty box" — renders no box. A permanent gap above the
    // composer for a queue that is usually empty would be worse than nothing.
    expect(container).toBeEmptyDOMElement();
    expect(screen.queryByTestId("queued-messages")).toBeNull();
  });

  it("shows the queued text with its age, and ticks the age live", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const createdAt = new Date(Date.now() - 12_000).toISOString();

    renderWithQuery(
      <QueuedMessages
        chatId={CHAT_ID}
        messages={[queued({ created_at: createdAt })]}
        onRefresh={vi.fn()}
      />,
    );

    expect(screen.getByText("check the migration file")).toBeTruthy();
    expect(screen.getByText(/queued 12s ago/)).toBeTruthy();

    // The clock advances on its own — the label is not frozen at mount.
    await act(async () => {
      vi.advanceTimersByTime(5_000);
    });
    expect(screen.getByText(/queued 17s ago/)).toBeTruthy();
  });

  it("hides another agent's spawn_send message — it is not the human's to revoke", () => {
    renderWithQuery(
      <QueuedMessages
        chatId={CHAT_ID}
        messages={[
          queued({ id: "peer-1", body: "from a peer agent", sender_kind: AGENT_TO_AGENT }),
        ]}
        onRefresh={vi.fn()}
      />,
    );

    expect(screen.queryByText("from a peer agent")).toBeNull();
    expect(screen.queryByTestId("queued-messages")).toBeNull();
  });

  it("Cancel calls the RPC and drops the entry when the cancel takes", async () => {
    cancelQueuedAgentMessageMock.mockResolvedValue({ success: true, message: "Cancelled" });
    const onRefresh = vi.fn();
    const onForget = vi.fn();

    renderWithQuery(
      <QueuedMessages
        chatId={CHAT_ID}
        messages={[queued({ id: "queued-7" })]}
        onRefresh={onRefresh}
        onForget={onForget}
      />,
    );

    fireEvent.click(screen.getByLabelText("Cancel queued message"));

    await waitFor(() => {
      expect(cancelQueuedAgentMessageMock).toHaveBeenCalledWith(CHAT_ID, "queued-7");
    });
    await waitFor(() => {
      expect(onForget).toHaveBeenCalledWith("queued-7");
    });
    expect(onRefresh).toHaveBeenCalled();
    // No "already delivered" story to tell on the happy path.
    expect(toastInfoMock).not.toHaveBeenCalled();
  });

  it("Cancel on an already-delivered message keeps the entry and says why", async () => {
    cancelQueuedAgentMessageMock.mockResolvedValue({
      success: false,
      message: "Already delivered to the agent.",
    });
    const onRefresh = vi.fn();
    const onForget = vi.fn();

    renderWithQuery(
      <QueuedMessages
        chatId={CHAT_ID}
        messages={[queued({ id: "queued-7" })]}
        onRefresh={onRefresh}
        onForget={onForget}
      />,
    );

    fireEvent.click(screen.getByLabelText("Cancel queued message"));

    await waitFor(() => {
      expect(toastInfoMock).toHaveBeenCalledWith("Already delivered to the agent.");
    });
    // The row still stands on the server, so the UI must not claim otherwise.
    expect(onForget).not.toHaveBeenCalled();
    expect(onRefresh).toHaveBeenCalled();
    expect(screen.getByText("check the migration file")).toBeTruthy();
  });

  it("Send now claims the message atomically, then sends what it got back", async () => {
    claimQueuedAgentMessagesMock.mockResolvedValue({
      messages: [queued({ id: "queued-7", body: "run the tests" })],
    });
    const onSendNow = vi.fn().mockResolvedValue(undefined);

    renderWithQuery(
      <QueuedMessages
        chatId={CHAT_ID}
        threadId={THREAD_ID}
        messages={[queued({ id: "queued-7", body: "run the tests" })]}
        onRefresh={vi.fn()}
        onSendNow={onSendNow}
        isRunning={true}
      />,
    );

    fireEvent.click(screen.getByLabelText("Send now"));

    await waitFor(() => {
      expect(onSendNow).toHaveBeenCalledWith("run the tests", []);
    });
    expect(claimQueuedAgentMessagesMock).toHaveBeenCalledWith(CHAT_ID, THREAD_ID, "queued-7");
    expect(onSendNow).toHaveBeenCalledTimes(1);
  });

  it("Send now does NOT send when the drain won the message first", async () => {
    // An empty claim is the whole verdict: the row was already drained, so
    // this caller owns nothing and may resend nothing. There is no second
    // call whose answer could disagree.
    claimQueuedAgentMessagesMock.mockResolvedValue({ messages: [] });
    const onSendNow = vi.fn().mockResolvedValue(undefined);

    renderWithQuery(
      <QueuedMessages
        chatId={CHAT_ID}
        threadId={THREAD_ID}
        messages={[queued({ id: "queued-7", body: "run the tests" })]}
        onRefresh={vi.fn()}
        onSendNow={onSendNow}
        isRunning={true}
      />,
    );

    fireEvent.click(screen.getByLabelText("Send now"));

    await waitFor(() => {
      expect(toastInfoMock).toHaveBeenCalled();
    });
    // The agent already has this message. Sending it now would put the user's
    // text into the conversation a second time.
    expect(onSendNow).not.toHaveBeenCalled();
  });

  it("Send all flushes the whole queue in one claim, in order", async () => {
    claimQueuedAgentMessagesMock.mockResolvedValue({
      messages: [
        queued({ id: "q-1", body: "first" }),
        queued({ id: "q-2", body: "second" }),
      ],
    });
    const onSendNow = vi.fn().mockResolvedValue(undefined);

    renderWithQuery(
      <QueuedMessages
        chatId={CHAT_ID}
        threadId={THREAD_ID}
        messages={[
          queued({ id: "q-1", body: "first" }),
          queued({ id: "q-2", body: "second" }),
        ]}
        onRefresh={vi.fn()}
        onSendNow={onSendNow}
        isRunning={true}
      />,
    );

    fireEvent.click(screen.getByLabelText("Send all queued messages now"));

    await waitFor(() => {
      expect(onSendNow).toHaveBeenCalledTimes(2);
    });
    // ONE claim for the whole queue, not one per message: a per-message loop
    // would reintroduce the partial-failure window this replaced.
    expect(claimQueuedAgentMessagesMock).toHaveBeenCalledTimes(1);
    expect(claimQueuedAgentMessagesMock).toHaveBeenCalledWith(CHAT_ID, THREAD_ID, undefined);
    expect(onSendNow.mock.calls.map((c) => c[0])).toEqual(["first", "second"]);
  });

  it("Send all resends only what the claim returned, not the local list", async () => {
    // The strip still shows both, but the agent drained one before the click
    // landed — so the claim returns one. Resending from the rendered list
    // would put an already-delivered message into the conversation twice.
    claimQueuedAgentMessagesMock.mockResolvedValue({
      messages: [queued({ id: "q-2", body: "still mine" })],
    });
    const onSendNow = vi.fn().mockResolvedValue(undefined);

    renderWithQuery(
      <QueuedMessages
        chatId={CHAT_ID}
        threadId={THREAD_ID}
        messages={[
          queued({ id: "q-1", body: "already drained" }),
          queued({ id: "q-2", body: "still mine" }),
        ]}
        onRefresh={vi.fn()}
        onSendNow={onSendNow}
        isRunning={true}
      />,
    );

    fireEvent.click(screen.getByLabelText("Send all queued messages now"));

    await waitFor(() => {
      expect(onSendNow).toHaveBeenCalledTimes(1);
    });
    expect(onSendNow).toHaveBeenCalledWith("still mine", []);
    expect(onSendNow).not.toHaveBeenCalledWith("already drained", []);
  });

  it("caps the list height and scrolls, so the queue cannot push the composer off screen", () => {
    // The strip sits directly above the composer. Without a cap, a queue of
    // long messages grew until the transcript was unreachable — the bug this
    // pins. The dock stays a fixed size and scrolls internally instead.
    renderWithQuery(
      <QueuedMessages
        chatId={CHAT_ID}
        threadId={THREAD_ID}
        messages={Array.from({ length: 12 }, (_, i) =>
          queued({ id: `q-${i}`, body: `queued message ${i}` }),
        )}
        onRefresh={vi.fn()}
      />,
    );

    const list = screen.getByTestId("queued-messages-list");
    expect(list.className).toContain("overflow-y-auto");
    expect(list.className).toMatch(/max-h-/);
    // Every entry is still present — capped means scrollable, not truncated.
    expect(screen.getAllByTestId("queued-message")).toHaveLength(12);
  });

  it("collapses a long body behind Show more, and expands it in place", () => {
    const longBody = "x".repeat(400);
    renderWithQuery(
      <QueuedMessages
        chatId={CHAT_ID}
        threadId={THREAD_ID}
        messages={[queued({ id: "q-long", body: longBody })]}
        onRefresh={vi.fn()}
      />,
    );

    const body = screen.getByText(longBody);
    expect(body.className).toContain("line-clamp-3");

    fireEvent.click(screen.getByText("Show more"));
    expect(body.className).not.toContain("line-clamp-3");
    // Expanded is still bounded: opening a huge message must not reintroduce
    // the unbounded growth the cap exists to prevent.
    expect(body.className).toMatch(/max-h-/);
    expect(body.className).toContain("overflow-y-auto");

    fireEvent.click(screen.getByText("Show less"));
    expect(body.className).toContain("line-clamp-3");
  });

  it("leaves an ordinary short message alone", () => {
    // The clamp is for pasted logs and diffs. A normal steering message
    // should not grow a toggle it does not need.
    renderWithQuery(
      <QueuedMessages
        chatId={CHAT_ID}
        threadId={THREAD_ID}
        messages={[queued({ id: "q-short", body: "check the logs first" })]}
        onRefresh={vi.fn()}
      />,
    );

    expect(screen.queryByText("Show more")).toBeNull();
    expect(screen.getByText("check the logs first").className).not.toContain(
      "line-clamp-3",
    );
  });

  it("offers Send all only when more than one message is queued", () => {
    const { rerender } = renderWithQuery(
      <QueuedMessages
        chatId={CHAT_ID}
        threadId={THREAD_ID}
        messages={[queued({ id: "q-1" })]}
        onRefresh={vi.fn()}
        onSendNow={vi.fn()}
      />,
    );
    expect(screen.queryByLabelText("Send all queued messages now")).toBeNull();

    rerender(
      <QueuedMessages
        chatId={CHAT_ID}
        threadId={THREAD_ID}
        messages={[queued({ id: "q-1" }), queued({ id: "q-2" })]}
        onRefresh={vi.fn()}
        onSendNow={vi.fn()}
      />,
    );
    expect(screen.getByLabelText("Send all queued messages now")).toBeTruthy();
  });

  it("surfaces a failed cancel RPC instead of silently dropping the entry", async () => {
    cancelQueuedAgentMessageMock.mockRejectedValue(new Error("connection lost"));
    const onForget = vi.fn();

    renderWithQuery(
      <QueuedMessages
        chatId={CHAT_ID}
        messages={[queued()]}
        onRefresh={vi.fn()}
        onForget={onForget}
      />,
    );

    fireEvent.click(screen.getByLabelText("Cancel queued message"));

    await waitFor(() => {
      expect(toastErrorMock).toHaveBeenCalled();
    });
    expect(onForget).not.toHaveBeenCalled();
    expect(screen.getByText("check the migration file")).toBeTruthy();
  });
});
