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
  questionGrpc: { resolveQuestion, getPendingQuestion: vi.fn(async () => null) },
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
  it("replies with the answers JSON and clears the chat's pending-question cache", async () => {
    queryClient.setQueryData(questionKeys.pending("chat-1"), { question_id: "q-1" });
    const answers = { answers: [{ question: "Ship it?", selected: ["Yes"] }] };

    await answerQuestion("chat-1", "q-1", answers);

    expect(resolveQuestion).toHaveBeenCalledWith("q-1", "reply", JSON.stringify(answers));
    expect(queryClient.getQueryData(questionKeys.pending("chat-1"))).toBeNull();
  });
});

describe("chatStore.resolveQuestion (the chat composer's path)", () => {
  it("still resolves the question and clears the pending cache", async () => {
    queryClient.setQueryData(questionKeys.pending("chat-2"), { question_id: "q-2" });
    await useChatStore.getState().resolveQuestion("chat-2", "q-2", "reply", '{"answers":[]}');
    expect(resolveQuestion).toHaveBeenCalledWith("q-2", "reply", '{"answers":[]}');
    expect(queryClient.getQueryData(questionKeys.pending("chat-2"))).toBeNull();
  });
});
