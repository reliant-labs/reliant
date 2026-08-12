/**
 * Flushing a queue the agent left behind.
 *
 * A queued message is delivered by exactly one mechanism: the agent's own
 * drain at its next loop-step boundary. The last boundary of a run never
 * arrives, so a message queued late in a run is stranded — the row stays in
 * the mailbox, the poll stops, and nothing on either side will ever send it.
 * The user's only recourse was to type something else to shake it loose,
 * which is not a recourse so much as a workaround for a message that was
 * accepted and then silently dropped on the floor.
 *
 * The strip flushes it instead, on both edges where a stranded queue can
 * exist: the run ending with rows still queued, and opening a chat that is
 * already idle and already has rows. What it must NOT do is act while the
 * agent is running — the drain handles that case correctly, and racing it is
 * how a message ends up delivered twice.
 */

import { screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi, beforeEach } from "vitest";
import { renderWithQuery } from "../../../test/renderWithQuery";
import type { QueuedAgentMessageView } from "../../../api/chat-grpc";

const CHAT_ID = "chat-abc";
const THREAD_ID = "thread-xyz";

const {
  claimQueuedAgentMessagesMock,
  cancelQueuedAgentMessageMock,
  toastInfoMock,
  toastErrorMock,
} = vi.hoisted(() => ({
  claimQueuedAgentMessagesMock: vi.fn(),
  cancelQueuedAgentMessageMock: vi.fn(),
  toastInfoMock: vi.fn(),
  toastErrorMock: vi.fn(),
}));

