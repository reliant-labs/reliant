/**
 * The pending-queue strip above the composer.
 *
 * A queued message lives in the agent's mailbox, not the transcript, so the
 * strip is the only place the user can see it — and the only place they can
 * take it back. The behaviors pinned here are the ones where getting it wrong
 * costs the user something real:
 *
 *  - a queued message is VISIBLE, with a live age, so delivery state is
 *    verifiable without a transient toast.
 *  - Cancel that SUCCEEDS removes the entry.
 *  - Cancel that LOSES the race (success: false — the agent already drained
 *    it) does NOT remove the entry. Pretending it worked would tell the user
 *    their message was revoked while the agent acts on it.
 *  - INTERRUPT stops the work in flight so the agent reads the queue on its
 *    next turn. It does NOT pull messages back out of the mailbox and resend
 *    them: the agent's own call_llm delivers the whole queue, in order. An
 *    earlier design claimed rows client-side and resent them, which had to
 *    race the agent's delivery and could land a user's text in the
 *    conversation twice.
 *
 * The interrupt cases pass isRunning, and it is not decoration: there is only
 * something to interrupt while the agent is working. An idle agent's queue is
 * absorbed by the next ordinary send, server-side.
 */

import { fireEvent, screen, waitFor, act } from "@testing-library/react";
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { renderWithQuery } from "../../../test/renderWithQuery";
import type { QueuedAgentMessageView } from "../../../api/chat-grpc";

const CHAT_ID = "chat-abc";
const THREAD_ID = "thread-xyz";

const {
  cancelQueuedAgentMessageMock,
  interruptThreadMock,
  toastInfoMock,
  toastWarningMock,
  toastErrorMock,
} = vi.hoisted(() => ({
  cancelQueuedAgentMessageMock: vi.fn(),
  interruptThreadMock: vi.fn(),
  toastInfoMock: vi.fn(),
  toastWarningMock: vi.fn(),
  toastErrorMock: vi.fn(),
}));

// QUEUED_SENDER_KIND_HUMAN is a plain constant the component filters on, so
// the mock has to carry it through or every row would be filtered out and the
// tests would pass vacuously against an empty strip.
vi.mock("../../../api/chat-grpc", () => ({
  chatGrpc: {
    cancelQueuedAgentMessage: cancelQueuedAgentMessageMock,
    interruptThread: interruptThreadMock,
  },
  QUEUED_SENDER_KIND_HUMAN: 5,
}));

