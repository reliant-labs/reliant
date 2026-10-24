/**
 * Every ask_user call renders in the transcript as its own card, outside the
 * tool rows, and never as nothing. An async one (the default: no blocking:
 * true) is answerable there; a blocking one shows its question while the
 * composer holds the form (research/ASYNC_ASK_USER.md).
 */

import { render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
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
setTransport(
  mockTransport({
    "QuestionService.ListPendingQuestions": { response: { questions: [] } },
    "QuestionService.ListAsyncQuestions": {
      response: {
        questions: [
          {
            questionId: "q-1", chatId: "chat-1", workflowId: "chat-1", threadId: "chat-1", stepId: "execute_tools",
            status: "pending", createdAt: "", async: true, toolCallId: "call-async",
          },
          {
            questionId: "q-prod", chatId: "chat-1", workflowId: "chat-1", threadId: "chat-1", stepId: "execute_tools",
            status: "pending", createdAt: "", async: true, toolCallId: "toolu_015apcEoACxzEnwZDGZw4LBo",
          },
        ],
      },
    },
    "QuestionService.ResolveQuestion": { response: {} },
  }),
);

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

    const card = await screen.findByTestId("ask-user-card");
    expect(card).toHaveTextContent("Question for you · not blocking");
    expect(await screen.findByRole("button", { name: /submit/i })).toBeInTheDocument();
    expect(card.closest(".tool-executions-container")).toBeNull();
  });

  it("shows a blocking question's text, with the form left to the composer", async () => {
    renderMessage(assistantAsking("call-blocking", { blocking: true, questions }));

    const card = await screen.findByTestId("ask-user-card");
    expect(card).toHaveTextContent("Which database?");
    expect(screen.queryByRole("button", { name: /submit/i })).toBeNull();
  });

  // Recorded before async became the default: no flag, yet it blocked, so it
  // finished with no async question row. It still shows what was asked.
  it("shows the question of a finished call without an async question", async () => {
    renderMessage(assistantAsking("call-old-blocking", { questions }));

    await waitFor(() => expect(screen.getByTestId("ask-user-card")).toHaveAttribute("data-mode", "asked"));
    expect(screen.getByTestId("ask-user-card")).toHaveTextContent("Which database?");
  });

  // The exact prod shape (chat 641e33c9, 2026-10-22): `questions` is a
  // JSON-encoded string, there is no blocking flag, and the result is in a
  // separate tool message, so the call block carries none.
  it("renders the prod ask_user: string-encoded questions, async row found by tool call id", async () => {
    const id = "toolu_015apcEoACxzEnwZDGZw4LBo";
    const message = assistantAsking(id, { questions: JSON.stringify(questions) });
    const block = message.contentBlocks![1] as unknown as Record<string, unknown>;
    delete block.matchedResult;
    message.contentBlocks = [message.contentBlocks![1]];
    renderMessage(message);

    const card = await screen.findByTestId("ask-user-card");
    expect(card).toHaveTextContent("Which database?");
    await waitFor(() => expect(card).toHaveAttribute("data-mode", "async"));
    expect(await screen.findByRole("button", { name: /submit/i })).toBeInTheDocument();
  });
});
