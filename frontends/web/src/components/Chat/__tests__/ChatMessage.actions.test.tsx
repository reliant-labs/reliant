import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ChatMessage } from "../ChatMessage";
import { ContentBlockType, MessageRole, StreamingState } from "../../../types/chat";
import type { Message } from "../../../types/chat";
import { SurfaceProvider } from "../../../lib/surfaceContext";

vi.mock("../MarkdownRenderer", () => ({
  MarkdownRenderer: ({ content }: { content: string }) => <div>{content}</div>,
}));

vi.mock("../../../store/chatStoreHooks", () => ({
  useActiveChatId: () => "chat-1",
  useChatIdOrActive: (chatId?: string | null) => chatId || "chat-1",
  useToolResultsByCallId: () => ({}),
  useToolCallStates: () => new Map(),
  useChat: () => ({ worktreeId: "worktree-1" }),
  useChatMessages: () => [],
  useStreamingMessages: () => [],
}));

vi.mock("../../../store/projectStore", () => ({
  useProjectStore: () => ({ id: "project-1" }),
}));

vi.mock("../../../store/chatStore", () => ({
  useChatStore: { getState: () => ({ branchChat: vi.fn() }) },
}));

vi.mock("../../../api/client", () => ({
  api: {},
}));

vi.mock("../../../lib/logger", () => ({
  logger: { debug: vi.fn(), error: vi.fn(), info: vi.fn(), warn: vi.fn() },
}));

vi.mock("../../../lib/toast-manager", () => ({ toast: { error: vi.fn() } }));

vi.mock("../../../lib/tabSwitchProfiler", () => ({
  tabSwitchProfiler: { isEnabled: () => false },
}));

vi.mock("../BranchOptionsMenu", () => ({ BranchOptionsMenu: () => null }));
vi.mock("../BranchToWorktreeModal", () => ({ BranchToWorktreeModal: () => null }));
vi.mock("../BranchToExistingWorktreeModal", () => ({
  BranchToExistingWorktreeModal: () => null,
}));
vi.mock("../CodeContextPill", () => ({ CodeContextPill: () => null }));

function buildMessage(role: MessageRole): Message {
  return {
    id: "msg-1",
    chatId: "chat-1",
    seq: BigInt(1),
    thread: "chat-1",
    role,
    streamingState: StreamingState.COMPLETE,
    contentBlocks: [
      { id: "b0", index: 0, type: ContentBlockType.TEXT, content: "Hello there." },
    ],
    createdAt: "2024-01-01T00:00:00.000Z",
    updatedAt: "2024-01-01T00:00:00.000Z",
    sequenceNumber: BigInt(1),
  } as Message;
}

function actionsRow(): HTMLElement {
  const el = screen
    .getByRole("button", { name: "Copy message" })
    .closest(".message-actions");
  if (!(el instanceof HTMLElement)) throw new Error("actions row not found");
  return el;
}

// §2.3 / §2.5 of the chat design: the actions sit in the flow, under the
// bubble or the answer. Nothing floats over the message, and no band is
// reserved above it.
describe("ChatMessage actions row", () => {
  it("puts copy, branch and the time under an answer, in the flow", () => {
    render(<ChatMessage message={buildMessage(MessageRole.ASSISTANT)} chatId="chat-1" isLatestMessage />);

    const row = actionsRow();
    expect(row).toHaveClass("flex", "h-7", "text-meta");
    expect(row).not.toHaveClass("absolute", "sticky");
    expect(row.parentElement).not.toHaveClass("absolute");
    expect(screen.getByRole("button", { name: "Branch from message" }).closest(".message-actions")).toBe(row);
    // Buttons first, then the time.
    expect(row.lastElementChild?.tagName).toBe("TIME");
    expect(row.textContent).toMatch(/ago/);
  });

  it("puts copy and the time under a user bubble, without branch", () => {
    render(<ChatMessage message={buildMessage(MessageRole.USER)} chatId="chat-1" isLatestMessage />);

    const row = actionsRow();
    expect(row.querySelector("time")).not.toBeNull();
    expect(screen.queryByRole("button", { name: "Branch from message" })).not.toBeInTheDocument();
  });

  it.each([
    ["assistant", MessageRole.ASSISTANT],
    ["user", MessageRole.USER],
  ])("keeps the latest %s turn's actions on screen", (_label, role) => {
    render(<ChatMessage message={buildMessage(role)} chatId="chat-1" isLatestMessage />);
    expect(actionsRow()).not.toHaveClass("opacity-0");
  });

  it.each([
    ["assistant", MessageRole.ASSISTANT],
    ["user", MessageRole.USER],
  ])("reveals an older %s turn's actions on hover or focus", (_label, role) => {
    render(<ChatMessage message={buildMessage(role)} chatId="chat-1" />);
    expect(actionsRow()).toHaveClass("opacity-0", "group-hover:opacity-100", "group-focus-within:opacity-100");
  });

  it("never shows the actions under a streaming answer", () => {
    render(
      <ChatMessage message={buildMessage(MessageRole.ASSISTANT)} chatId="chat-1" isLatestMessage isStreaming />,
    );
    expect(screen.queryByRole("button", { name: "Copy message" })).not.toBeInTheDocument();
  });

  it("leaves the actions to the row that ends the turn", () => {
    render(<ChatMessage message={buildMessage(MessageRole.ASSISTANT)} chatId="chat-1" turnEnd={false} />);
    expect(screen.queryByRole("button", { name: "Copy message" })).not.toBeInTheDocument();
  });

  it("reserves no band above the message", () => {
    const { container } = render(<ChatMessage message={buildMessage(MessageRole.ASSISTANT)} chatId="chat-1" />);
    expect(container.querySelector(".pt-7")).toBeNull();
  });

  it("shows no actions row on mobile, where the actions sheet holds them", () => {
    render(
      <SurfaceProvider surface="mobile">
        <ChatMessage message={buildMessage(MessageRole.ASSISTANT)} chatId="chat-1" isLatestMessage />
      </SurfaceProvider>,
    );

    expect(screen.queryByRole("button", { name: "Branch from message" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Message actions" })).toBeInTheDocument();
  });

  it("omits the actions entirely from the pinned header breadcrumb", () => {
    render(
      <ChatMessage message={buildMessage(MessageRole.USER)} chatId="chat-1" pinned />,
    );

    expect(screen.queryByRole("button", { name: "Copy message" })).not.toBeInTheDocument();
  });
});
