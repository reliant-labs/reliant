/**
 * request_machine renders as its own card in the message flow, not as a tool
 * row (research/NO_MACHINE_CHATS.md §3). It is a question to the user — "this
 * needs a machine: <reason>" with Connect a machine — and a collapsed tool
 * card is exactly where a question goes unseen.
 */

import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ChatMessage } from "../ChatMessage";
import { ContentBlockType, MessageRole, StreamingState } from "../../../types/chat";
import type { Message } from "../../../types/chat";

vi.mock("../../../store/chatStoreHooks", () => ({
  useActiveChatId: () => "chat-1",
  useToolResultsByCallId: () => ({}),
  useToolCallStates: () => new Map(),
  useChat: () => ({ worktreeId: "worktree-1" }),
  useChatMessages: () => [],
  useStreamingMessages: () => [],
}));
vi.mock("@/hooks/chat-queries", () => ({
  useChat: () => ({ data: { id: "chat-1", noMachine: true } }),
}));
vi.mock("../../../store/projectStore", () => ({ useProjectStore: () => ({ id: "project-1" }) }));
vi.mock("../../../store/chatStore", () => ({
  useChatStore: { getState: () => ({ branchChat: vi.fn(), connectChatToMachine: vi.fn() }) },
}));
vi.mock("../../../api/client", () => ({
  api: { toolCalls: { cancel: vi.fn(), convertToBackground: vi.fn() } },
}));
vi.mock("../../../lib/logger", () => ({
  logger: { debug: vi.fn(), error: vi.fn(), info: vi.fn(), warn: vi.fn() },
}));
vi.mock("../../../lib/toast-manager", () => ({ toast: { error: vi.fn() } }));
vi.mock("../../../lib/tabSwitchProfiler", () => ({ tabSwitchProfiler: { isEnabled: () => false } }));
vi.mock("../BranchOptionsMenu", () => ({ BranchOptionsMenu: () => null }));
vi.mock("../BranchToWorktreeModal", () => ({ BranchToWorktreeModal: () => null }));
vi.mock("../BranchToExistingWorktreeModal", () => ({ BranchToExistingWorktreeModal: () => null }));
vi.mock("../CodeContextPill", () => ({ CodeContextPill: () => null }));
vi.mock("../ConnectMachineDialog", () => ({ ConnectMachineDialog: () => null }));

function assistantAskingForAMachine(): Message {
  return {
    id: "msg-1",
    chatId: "chat-1",
    seq: BigInt(1),
    thread: "chat-1",
    role: MessageRole.ASSISTANT,
    streamingState: StreamingState.COMPLETE,
    contentBlocks: [
      { id: "b0", index: 0, type: ContentBlockType.TEXT, content: "I can look this up, but the fix needs your code." },
      {
        id: "b1",
        index: 1,
        type: ContentBlockType.TOOL_CALL,
        toolCallId: "call-1",
        toolName: "request_machine",
        input: JSON.stringify({ reason: "Fixing the failing test needs a checkout of the repository." }),
        matchedResult: { toolCallId: "call-1", content: "The user has been shown your reason.", isError: false },
      },
    ],
    createdAt: "2024-01-01T00:00:00.000Z",
    updatedAt: "2024-01-01T00:00:00.000Z",
    sequenceNumber: BigInt(1),
  } as unknown as Message;
}

describe("request_machine in the transcript", () => {
  it("renders the reason as a card with Connect a machine, outside the tool rows", () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={queryClient}>
        <ChatMessage message={assistantAskingForAMachine()} chatId="chat-1" />
      </QueryClientProvider>,
    );

    const card = screen.getByTestId("request-machine-card");
    expect(card).toHaveTextContent("This needs a machine");
    expect(card).toHaveTextContent("Fixing the failing test needs a checkout of the repository.");
    expect(screen.getByTestId("request-machine-connect")).toBeInTheDocument();
    expect(card.closest(".tool-executions-container")).toBeNull();
    expect(screen.queryByText("request_machine")).toBeNull();
  });
});
