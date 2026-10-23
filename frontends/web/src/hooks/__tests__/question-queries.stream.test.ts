import { create } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  ListAsyncQuestionsResponseSchema,
  QuestionInfoSchema,
  type ListAsyncQuestionsResponse,
  type ListPendingQuestionsResponse,
} from "../../gen/reliant/v1/question_pb";
import { queryClient } from "../../lib/query-client";
import type { QuestionUpdate } from "../../types/streaming";
import {
  applyQuestionUpdate,
  removePendingQuestionFromCache,
  upsertPendingQuestionInCache,
} from "../question-queries";
import { questionServiceKeys } from "../question-service-hooks_gen";

// The chat stream's question events patch the very cache entries the
// generated QuestionService hooks read: a blocking question lands in the
// pending list the composer and the background-work pill read; an async one
// (ask_user without blocking: true) lands only in the timeline's async list,
// so it can never take over the composer.

function event(id: string, status: "pending" | "resolved", async: boolean): QuestionUpdate {
  return {
    update_type: "question",
    question_id: id,
    chat_id: "c1",
    workflow_id: "c1",
    thread_id: "c1",
    step_id: "execute_tools",
    status,
    async,
    tool_call_id: `call-${id}`,
  };
}

const pendingKey = questionServiceKeys.listPendingQuestions({ chatId: "c1" });
const asyncKey = questionServiceKeys.listAsyncQuestions({ chatId: "c1" });
const pendingList = () => queryClient.getQueryData<ListPendingQuestionsResponse>(pendingKey)?.questions;
const asyncList = () => queryClient.getQueryData<ListAsyncQuestionsResponse>(asyncKey)?.questions;
const seedAsync = () => queryClient.setQueryData(asyncKey, create(ListAsyncQuestionsResponseSchema, {}));

beforeEach(() => queryClient.clear());
afterEach(() => queryClient.clear());

describe("applyQuestionUpdate", () => {
  it("routes an async question to the async list only", () => {
    seedAsync();

    applyQuestionUpdate("c1", event("qa", "pending", true));

    expect(pendingList() ?? []).toEqual([]);
    expect(asyncList()?.map((q) => [q.questionId, q.status, q.toolCallId, q.async])).toEqual([
      ["qa", "pending", "call-qa", true],
    ]);

    applyQuestionUpdate("c1", event("qa", "resolved", true));
    expect(asyncList()?.map((q) => [q.questionId, q.status])).toEqual([["qa", "resolved"]]);
    expect(pendingList() ?? []).toEqual([]);
  });

  it("records why a withdrawn async question was withdrawn", () => {
    seedAsync();
    applyQuestionUpdate("c1", event("qw", "pending", true));

    applyQuestionUpdate("c1", { ...event("qw", "resolved", true), withdrawn_reason: "moot now" });

    expect(asyncList()?.map((q) => [q.questionId, q.status, q.withdrawnReason])).toEqual([["qw", "resolved", "moot now"]]);
  });

  it("does not seed an async list that was never fetched", () => {
    applyQuestionUpdate("c1", event("qa", "pending", true));
    expect(asyncList()).toBeUndefined();
  });

  it("routes a blocking question to the pending list", () => {
    applyQuestionUpdate("c1", event("qb", "pending", false));
    expect(pendingList()?.map((q) => q.questionId)).toEqual(["qb"]);

    applyQuestionUpdate("c1", event("qb", "resolved", false));
    expect(pendingList()).toEqual([]);
  });
});

describe("pending question cache patching", () => {
  const question = (id: string, thread: string) =>
    create(QuestionInfoSchema, {
      questionId: id,
      chatId: "c1",
      workflowId: "w1",
      threadId: thread,
      stepId: "s1",
      status: "pending",
    });

  it("keeps every thread's question: a newer one never replaces an older one", () => {
    upsertPendingQuestionInCache("c1", question("qA", "spawn-a"));
    upsertPendingQuestionInCache("c1", question("qB", "spawn-b"));
    expect(pendingList()?.map((q) => q.questionId)).toEqual(["qA", "qB"]);
  });

  it("re-delivery of the same question updates it in place", () => {
    upsertPendingQuestionInCache("c1", question("qA", "spawn-a"));
    upsertPendingQuestionInCache("c1", { ...question("qA", "spawn-a"), metadata: "m" });
    expect(pendingList()).toEqual([{ ...question("qA", "spawn-a"), metadata: "m" }]);
  });

  it("a resolution removes only the resolved question", () => {
    // The incident: B's answer cleared the cache, hiding A's still-pending
    // question, so the reply typed for A went to A's mailbox instead.
    upsertPendingQuestionInCache("c1", question("qA", "spawn-a"));
    upsertPendingQuestionInCache("c1", question("qB", "spawn-b"));
    removePendingQuestionFromCache("c1", "qB");
    expect(pendingList()?.map((q) => q.questionId)).toEqual(["qA"]);
  });
});
