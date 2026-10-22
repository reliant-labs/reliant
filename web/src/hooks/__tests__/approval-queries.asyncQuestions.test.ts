import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { QuestionInfo } from "../../api/question-grpc";
import { queryClient } from "../../lib/query-client";
import type { QuestionUpdate } from "../../types/streaming";
import { applyQuestionUpdate, questionKeys } from "../approval-queries";

// The chat stream's question events: a blocking question lands in the pending
// list the composer and the background-work pill read; an async one
// (ask_user async: true) lands only in the timeline's async list, so it can
// never take over the composer.

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

const pendingList = () => queryClient.getQueryData<QuestionInfo[]>(questionKeys.pendingList("c1"));
const asyncList = () => queryClient.getQueryData<QuestionInfo[]>(questionKeys.asyncList("c1"));

beforeEach(() => queryClient.clear());
afterEach(() => queryClient.clear());

describe("applyQuestionUpdate", () => {
  it("routes an async question to the async list only", () => {
    queryClient.setQueryData<QuestionInfo[]>(questionKeys.asyncList("c1"), []);

    applyQuestionUpdate("c1", event("qa", "pending", true));

    expect(pendingList() ?? []).toEqual([]);
    expect(asyncList()?.map((q) => [q.question_id, q.status, q.tool_call_id, q.async])).toEqual([
      ["qa", "pending", "call-qa", true],
    ]);

    applyQuestionUpdate("c1", event("qa", "resolved", true));
    expect(asyncList()?.map((q) => [q.question_id, q.status])).toEqual([["qa", "resolved"]]);
    expect(pendingList() ?? []).toEqual([]);
  });

  it("records why a withdrawn async question was withdrawn", () => {
    queryClient.setQueryData<QuestionInfo[]>(questionKeys.asyncList("c1"), []);
    applyQuestionUpdate("c1", event("qw", "pending", true));

    applyQuestionUpdate("c1", { ...event("qw", "resolved", true), withdrawn_reason: "moot now" });

    expect(asyncList()?.map((q) => [q.question_id, q.status, q.withdrawn_reason])).toEqual([["qw", "resolved", "moot now"]]);
  });

  it("does not seed an async list that was never fetched", () => {
    applyQuestionUpdate("c1", event("qa", "pending", true));
    expect(asyncList()).toBeUndefined();
  });

  it("routes a blocking question to the pending list", () => {
    applyQuestionUpdate("c1", event("qb", "pending", false));
    expect(pendingList()?.map((q) => q.question_id)).toEqual(["qb"]);

    applyQuestionUpdate("c1", event("qb", "resolved", false));
    expect(pendingList()).toEqual([]);
  });
});
