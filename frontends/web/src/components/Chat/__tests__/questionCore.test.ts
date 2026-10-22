// Copyright (c) 2025 Reliant Labs

/**
 * The store-free halves of the chat's ask_user UI, now shared with the Inbox:
 * metadata → QuestionPrompt items, and answering a question by id. The chat's
 * composer (ChatInput via chatStore.resolveQuestion) and the Inbox row both go
 * through these, so a regression here breaks the chat too.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";

const { resolveQuestion } = vi.hoisted(() => ({
  resolveQuestion: vi.fn(async (..._args: unknown[]) => ({})),
}));
vi.mock("../../../api/question-grpc", () => ({
  questionGrpc: { resolveQuestion, listPendingQuestions: vi.fn(async () => []) },
}));

import { askUserQuestionItems } from "../askUserUtils";
import { answerQuestion, questionKeys } from "../../../hooks/approval-queries";
import { queryClient } from "../../../lib/query-client";
import { useChatStore } from "../../../store/chatStore";

beforeEach(() => {
  resolveQuestion.mockClear();
  queryClient.clear();
});

describe("askUserQuestionItems", () => {
  it("maps ask_user metadata onto QuestionPrompt's items", () => {
    const metadata = JSON.stringify({
      type: "ask_user",
      questions: [
        { question: "Ship it?", options: [{ label: "Yes", description: "" }], allow_multiple: true },
        { question: "Why?" },
      ],
    });
    expect(askUserQuestionItems(metadata)).toEqual([
      { question: "Ship it?", options: [{ label: "Yes", description: "" }], allowMultiple: true },
      { question: "Why?", options: [], allowMultiple: false },
    ]);
  });

  it("returns null for metadata that is not an ask_user question", () => {
    expect(askUserQuestionItems(undefined)).toBeNull();
    expect(askUserQuestionItems('{"type":"other"}')).toBeNull();
  });
});

describe("answerQuestion", () => {
  it("replies with the answers JSON and drops only that question from the chat's pending list", async () => {
    queryClient.setQueryData(questionKeys.pendingList("chat-1"), [
      { question_id: "q-1", thread_id: "chat-1" },
      { question_id: "q-other", thread_id: "spawn-a" },
    ]);
    const answers = { answers: [{ question: "Ship it?", selected: ["Yes"] }] };

    await answerQuestion("chat-1", "q-1", answers);

    expect(resolveQuestion).toHaveBeenCalledWith("q-1", "reply", JSON.stringify(answers));
    expect(queryClient.getQueryData(questionKeys.pendingList("chat-1"))).toEqual([
      { question_id: "q-other", thread_id: "spawn-a" },
    ]);
  });
});

describe("chatStore.resolveQuestion (the chat composer's path)", () => {
  it("still resolves the question and drops it from the pending list", async () => {
    queryClient.setQueryData(questionKeys.pendingList("chat-2"), [{ question_id: "q-2" }]);
    await useChatStore.getState().resolveQuestion("chat-2", "q-2", "reply", '{"answers":[]}');
    expect(resolveQuestion).toHaveBeenCalledWith("q-2", "reply", '{"answers":[]}');
    expect(queryClient.getQueryData(questionKeys.pendingList("chat-2"))).toEqual([]);
  });
});