vi.mock("../../../api/chat-grpc", () => ({
  chatGrpc: {
    claimQueuedAgentMessages: claimQueuedAgentMessagesMock,
    cancelQueuedAgentMessage: cancelQueuedAgentMessageMock,
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

describe("auto-flushing a queue the agent will never drain", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    claimQueuedAgentMessagesMock.mockResolvedValue({ messages: [] });
  });

  it("sends a stranded queue on its own when the chat is already idle", async () => {
    claimQueuedAgentMessagesMock.mockResolvedValue({
      messages: [queued({ id: "q-1", body: "run the tests" })],
    });
    const onSendNow = vi.fn().mockResolvedValue(undefined);

    // Opening a chat that is already idle with a row left over from a run
    // that ended. Nobody clicks anything.
    renderWithQuery(
      <QueuedMessages
        chatId={CHAT_ID}
        threadId={THREAD_ID}
        messages={[queued({ id: "q-1", body: "run the tests" })]}
        onRefresh={vi.fn()}
        onSendNow={onSendNow}
        isRunning={false}
      />,
    );

    await waitFor(() => {
      expect(onSendNow).toHaveBeenCalledWith("run the tests", []);
    });
    // Through the claim, like every other send — never straight from the
    // rendered list.
    expect(claimQueuedAgentMessagesMock).toHaveBeenCalledWith(
      CHAT_ID,
      THREAD_ID,
      undefined,
    );
  });

  it("flushes on the running -> idle edge, once", async () => {
    claimQueuedAgentMessagesMock.mockResolvedValue({
      messages: [queued({ id: "q-1", body: "and check the logs" })],
    });
    const onSendNow = vi.fn().mockResolvedValue(undefined);

    const props = {
      chatId: CHAT_ID,
      threadId: THREAD_ID,
      messages: [queued({ id: "q-1", body: "and check the logs" })],
      onRefresh: vi.fn(),
      onSendNow,
    };

    const { rerender } = renderWithQuery(
      <QueuedMessages {...props} isRunning={true} />,
    );

    // While the agent runs, the drain owns the mailbox. Touching it here is
    // the double-delivery bug.
    expect(claimQueuedAgentMessagesMock).not.toHaveBeenCalled();

    rerender(<QueuedMessages {...props} isRunning={false} />);

    await waitFor(() => {
      expect(onSendNow).toHaveBeenCalledWith("and check the logs", []);
    });

    // Re-renders keep coming — a poll settling, the age label ticking. None of
    // them is a new transition, and each extra claim would be another chance
    // to send the user's message twice.
    rerender(<QueuedMessages {...props} isRunning={false} />);
    rerender(<QueuedMessages {...props} isRunning={false} />);
    await Promise.resolve();

    expect(claimQueuedAgentMessagesMock).toHaveBeenCalledTimes(1);
    expect(onSendNow).toHaveBeenCalledTimes(1);
  });

  it("sends exactly what the claim returned, not what the strip was showing", async () => {
    // The agent drained one row on its way out. The strip still renders both
    // because its last poll predates that. Only the claim knows the truth.
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
        isRunning={false}
      />,
    );

    await waitFor(() => {
      expect(onSendNow).toHaveBeenCalledTimes(1);
    });
    expect(onSendNow).toHaveBeenCalledWith("still mine", []);
    expect(onSendNow).not.toHaveBeenCalledWith("already drained", []);
  });

  it("stays out of the way while the agent is running", async () => {
    const onSendNow = vi.fn().mockResolvedValue(undefined);

    renderWithQuery(
      <QueuedMessages
        chatId={CHAT_ID}
        threadId={THREAD_ID}
        messages={[queued({ id: "q-1" })]}
        onRefresh={vi.fn()}
        onSendNow={onSendNow}
        isRunning={true}
      />,
    );

    // The row is visible and waiting, which is the correct state: the agent
    // will read it at its next boundary.
    expect(screen.getByText("check the migration file")).toBeTruthy();
    await Promise.resolve();
    expect(claimQueuedAgentMessagesMock).not.toHaveBeenCalled();
    expect(onSendNow).not.toHaveBeenCalled();
  });

  it("stays out of the way of a PAUSED run, which the composer reports as not-busy", async () => {
    // The trap this pins: isRunning is the composer's "busy" flag, and it is
    // deliberately false in discuss mode and while a question is pending so
    // the input stays enabled. Both of those sit on a PAUSED run — which
    // resumes and drains its mailbox normally. Flushing on isRunning alone
    // would claim rows out from under a live agent that was going to read
    // them, which is the exact theft the mailbox exists to prevent.
    const onSendNow = vi.fn().mockResolvedValue(undefined);

    renderWithQuery(
      <QueuedMessages
        chatId={CHAT_ID}
        threadId={THREAD_ID}
        messages={[queued({ id: "q-1" })]}
        onRefresh={vi.fn()}
        onSendNow={onSendNow}
        isRunning={false}
        isWorkflowLive={true}
      />,
    );

    await Promise.resolve();
    expect(claimQueuedAgentMessagesMock).not.toHaveBeenCalled();
    expect(onSendNow).not.toHaveBeenCalled();
  });

  it("flushes once the paused run finally reaches a dead end", async () => {
    claimQueuedAgentMessagesMock.mockResolvedValue({
      messages: [queued({ id: "q-1", body: "run the tests" })],
    });
    const onSendNow = vi.fn().mockResolvedValue(undefined);

    const props = {
      chatId: CHAT_ID,
      threadId: THREAD_ID,
      messages: [queued({ id: "q-1", body: "run the tests" })],
      onRefresh: vi.fn(),
      onSendNow,
      isRunning: false,
    };

    const { rerender } = renderWithQuery(
      <QueuedMessages {...props} isWorkflowLive={true} />,
    );
    expect(claimQueuedAgentMessagesMock).not.toHaveBeenCalled();

    // The run ends without ever resuming — now nothing will drain the row.
    rerender(<QueuedMessages {...props} isWorkflowLive={false} />);

    await waitFor(() => {
      expect(onSendNow).toHaveBeenCalledWith("run the tests", []);
    });
  });

  it("says nothing when an idle queue turns out to be already empty", async () => {
    // The drain got there first, so the claim comes back empty. On a click
    // that deserves "too late to pull back"; here nobody asked, and a toast
    // would be the UI talking to itself.
    claimQueuedAgentMessagesMock.mockResolvedValue({ messages: [] });
    const onSendNow = vi.fn().mockResolvedValue(undefined);

    renderWithQuery(
      <QueuedMessages
        chatId={CHAT_ID}
        threadId={THREAD_ID}
        messages={[queued({ id: "q-1" })]}
        onRefresh={vi.fn()}
        onSendNow={onSendNow}
        isRunning={false}
      />,
    );

    await waitFor(() => {
      expect(claimQueuedAgentMessagesMock).toHaveBeenCalled();
    });
    expect(onSendNow).not.toHaveBeenCalled();
    expect(toastInfoMock).not.toHaveBeenCalled();
  });

  it("surfaces a send that fails after the claim, instead of losing the message", async () => {
    // The rows are already off the mailbox at this point, so a silent failure
    // here would destroy them: not queued, not sent, not shown anywhere.
    claimQueuedAgentMessagesMock.mockResolvedValue({
      messages: [queued({ id: "q-1", body: "run the tests" })],
    });
    const onSendNow = vi.fn().mockRejectedValue(new Error("connection lost"));
    const onRefresh = vi.fn();

    renderWithQuery(
      <QueuedMessages
        chatId={CHAT_ID}
        threadId={THREAD_ID}
        messages={[queued({ id: "q-1", body: "run the tests" })]}
        onRefresh={onRefresh}
        onSendNow={onSendNow}
        isRunning={false}
      />,
    );

    await waitFor(() => {
      expect(toastErrorMock).toHaveBeenCalled();
    });
    expect(onRefresh).toHaveBeenCalled();
  });
});
