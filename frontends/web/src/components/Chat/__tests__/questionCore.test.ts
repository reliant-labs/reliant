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

import { askUserQuestionItems } from "../askUserUtils";
import { answerQuestion } from "../../../hooks/question-queries";
import { questionServiceKeys } from "../../../hooks/question-service-hooks_gen";
import { setTransport } from "../../../lib/connect";
import { queryClient } from "../../../lib/query-client";
import { mockTransport } from "../../../lib/test-utils";
import { useChatStore } from "../../../store/chatStore";

const pendingKey = (chatId: string) => questionServiceKeys.listPendingQuestions({ chatId });

beforeEach(() => {
  resolveQuestion.mockClear();
  queryClient.clear();
  setTransport(
    mockTransport({
      "QuestionService.ResolveQuestion": (req) => {
        const { questionId, action, responseData } = req as { questionId: string; action: string; responseData?: string };
        return resolveQuestion(questionId, action, responseData);
      },
    }),
  );
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
    queryClient.setQueryData(pendingKey("chat-1"), {
      questions: [
        { questionId: "q-1", threadId: "chat-1" },
        { questionId: "q-other", threadId: "spawn-a" },
      ],
    });
    const answers = { answers: [{ question: "Ship it?", selected: ["Yes"] }] };

    await answerQuestion("chat-1", "q-1", answers);

    expect(resolveQuestion).toHaveBeenCalledWith("q-1", "reply", JSON.stringify(answers));
    expect(queryClient.getQueryData(pendingKey("chat-1"))).toMatchObject({
      questions: [{ questionId: "q-other", threadId: "spawn-a" }],
    });
  });
});

describe("chatStore.resolveQuestion (the chat composer's path)", () => {
  it("still resolves the question and drops it from the pending list", async () => {
    queryClient.setQueryData(pendingKey("chat-2"), { questions: [{ questionId: "q-2" }] });
    await useChatStore.getState().resolveQuestion("chat-2", "q-2", "reply", '{"answers":[]}');
    expect(resolveQuestion).toHaveBeenCalledWith("q-2", "reply", '{"answers":[]}');
    expect(queryClient.getQueryData(pendingKey("chat-2"))).toMatchObject({ questions: [] });
  });
});
