/**
 * The timeline card of an async ask_user (input async: true): the agent asked
 * without waiting, and the user answers here whenever they like. The answer
 * goes through ResolveQuestion — the Inbox's call — and the card then shows
 * it. research/ASYNC_ASK_USER.md.
 */

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { QuestionInfo } from "../../../api/question-grpc";
import { queryClient } from "../../../lib/query-client";

const { listAsyncQuestionsMock, resolveQuestionMock } = vi.hoisted(() => ({
  listAsyncQuestionsMock: vi.fn(),
  resolveQuestionMock: vi.fn(),
}));

vi.mock("../../../api/question-grpc", () => ({
  questionGrpc: {
    listPendingQuestions: vi.fn().mockResolvedValue([]),
    listAsyncQuestions: listAsyncQuestionsMock,
    resolveQuestion: resolveQuestionMock,
  },
}));
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn() } }));

import { AsyncQuestionCard } from "../AsyncQuestionCard";

const CHAT_ID = "chat-card";
const TOOL_CALL = "call_async_1";
const input = {
  async: true,
  questions: [
    {
      question: "Which database?",
      options: [
        { label: "Postgres (Recommended)", description: "What we use elsewhere" },
        { label: "SQLite", description: "Smaller" },
      ],
    },
  ],
};

function asyncQuestion(overrides: Partial<QuestionInfo> = {}): QuestionInfo {
  return {
    question_id: "q-1",
    chat_id: CHAT_ID,
    workflow_id: CHAT_ID,
    thread_id: CHAT_ID,
    step_id: "execute_tools",
    status: "pending",
    created_at: "2026-10-22T00:00:00Z",
    async: true,
    tool_call_id: TOOL_CALL,
    ...overrides,
  };
}

function renderCard() {
  return render(
    <QueryClientProvider client={queryClient}>
      <AsyncQuestionCard chatId={CHAT_ID} toolCallId={TOOL_CALL} input={input} />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  queryClient.clear();
  resolveQuestionMock.mockResolvedValue({ success: true });
});
afterEach(() => queryClient.clear());

describe("AsyncQuestionCard", () => {
  it("is answerable while pending, through ResolveQuestion, and then shows the answer", async () => {
    listAsyncQuestionsMock.mockResolvedValue([asyncQuestion()]);
    const user = userEvent.setup();
    renderCard();

    expect(await screen.findByText("Question for you · not blocking")).toBeInTheDocument();
    await user.click(await screen.findByText("SQLite"));
    await user.click(screen.getByRole("button", { name: /submit/i }));

    await waitFor(() => expect(resolveQuestionMock).toHaveBeenCalledTimes(1));
    const [questionId, action, responseData] = resolveQuestionMock.mock.calls[0];
    expect(questionId).toBe("q-1");
    expect(action).toBe("reply");
    expect(JSON.parse(responseData)).toEqual({
      answers: [{ question: "Which database?", selected: ["SQLite"] }],
    });

    expect(await screen.findByText("Question answered")).toBeInTheDocument();
    expect(screen.getByTestId("async-question-answers")).toHaveTextContent("SQLite");
    expect(screen.queryByRole("button", { name: /submit/i })).toBeNull();
  });

  it("shows an answered question's answer, with no form", async () => {
    listAsyncQuestionsMock.mockResolvedValue([
      asyncQuestion({
        status: "resolved",
        response_data: JSON.stringify({ answers: [{ question: "Which database?", selected: ["Postgres (Recommended)"] }] }),
      }),
    ]);
    renderCard();

    expect(await screen.findByText("Question answered")).toBeInTheDocument();
    expect(screen.getByTestId("async-question-answers")).toHaveTextContent("Postgres (Recommended)");
    expect(screen.queryByRole("button", { name: /submit/i })).toBeNull();
  });

  it("shows what is asked before the question row arrives", async () => {
    listAsyncQuestionsMock.mockResolvedValue([]);
    renderCard();

    expect(await screen.findByTestId("async-question-posting")).toHaveTextContent("Which database?");
    expect(screen.queryByRole("button", { name: /submit/i })).toBeNull();
  });

  it("re-arms the form when the answer could not be delivered", async () => {
    listAsyncQuestionsMock.mockResolvedValue([asyncQuestion()]);
    resolveQuestionMock.mockRejectedValueOnce(new Error("This chat is paused and its run could not be resumed"));
    const user = userEvent.setup();
    renderCard();

    await user.click(await screen.findByText("SQLite"));
    await user.click(screen.getByRole("button", { name: /submit/i }));

    await waitFor(() => expect(resolveQuestionMock).toHaveBeenCalledTimes(1));
    expect(await screen.findByText("Question for you · not blocking")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /submit/i })).toBeInTheDocument();
  });
});
