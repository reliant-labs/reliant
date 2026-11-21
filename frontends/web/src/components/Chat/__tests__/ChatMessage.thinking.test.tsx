import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ChatMessage } from "../ChatMessage";
import { ContentBlockType, MessageRole, StreamingState } from "../../../types/chat";
import type { Message } from "../../../types/chat";

vi.mock("../MarkdownRenderer", () => ({
  MarkdownRenderer: ({ content, className }: { content: string; className?: string }) => (
    <div data-testid="markdown" className={className}>{content}</div>
  ),
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

const branchChat = vi.fn();
vi.mock("../../../store/chatStore", () => ({
  useChatStore: { getState: () => ({ branchChat }) },
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

function assistantWith(blocks: Message["contentBlocks"]): Message {
  return {
    id: "msg-think",
    chatId: "chat-1",
    seq: BigInt(1),
    thread: "chat-1",
    role: MessageRole.ASSISTANT,
    streamingState: StreamingState.COMPLETE,
    contentBlocks: blocks,
    createdAt: "2024-01-01T00:00:00.000Z",
    updatedAt: "2024-01-01T00:00:00.000Z",
    sequenceNumber: BigInt(1),
  } as Message;
}

describe("ChatMessage thinking", () => {
  it("shows readable thinking as a collapsed Thought row that opens onto it", () => {
    render(
      <ChatMessage
        chatId="chat-1"
        message={assistantWith([
          { id: "t", index: 0, type: ContentBlockType.THINKING, content: "The discount is taken per line." },
          { id: "b", index: 1, type: ContentBlockType.TEXT, content: "Found it." },
        ] as Message["contentBlocks"])}
      />,
    );
    const row = screen.getByRole("button", { name: "Show thinking" });
    expect(row).toHaveAttribute("aria-expanded", "false");
    expect(screen.getByText("Thought")).toBeInTheDocument();
    expect(screen.queryByText("The discount is taken per line.")).toBeNull();
    fireEvent.click(row);
    expect(screen.getByText("The discount is taken per line.")).toBeInTheDocument();
    expect(screen.getByText("Found it.")).toBeInTheDocument();
  });

  it("never renders redacted thinking, not even as a Thought row", () => {
    const { container } = render(
      <ChatMessage
        chatId="chat-1"
        message={assistantWith([
          { id: "r", index: 0, type: ContentBlockType.REDACTED_THINKING, content: "EuYBCkQ-opaque-ciphertext" },
          { id: "b", index: 1, type: ContentBlockType.TEXT, content: "Done." },
        ] as Message["contentBlocks"])}
      />,
    );
    expect(container.textContent).not.toContain("opaque-ciphertext");
    expect(screen.queryByTestId("thought-row")).toBeNull();
    expect(screen.getByText("Done.")).toBeInTheDocument();
  });
});
