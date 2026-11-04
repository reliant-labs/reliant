/**
 * The timeline card of an ask_user call. An async one (no blocking: true): the
 * agent asked without waiting, and the user answers here whenever they like.
 * The answer goes through ResolveQuestion — the Inbox's call — and the card
 * then shows it. Every other state still shows what was asked: an ask_user
 * call never renders as nothing. research/ASYNC_ASK_USER.md.
 */

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { QuestionInfo } from "../../../gen/reliant/v1/question_pb";
import { setTransport } from "../../../lib/connect";
import { mockTransport } from "../../../lib/test-utils";
import { queryClient } from "../../../lib/query-client";

const { listAsyncQuestionsMock, resolveQuestionMock } = vi.hoisted(() => ({
  listAsyncQuestionsMock: vi.fn(),
  resolveQuestionMock: vi.fn(),
}));

vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn() } }));

import { AskUserCard, askUserCardMode } from "../AskUserCard";

const CHAT_ID = "chat-card";
const TOOL_CALL = "call_async_1";
const input = {
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
    $typeName: "reliant.v1.QuestionInfo",
    questionId: "q-1",
    chatId: CHAT_ID,
    workflowId: CHAT_ID,
    threadId: CHAT_ID,
    stepId: "execute_tools",
    status: "pending",
    createdAt: "2026-10-22T00:00:00Z",
    async: true,
    toolCallId: TOOL_CALL,
    ...overrides,
  };
}

function renderCard(finished = false, cardInput: unknown = input) {
  return render(
    <QueryClientProvider client={queryClient}>
      <AskUserCard chatId={CHAT_ID} toolCallId={TOOL_CALL} input={cardInput} finished={finished} />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  queryClient.clear();
  // Like the server: once answered, the async list reports it answered. An
  // answer invalidates the list, so a fake that kept saying "pending" would
  // put the form back.
  resolveQuestionMock.mockImplementation(async (questionId: string, _action: string, responseData?: string) => {
    listAsyncQuestionsMock.mockResolvedValue([asyncQuestion({ questionId, status: "resolved", responseData })]);
    return { success: true };
  });
  setTransport(
    mockTransport({
      "QuestionService.ListPendingQuestions": { response: { questions: [] } },
      "QuestionService.ListAsyncQuestions": async () => ({ questions: await listAsyncQuestionsMock() }),
      "QuestionService.ResolveQuestion": (req) => {
        const { questionId, action, responseData } = req as { questionId: string; action: string; responseData?: string };
        return resolveQuestionMock(questionId, action, responseData);
      },
    }),
  );
});
afterEach(() => queryClient.clear());

