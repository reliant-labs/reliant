import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ChatMessage } from "../ChatMessage";
import {
  ContentBlockType,
  MessageRole,
  StreamingState,
} from "../../../types/chat";
import type { Message } from "../../../types/chat";
import type { ToolCallState } from "../../../store/chatStore";

// A spawn's assistant message is persisted BEFORE the spawn runs, so the
// tool-call block a live client holds has no childWorkflowId. The id arrives
// afterwards on the tool-status channel. The card must receive it from there,
// or the spawn preview has no thread to read and renders "Starting…" over an
// agent that is actively working — until a reload re-reads the durable row.

const TOOL_CALL_ID = "toolu_01VVr3JpDYCBeAp4fHX6LbZv";
const CHILD_WORKFLOW_ID = "b760a4b2-471c-5c90-b30a-89e3effd1560";

let mockToolCallStates = new Map<string, ToolCallState>();

vi.mock("../ToolExecution", () => ({
  ToolExecution: ({
    toolCall,
  }: {
    toolCall: { name: string; childWorkflowId?: string };
  }) => (
    <div data-testid="tool-card">
      {toolCall.name}:{toolCall.childWorkflowId ?? "none"}
    </div>
  ),
}));
vi.mock("../ToolExecutionGroup", () => ({
  ToolExecutionGroup: () => <div data-testid="tool-group">group</div>,
}));
vi.mock("../ToolExecutionCollapsibleGroup", () => ({
  ToolExecutionCollapsibleGroup: () => <div>collapsible</div>,
}));
vi.mock("../MarkdownRenderer", () => ({
  MarkdownRenderer: ({ content }: { content: string }) => <div>{content}</div>,
}));

vi.mock("../../../store/chatStoreHooks", () => ({
  useActiveChatId: () => "chat-1",
  useToolResultsByCallId: () => ({}),
  useToolCallStates: () => mockToolCallStates,
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

/** The assistant message as persisted before the spawn ran: no childWorkflowId. */
function spawnMessage(childWorkflowId?: string): Message {
  return {
    id: "msg-1",
    chatId: "chat-1",
    seq: BigInt(1),
    thread: "chat-1",
    role: MessageRole.ASSISTANT,
    streamingState: StreamingState.COMPLETE,
    contentBlocks: [
      {
        id: "b0",
        index: 0,
        type: ContentBlockType.TOOL_CALL,
        toolName: "spawn",
        toolCallId: TOOL_CALL_ID,
        input: '{"title":"SWEEP"}',
        childWorkflowId,
      },
    ],
    createdAt: "2024-01-01T00:00:00.000Z",
    updatedAt: "2024-01-01T00:00:00.000Z",
    sequenceNumber: BigInt(1),
  } as Message;
}

function liveState(childWorkflowId?: string): Map<string, ToolCallState> {
  return new Map([
    [
      TOOL_CALL_ID,
      {
        id: TOOL_CALL_ID,
        sessionId: "chat-1",
        toolName: "spawn",
        status: "completed",
        timestamp: "t",
        childWorkflowId,
      } as ToolCallState,
    ],
  ]);
}

describe("ChatMessage spawn child workflow id", () => {
  it("hands the card the child workflow id the status event reported", () => {
    mockToolCallStates = liveState(CHILD_WORKFLOW_ID);

    render(<ChatMessage message={spawnMessage()} chatId="chat-1" />);

    expect(screen.getByTestId("tool-card").textContent).toBe(
      `spawn:${CHILD_WORKFLOW_ID}`,
    );
  });

  it("keeps the id a loaded block already carries when no live state exists", () => {
    mockToolCallStates = new Map();

    render(
      <ChatMessage message={spawnMessage(CHILD_WORKFLOW_ID)} chatId="chat-1" />,
    );

    expect(screen.getByTestId("tool-card").textContent).toBe(
      `spawn:${CHILD_WORKFLOW_ID}`,
    );
  });
});
