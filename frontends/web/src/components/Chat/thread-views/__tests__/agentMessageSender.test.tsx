/**
 * A message another agent sent (spawn_send) is role USER for the model, but it
 * must never render as the human's own bubble, never start a user turn and
 * never become the pinned user header. `sender` is what tells them apart.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { AgentMessageKind } from "../../../../gen/reliant/v1/chat_pb";
import { ContentBlockType, MessageRole, StreamingState } from "../../../../types/chat";
import type { Message } from "../../../../types/chat";
import { isAgentMessage } from "../../AgentMessageCard";
import { InterleavedTimeline } from "../InterleavedTimeline";

class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
globalThis.ResizeObserver ??= ResizeObserverStub as unknown as typeof ResizeObserver;

vi.mock("../../../../store/threadActivityStore", () => ({
  useActiveThreads: () => [],
}));

// The user bubble: a marker, so the test asserts WHICH renderer a message got.
vi.mock("../../ChatMessage", () => ({
  ChatMessage: ({ message, pinned }: { message: Message; pinned?: boolean }) => (
    <div data-testid={`user-or-assistant-${message.id}`} data-pinned={pinned ? "true" : "false"}>
      {message.id}
    </div>
  ),
}));

vi.mock("../../MarkdownRenderer", () => ({
  MarkdownRenderer: ({ content }: { content: string }) => <div>{content}</div>,
}));

vi.mock("@tanstack/react-virtual", () => ({
  useVirtualizer: (options: { count: number }) => ({
    getVirtualItems: () =>
      Array.from({ length: options.count }, (_, index) => ({
        index,
        key: index,
        start: 0,
        end: 0,
        size: 0,
        lane: 0,
      })),
    getTotalSize: () => 0,
    measureElement: () => {},
    containerRef: () => {},
    scrollToIndex: () => {},
    scrollToEnd: () => {},
  }),
}));

function message(index: number, role: MessageRole, overrides: Partial<Message> = {}): Message {
  return {
    id: `m${index}`,
    chatId: "chat-1",
    seq: BigInt(index),
    thread: "chat-1",
    role,
    streamingState: StreamingState.COMPLETE,
    contentBlocks: [
      { id: `m${index}-text`, index: 0, type: ContentBlockType.TEXT, content: `message ${index}` },
    ],
    createdAt: new Date(Date.UTC(2026, 0, 1, 0, 0, index)).toISOString(),
    updatedAt: new Date(Date.UTC(2026, 0, 1, 0, 0, index)).toISOString(),
    sequenceNumber: BigInt(index),
    ...overrides,
  } as Message;
}

const fromAgent = (index: number, body = `message ${index}`, subject = ""): Message =>
  message(index, MessageRole.USER, {
    contentBlocks: [{ id: `m${index}-text`, index: 0, type: ContentBlockType.TEXT, content: body }],
    sender: {
      $typeName: "reliant.v1.MessageSender",
      threadId: "thread-reviewer",
      title: "Reviewer",
      kind: AgentMessageKind.MESSAGE,
      subject,
    },
  });

beforeEach(() => {
  vi.clearAllMocks();
});

describe("isAgentMessage", () => {
  it("is true only when a sender is attached", () => {
    expect(isAgentMessage(fromAgent(1))).toBe(true);
    expect(isAgentMessage(message(2, MessageRole.USER))).toBe(false);
  });
});

describe("a sender-tagged USER message in the timeline", () => {
  it("renders as an agent card, not a user bubble", () => {
    render(
      <InterleavedTimeline
        messages={[message(0, MessageRole.USER), fromAgent(1)]}
        chatId="chat-1"
        isStreaming={false}
      />,
    );

    expect(screen.getByTestId("user-or-assistant-m0")).toBeTruthy();
    expect(screen.queryByTestId("user-or-assistant-m1")).toBeNull();
    const card = screen.getByTestId("agent-message-card");
    expect(card.textContent).toContain("from");
    expect(card.textContent).toContain("Reviewer");
    expect(card.textContent).toContain("message 1");
  });

  it("links the sender to its thread", () => {
    const onSelectThread = vi.fn();
    render(
      <InterleavedTimeline
        messages={[fromAgent(1)]}
        chatId="chat-1"
        isStreaming={false}
        onSelectThread={onSelectThread}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Reviewer" }));

    expect(onSelectThread).toHaveBeenCalledWith("thread-reviewer");
  });

  it("shows only the subject, collapsed, and expands to the full body", () => {
    render(
      <InterleavedTimeline
        messages={[fromAgent(1, "The full report.\n\nWith a second paragraph.", "Tests pass; PR ready")]}
        chatId="chat-1"
        isStreaming={false}
      />,
    );

    expect(screen.getByTestId("agent-message-subject").textContent).toBe("Tests pass; PR ready");
    expect(screen.queryByTestId("agent-message-body")).toBeNull();
    expect(screen.queryByText(/The full report/)).toBeNull();

    const toggle = screen.getByRole("button", { name: /Tests pass; PR ready/ });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    fireEvent.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByTestId("agent-message-body").textContent).toContain("With a second paragraph.");

    fireEvent.click(toggle);
    expect(screen.queryByTestId("agent-message-body")).toBeNull();
  });

  it("falls back to a truncated first line when the message has no subject", () => {
    render(
      <InterleavedTimeline
        messages={[fromAgent(1, `\n## ${"Long heading ".repeat(20)}\n\nbody text`)]}
        chatId="chat-1"
        isStreaming={false}
      />,
    );

    const subject = screen.getByTestId("agent-message-subject").textContent ?? "";
    expect(subject.startsWith("Long heading Long heading")).toBe(true);
    expect(subject.endsWith("…")).toBe(true);
    expect(subject.length).toBeLessThanOrEqual(100);
    expect(subject).not.toContain("#");
    expect(screen.queryByText(/body text/)).toBeNull();
  });

  it("does not take a user bubble's place as a turn boundary", () => {
    // Only the human's m0 may head a turn; the agent message m2 must not move
    // the layer header away from it.
    const { container } = render(
      <InterleavedTimeline
        messages={[message(0, MessageRole.USER), message(1, MessageRole.ASSISTANT), fromAgent(2)]}
        chatId="chat-1"
        isStreaming={false}
      />,
    );

    expect(container.querySelectorAll('[data-pinned="true"]')).toHaveLength(0);
    expect(screen.queryByTestId("user-or-assistant-m2")).toBeNull();
    expect(screen.getAllByTestId("agent-message-card")).toHaveLength(1);
  });
});
