import { beforeEach, afterEach, describe, expect, it } from "vitest";
import { ApprovalStatus } from "../../api/approval-grpc";
import type { ToolApprovalRequest } from "../../api/approval-grpc";
import { queryClient } from "../../lib/query-client";
import {
  approvalKeys,
  patchApprovalsCache,
  upsertApprovalInCache,
  patchPendingQuestionCache,
  questionKeys,
} from "../approval-queries";

const now = "2026-01-01T00:00:00.000Z";

function approval(overrides: Partial<ToolApprovalRequest>): ToolApprovalRequest {
  return {
    id: "a1",
    chat_id: "c1",
    content_block_id: "cb1",
    status: ApprovalStatus.PENDING,
    created_at: now,
    ...overrides,
  } as ToolApprovalRequest;
}

// queryClient is a module singleton — clear between tests to avoid bleed.
beforeEach(() => queryClient.clear());
afterEach(() => queryClient.clear());

describe("approval cache patching", () => {
  it("upserts a new approval into an empty cache", () => {
    upsertApprovalInCache("c1", approval({ id: "a1" }));
    const list = queryClient.getQueryData<ToolApprovalRequest[]>(
      approvalKeys.list("c1"),
    );
    expect(list?.map((a) => a.id)).toEqual(["a1"]);
  });

  it("replaces an existing approval by id (no duplicate)", () => {
    upsertApprovalInCache("c1", approval({ id: "a1", status: ApprovalStatus.PENDING }));
    upsertApprovalInCache("c1", approval({ id: "a1", status: ApprovalStatus.APPROVED }));
    const list = queryClient.getQueryData<ToolApprovalRequest[]>(
      approvalKeys.list("c1"),
    );
    expect(list).toHaveLength(1);
    expect(list?.[0].status).toBe(ApprovalStatus.APPROVED);
  });

  it("patchApprovalsCache seeds an absent cache rather than dropping the event", () => {
    patchApprovalsCache("c1", (prev) => [...prev, approval({ id: "a2" })]);
    const list = queryClient.getQueryData<ToolApprovalRequest[]>(
      approvalKeys.list("c1"),
    );
    expect(list?.map((a) => a.id)).toEqual(["a2"]);
  });

  it("keeps separate chats' approval lists independent", () => {
    upsertApprovalInCache("c1", approval({ id: "a1", chat_id: "c1" }));
    upsertApprovalInCache("c2", approval({ id: "b1", chat_id: "c2" }));
    expect(
      queryClient.getQueryData<ToolApprovalRequest[]>(approvalKeys.list("c1")),
    ).toHaveLength(1);
    expect(
      queryClient.getQueryData<ToolApprovalRequest[]>(approvalKeys.list("c2")),
    ).toHaveLength(1);
  });
});

describe("pending question cache patching", () => {
  it("sets and clears the pending question", () => {
    patchPendingQuestionCache("c1", {
      question_id: "q1",
      chat_id: "c1",
      workflow_id: "w1",
      step_id: "s1",
      status: "pending",
      created_at: "",
    } as never);
    expect(
      queryClient.getQueryData(questionKeys.pending("c1")),
    ).toMatchObject({ question_id: "q1" });

    patchPendingQuestionCache("c1", null);
    expect(queryClient.getQueryData(questionKeys.pending("c1"))).toBeNull();
  });
});