describe("AskUserCard", () => {
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

    // Answered: the card settles to its one line, question then answer.
    const summary = await screen.findByTestId("ask-user-summary");
    expect(summary).toHaveTextContent("Which database?");
    expect(summary).toHaveTextContent("SQLite");
    expect(screen.queryByRole("button", { name: /submit/i })).toBeNull();
  });

  it("shows an answered question's answer, with no form", async () => {
    listAsyncQuestionsMock.mockResolvedValue([
      asyncQuestion({
        status: "resolved",
        responseData: JSON.stringify({ answers: [{ question: "Which database?", selected: ["Postgres (Recommended)"] }] }),
      }),
    ]);
    const user = userEvent.setup();
    renderCard();

    await user.click(await screen.findByTestId("ask-user-summary"));
    expect(await screen.findByText("Question answered")).toBeInTheDocument();
    expect(screen.getByTestId("ask-user-answers")).toHaveTextContent("Postgres (Recommended)");
    expect(screen.queryByRole("button", { name: /submit/i })).toBeNull();
  });

  it("shows a question its agent withdrew, with the reason and no form", async () => {
    listAsyncQuestionsMock.mockResolvedValue([
      asyncQuestion({ status: "resolved", withdrawnReason: "Went with Postgres; the schema is migrated" }),
    ]);
    const user = userEvent.setup();
    renderCard();

    await user.click(await screen.findByTestId("ask-user-summary"));
    expect(await screen.findByText("Question withdrawn")).toBeInTheDocument();
    expect(screen.getByTestId("ask-user-status")).toHaveTextContent(
      "Withdrawn: Went with Postgres; the schema is migrated",
    );
    expect(screen.getByTestId("ask-user-withdrawn")).toHaveTextContent("Which database?");
    expect(screen.queryByRole("button", { name: /submit/i })).toBeNull();
  });

  it("shows what is asked before the question row arrives", async () => {
    listAsyncQuestionsMock.mockResolvedValue([]);
    renderCard();

    expect(await screen.findByTestId("ask-user-questions")).toHaveTextContent("Which database?");
    expect(screen.queryByRole("button", { name: /submit/i })).toBeNull();
  });

  it("shows a finished call's async question", async () => {
    listAsyncQuestionsMock.mockResolvedValue([asyncQuestion()]);
    renderCard(true);

    expect(await screen.findByText("Question for you · not blocking")).toBeInTheDocument();
  });

  // A call recorded before async became the default blocked, though its input
  // carries no flag; it finished with no async question row. It once rendered
  // nothing; an ask_user call must always show what was asked.
  it("shows the question of a finished call that has no async question", async () => {
    listAsyncQuestionsMock.mockResolvedValue([]);
    renderCard(true);

    await waitFor(() => expect(listAsyncQuestionsMock).toHaveBeenCalled());
    expect(await screen.findByText("Question asked")).toBeInTheDocument();
    expect(screen.getByTestId("ask-user-card")).toHaveAttribute("data-mode", "asked");
    expect(screen.getByTestId("ask-user-questions")).toHaveTextContent("Which database?");
    expect(screen.queryByRole("button", { name: /submit/i })).toBeNull();
  });

  // Prod, chat 641e33c9 (2026-10-22): the model wrote `questions` as a
  // JSON-encoded string and no `blocking` flag; the call's result had landed
  // and its async row is found by tool call id.
  it("is answerable for a string-encoded questions input, found by tool call id", async () => {
    listAsyncQuestionsMock.mockResolvedValue([asyncQuestion()]);
    const user = userEvent.setup();
    renderCard(true, JSON.stringify({ questions: JSON.stringify(input.questions) }));

    await waitFor(() => expect(screen.getByTestId("ask-user-card")).toHaveAttribute("data-mode", "async"));
    expect(screen.getByText("Question for you · not blocking")).toBeInTheDocument();
    await user.click(await screen.findByText("SQLite"));
    await user.click(screen.getByRole("button", { name: /submit/i }));
    await waitFor(() => expect(resolveQuestionMock).toHaveBeenCalledTimes(1));
    expect(resolveQuestionMock.mock.calls[0][0]).toBe("q-1");
  });

  it("shows a running blocking call's question, leaving the form to the composer", async () => {
    listAsyncQuestionsMock.mockResolvedValue([]);
    renderCard(false, { blocking: true, ...input });

    expect(await screen.findByText("Question for you")).toBeInTheDocument();
    expect(screen.getByTestId("ask-user-card")).toHaveAttribute("data-mode", "waiting");
    expect(screen.getByTestId("ask-user-status")).toHaveTextContent("Answer it below.");
    expect(screen.getByTestId("ask-user-questions")).toHaveTextContent("Which database?");
    expect(screen.queryByRole("button", { name: /submit/i })).toBeNull();
  });

  it("takes the questions from the row's metadata when the input has none it can read", async () => {
    listAsyncQuestionsMock.mockResolvedValue([
      asyncQuestion({ metadata: JSON.stringify({ type: "ask_user", async: true, questions: input.questions }) }),
    ]);
    renderCard(true, { questions: 42 });

    expect(await screen.findByText("SQLite")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /submit/i })).toBeInTheDocument();
  });

  it("still renders a card when the question cannot be read at all", async () => {
    listAsyncQuestionsMock.mockResolvedValue([]);
    renderCard(true, "not json");

    expect(await screen.findByTestId("ask-user-card")).toHaveTextContent("The question's text could not be read.");
  });

  it("shows a question whose input is still streaming", async () => {
    listAsyncQuestionsMock.mockResolvedValue([]);
    render(
      <QueryClientProvider client={queryClient}>
        <AskUserCard chatId={CHAT_ID} toolCallId={TOOL_CALL} input={undefined} />
      </QueryClientProvider>,
    );

    expect(await screen.findByTestId("ask-user-card")).toHaveTextContent("Writing the question…");
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

describe("AskUserCard once settled", () => {
  const answered = () =>
    asyncQuestion({
      status: "resolved",
      responseData: JSON.stringify({ answers: [{ question: "Which database?", selected: ["SQLite"] }] }),
    });

  it("collapses an answered question to one line: the question, then the answer", async () => {
    listAsyncQuestionsMock.mockResolvedValue([answered()]);
    renderCard();

    const summary = await screen.findByTestId("ask-user-summary");
    expect(screen.getByTestId("ask-user-card")).toHaveAttribute("data-collapsed", "true");
    expect(summary).toHaveAttribute("aria-expanded", "false");
    expect(summary).toHaveAccessibleName(/^Question answered: Which database\?.*SQLite$/);
    expect(summary).toHaveClass("min-h-[44px]");
    // Collapsed means collapsed: no title, status line, or answer list.
    expect(screen.queryByText("Question answered")).toBeNull();
    expect(screen.queryByTestId("ask-user-answers")).toBeNull();
  });

  it("expands to the full card on click, and collapses again from its header", async () => {
    listAsyncQuestionsMock.mockResolvedValue([answered()]);
    const user = userEvent.setup();
    renderCard();

    await user.click(await screen.findByTestId("ask-user-summary"));
    expect(screen.getByTestId("ask-user-card")).toHaveAttribute("data-collapsed", "false");
    expect(screen.getByTestId("ask-user-answers")).toHaveTextContent("SQLite");
    const header = screen.getByRole("button", { name: /Question answered/, expanded: true });
    expect(header).toHaveAttribute("aria-controls");
    expect(document.getElementById(header.getAttribute("aria-controls")!)).toContainElement(
      screen.getByTestId("ask-user-answers"),
    );

    await user.click(header);
    expect(screen.getByTestId("ask-user-summary")).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByTestId("ask-user-answers")).toBeNull();
  });

  it("expands from the keyboard", async () => {
    listAsyncQuestionsMock.mockResolvedValue([answered()]);
    const user = userEvent.setup();
    renderCard();

    await screen.findByTestId("ask-user-summary");
    await user.tab();
    expect(screen.getByTestId("ask-user-summary")).toHaveFocus();
    await user.keyboard("{Enter}");
    expect(screen.getByTestId("ask-user-answers")).toBeInTheDocument();
    await user.keyboard(" ");
    expect(screen.queryByTestId("ask-user-answers")).toBeNull();
  });

  it("collapses a withdrawn question to the question and the reason", async () => {
    listAsyncQuestionsMock.mockResolvedValue([
      asyncQuestion({ status: "resolved", withdrawnReason: "Went with Postgres" }),
    ]);
    renderCard();

    const summary = await screen.findByTestId("ask-user-summary");
    expect(summary).toHaveAccessibleName(/^Question withdrawn: Which database\?.*Withdrawn: Went with Postgres$/);
    expect(summary).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByTestId("ask-user-withdrawn")).toBeNull();
  });

  it("says when an answered question carried no answer", async () => {
    listAsyncQuestionsMock.mockResolvedValue([asyncQuestion({ status: "resolved" })]);
    renderCard();
    expect(await screen.findByTestId("ask-user-summary")).toHaveTextContent("No answer given.");
  });

  it("names the extra questions of a multi-question card on its one line", async () => {
    listAsyncQuestionsMock.mockResolvedValue([answered()]);
    renderCard(false, { questions: [...input.questions, { question: "Which region?", options: [] }] });
    expect(await screen.findByTestId("ask-user-summary")).toHaveTextContent("Which database? (+1 more)");
  });

  it("keeps an unanswered question full, with no collapse control", async () => {
    listAsyncQuestionsMock.mockResolvedValue([asyncQuestion()]);
    renderCard();

    expect(await screen.findByText("Question for you · not blocking")).toBeInTheDocument();
    expect(screen.queryByTestId("ask-user-summary")).toBeNull();
    expect(screen.queryByRole("button", { expanded: true })).toBeNull();
    expect(screen.getByTestId("ask-user-card")).not.toHaveAttribute("data-collapsed");
  });
});

describe("askUserCardMode", () => {
  const row = asyncQuestion();
  const cases: Array<[string, Parameters<typeof askUserCardMode>[0], ReturnType<typeof askUserCardMode>]> = [
    ["an async row wins over the input", { question: row, input: { blocking: true }, finished: true, listed: true }, "async"],
    ["async, running, no row yet", { question: undefined, input: {}, finished: false, listed: true }, "posting"],
    ["async, finished, rows not loaded", { question: undefined, input: {}, finished: true, listed: false }, "posting"],
    ["async input, finished, no row", { question: undefined, input: {}, finished: true, listed: true }, "asked"],
    ["blocking, running", { question: undefined, input: { blocking: true }, finished: false, listed: false }, "waiting"],
    ["blocking, finished", { question: undefined, input: { blocking: "true" }, finished: true, listed: false }, "asked"],
  ];
  it.each(cases)("%s", (_name, args, want) => {
    expect(askUserCardMode(args)).toBe(want);
  });
});
