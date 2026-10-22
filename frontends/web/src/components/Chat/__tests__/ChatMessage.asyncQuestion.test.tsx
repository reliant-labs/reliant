/**
 * An async ask_user (the default: no blocking: true) renders in the transcript as its own
 * answerable card, outside the tool rows; a blocking one renders nowhere in
 * the transcript — the composer answers it (research/ASYNC_ASK_USER.md).
 */

import { render, screen, waitFor } from "@testing-library/react";
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
vi.mock("../../../api/question-grpc", () => ({
  questionGrpc: {
    listPendingQuestions: vi.fn().mockResolvedValue([]),
    listAsyncQuestions: vi.fn().mockResolvedValue([
      {
        question_id: "q-1", chat_id: "chat-1", workflow_id: "chat-1", thread_id: "chat-1", step_id: "execute_tools",
        status: "pending", created_at: "", async: true, tool_call_id: "call-async",
      },
    ]),
    resolveQuestion: vi.fn(),
  },
}));

function assistantAsking(id: string, input: Record<string, unknown>): Message {
  return {
    id: `msg-${id}`,
    chatId: "chat-1",
    seq: BigInt(1),
    thread: "chat-1",
    role: MessageRole.ASSISTANT,
    streamingState: StreamingState.COMPLETE,
    contentBlocks: [
      { id: "b0", index: 0, type: ContentBlockType.TEXT, content: "Going with Postgres for now." },
      {
        id: "b1",
        index: 1,
        type: ContentBlockType.TOOL_CALL,
        toolCallId: id,
        toolName: "ask_user",
        input: JSON.stringify(input),
        matchedResult: { toolCallId: id, content: "Question posted to the user without waiting.", isError: false },
      },
    ],
    createdAt: "2024-01-01T00:00:00.000Z",
    updatedAt: "2024-01-01T00:00:00.000Z",
    sequenceNumber: BigInt(1),
  } as unknown as Message;
}

const questions = [{ question: "Which database?", options: [{ label: "Postgres", description: "" }, { label: "SQLite", description: "" }] }];

function renderMessage(message: Message) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <ChatMessage message={message} chatId="chat-1" />
    </QueryClientProvider>,
  );
}

describe("ask_user in the transcript", () => {
  it("renders an async question as an answerable card, outside the tool rows", async () => {
    renderMessage(assistantAsking("call-async", { questions }));

    const card = await screen.findByTestId("async-question-card");
    expect(card).toHaveTextContent("Question for you · not blocking");
    expect(await screen.findByRole("button", { name: /submit/i })).toBeInTheDocument();
    expect(card.closest(".tool-executions-container")).toBeNull();
  });

  it("leaves a blocking question out of the transcript", () => {
    renderMessage(assistantAsking("call-blocking", { blocking: true, questions }));

    expect(screen.queryByTestId("async-question-card")).toBeNull();
    expect(screen.queryByText("Which database?")).toBeNull();
  });

  // Recorded before async became the default: no flag, yet it blocked, so it
  // finished with no async question row.
  it("shows no card for a finished call without an async question", async () => {
    renderMessage(assistantAsking("call-old-blocking", { questions }));

    await waitFor(() => expect(screen.queryByTestId("async-question-card")).toBeNull());
  });
});
