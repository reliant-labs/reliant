import { render } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ChatMessage } from "../ChatMessage";
import {
  ContentBlockType,
  MessageRole,
  StreamingState,
} from "../../../types/chat";
import type { Message } from "../../../types/chat";

// Render the tool cards as identifiable markers so we can assert DOM order
// relative to the text runs. The bug this guards: a tool call that occurred
// mid-message rendered after all prose instead of between the paragraphs.
vi.mock("../ToolExecution", () => ({
  ToolExecution: ({ toolCall }: { toolCall: { name: string } }) => (
    <div data-testid="tool-card">tool:{toolCall.name}</div>
  ),
}));
vi.mock("../ToolExecutionGroup", () => ({
  ToolExecutionGroup: () => <div data-testid="tool-group">group</div>,
}));
vi.mock("../ToolExecutionCollapsibleGroup", () => ({
  ToolExecutionCollapsibleGroup: () => (
    <div data-testid="tool-collapsible-group">collapsible</div>
  ),
}));

vi.mock("../MarkdownRenderer", () => ({
  MarkdownRenderer: ({ content }: { content: string }) => (
    <div data-testid="text-run">{content}</div>
  ),
}));

vi.mock("../../../store/chatStoreHooks", () => ({
  useActiveChatId: () => "chat-1",
  useToolResultsByCallId: () => ({}),
  useToolCallStates: () => new Map(),
  useChat: () => ({ worktreeId: "worktree-1" }),
}));

vi.mock("../../../store/projectStore", () => ({
  useProjectStore: () => ({ id: "project-1" }),
}));

vi.mock("../../../store/chatStore", () => ({
  useChatStore: { getState: () => ({ branchChat: vi.fn() }) },
}));

vi.mock("../../../api/client", () => ({
  api: { toolCalls: { cancel: vi.fn(), convertToBackground: vi.fn() } },
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

function assistantMessage(): Message {
  return {
    id: "msg-1",
    chatId: "chat-1",
    ordinal: BigInt(1),
    thread: "chat-1",
    role: MessageRole.ASSISTANT,
    streamingState: StreamingState.COMPLETE,
    contentBlocks: [
      { id: "b0", index: 0, type: ContentBlockType.TEXT, content: "Here is the plan." },
      {
        id: "b1",
        index: 1,
        type: ContentBlockType.TOOL_CALL,
        toolName: "bash",
        toolCallId: "call-1",
        input: "{}",
      },
      { id: "b2", index: 2, type: ContentBlockType.TEXT, content: "All done." },
    ],
    createdAt: "2024-01-01T00:00:00.000Z",
    updatedAt: "2024-01-01T00:00:00.000Z",
    sequenceNumber: BigInt(1),
  } as Message;
}

describe("ChatMessage segment ordering", () => {
  it("renders a mid-message tool call between the surrounding text runs", () => {
    render(<ChatMessage message={assistantMessage()} chatId="chat-1" />);

    // Collect the rendered content markers in document order.
    const markers = Array.from(
      document.querySelectorAll(
        '[data-testid="text-run"], [data-testid="tool-card"]',
      ),
    ).map((el) => el.textContent);

    expect(markers).toEqual(["Here is the plan.", "tool:bash", "All done."]);
  });
});