vi.mock("../../../lib/toast-manager", () => ({
  toast: {
    info: toastInfoMock,
    error: toastErrorMock,
    success: vi.fn(),
    warning: toastWarningMock,
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

  it("Cancel on an already-delivered message keeps the entry without a toast", async () => {
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
      expect(onRefresh).toHaveBeenCalled();
    });
    // The row still stands on the server, so the UI must not claim otherwise.
    expect(onForget).not.toHaveBeenCalled();
    expect(toastInfoMock).not.toHaveBeenCalled();
    expect(screen.getByText("check the migration file")).toBeTruthy();
  });

  it("Interrupt stops the agent so it reads the queue on its next turn", async () => {
    interruptThreadMock.mockResolvedValue({
      cancelledToolCalls: 2,
      undeliverableToolCalls: [],
    });
    const onInterrupted = vi.fn().mockResolvedValue(undefined);
    const onRefresh = vi.fn().mockResolvedValue(undefined);

    renderWithQuery(
      <QueuedMessages
        chatId={CHAT_ID}
        threadId={THREAD_ID}
        messages={[queued({ id: "queued-7", body: "run the tests" })]}
        onRefresh={onRefresh}
        onInterrupted={onInterrupted}
        isRunning
      />,
    );

    fireEvent.click(
      screen.getByLabelText("Interrupt the agent and deliver queued messages now"),
    );

    await waitFor(() => {
      expect(onInterrupted).toHaveBeenCalled();
    });

    // Per THREAD, not per message: interrupt takes no message id, because the
    // agent delivers its whole mailbox on the turn this unblocks.
    expect(interruptThreadMock).toHaveBeenCalledWith(CHAT_ID, THREAD_ID);
    expect(interruptThreadMock).toHaveBeenCalledTimes(1);
    expect(onRefresh).toHaveBeenCalled();
  });

  it("interrupts without non-error toasts for queue delivery", async () => {
    interruptThreadMock.mockResolvedValue({
      cancelledToolCalls: 1,
      undeliverableToolCalls: ["toolu_stuck"],
    });
    const onInterrupted = vi.fn().mockResolvedValue(undefined);
    const onRefresh = vi.fn().mockResolvedValue(undefined);

    renderWithQuery(
      <QueuedMessages
        chatId={CHAT_ID}
        threadId={THREAD_ID}
        messages={[queued({ id: "q-1", body: "stop that" })]}
        onRefresh={onRefresh}
        onInterrupted={onInterrupted}
        isRunning
      />,
    );

    fireEvent.click(
      screen.getByLabelText("Interrupt the agent and deliver queued messages now"),
    );

    await waitFor(() => {
      expect(onInterrupted).toHaveBeenCalled();
    });
    expect(onRefresh).toHaveBeenCalled();
    expect(toastInfoMock).not.toHaveBeenCalled();
    expect(toastWarningMock).not.toHaveBeenCalled();
  });

  it("interrupting with nothing in flight is still a success", async () => {
    // The agent was between tool calls. Not a failure: the queue is delivered
    // on its next turn regardless, and the strip remains the confirmation.
    interruptThreadMock.mockResolvedValue({
      cancelledToolCalls: 0,
      undeliverableToolCalls: [],
    });
    const onInterrupted = vi.fn().mockResolvedValue(undefined);

    renderWithQuery(
      <QueuedMessages
        chatId={CHAT_ID}
        threadId={THREAD_ID}
        messages={[queued({ id: "q-1", body: "one more thing" })]}
        onRefresh={vi.fn().mockResolvedValue(undefined)}
        onInterrupted={onInterrupted}
        isRunning
      />,
    );

    fireEvent.click(
      screen.getByLabelText("Interrupt the agent and deliver queued messages now"),
    );

    await waitFor(() => {
      expect(onInterrupted).toHaveBeenCalled();
    });
    expect(toastErrorMock).not.toHaveBeenCalled();
  });

  it("surfaces a failed interrupt instead of pretending the agent stopped", async () => {
    interruptThreadMock.mockRejectedValue(new Error("network down"));
    const onInterrupted = vi.fn().mockResolvedValue(undefined);

    renderWithQuery(
      <QueuedMessages
        chatId={CHAT_ID}
        threadId={THREAD_ID}
        messages={[queued({ id: "q-1", body: "stop" })]}
        onRefresh={vi.fn().mockResolvedValue(undefined)}
        onInterrupted={onInterrupted}
        isRunning
      />,
    );

    fireEvent.click(
      screen.getByLabelText("Interrupt the agent and deliver queued messages now"),
    );

    await waitFor(() => {
      expect(toastErrorMock).toHaveBeenCalled();
    });
    // The agent was NOT interrupted, so nothing downstream may act as if it was.
    expect(onInterrupted).not.toHaveBeenCalled();
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

  it("offers Interrupt only while the agent is actually working", () => {
    // An idle agent has no work in flight to stop, and its queue is absorbed
    // by the next ordinary send server-side. Offering the button there would
    // promise something the click cannot deliver.
    const { rerender } = renderWithQuery(
      <QueuedMessages
        chatId={CHAT_ID}
        threadId={THREAD_ID}
        messages={[queued({ id: "q-1" })]}
        onRefresh={vi.fn()}
        onInterrupted={vi.fn()}
      />,
    );
    expect(
      screen.queryByLabelText("Interrupt the agent and deliver queued messages now"),
    ).toBeNull();

    rerender(
      <QueuedMessages
        chatId={CHAT_ID}
        threadId={THREAD_ID}
        messages={[queued({ id: "q-1" })]}
        onRefresh={vi.fn()}
        onInterrupted={vi.fn()}
        isRunning
      />,
    );
    expect(
      screen.getByLabelText("Interrupt the agent and deliver queued messages now"),
    ).toBeTruthy();
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
