import { beforeEach, afterEach, describe, expect, it } from "vitest";
import { ApprovalStatus } from "../../api/approval-grpc";
import type { ToolApprovalRequest } from "../../api/approval-grpc";
import { queryClient } from "../../lib/query-client";
import {
  approvalKeys,
  patchApprovalsCache,
  upsertApprovalInCache,
  upsertPendingQuestionInCache,
  removePendingQuestionFromCache,
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

  it("keeps what an approval asked about when a resolution event lands", () => {
    upsertApprovalInCache("c1", approval({ id: "a1", title: "Post message in #general?", params: { channel: "#general" } }));
    upsertApprovalInCache("c1", approval({ id: "a1", status: ApprovalStatus.APPROVED, created_at: undefined as never }));
    const [merged] = queryClient.getQueryData<ToolApprovalRequest[]>(approvalKeys.list("c1")) ?? [];
    expect(merged.status).toBe(ApprovalStatus.APPROVED);
    expect(merged.title).toBe("Post message in #general?");
    expect(merged.params).toEqual({ channel: "#general" });
    expect(merged.created_at).toBe(now);
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
  const question = (id: string, thread: string) =>
    ({
      question_id: id,
      chat_id: "c1",
      workflow_id: "w1",
      thread_id: thread,
      step_id: "s1",
      status: "pending",
      created_at: "",
    }) as const;

  it("keeps every thread's question: a newer one never replaces an older one", () => {
    upsertPendingQuestionInCache("c1", question("qA", "spawn-a"));
    upsertPendingQuestionInCache("c1", question("qB", "spawn-b"));
    expect(
      queryClient
        .getQueryData<{ question_id: string }[]>(questionKeys.pendingList("c1"))
        ?.map((q) => q.question_id),
    ).toEqual(["qA", "qB"]);
  });

  it("re-delivery of the same question updates it in place", () => {
    upsertPendingQuestionInCache("c1", question("qA", "spawn-a"));
    upsertPendingQuestionInCache("c1", { ...question("qA", "spawn-a"), metadata: "m" });
    expect(queryClient.getQueryData(questionKeys.pendingList("c1"))).toEqual([
      { ...question("qA", "spawn-a"), metadata: "m" },
    ]);
  });

  it("a resolution removes only the resolved question", () => {
    // The incident: B's answer cleared the cache, hiding A's still-pending
    // question, so the reply typed for A went to A's mailbox instead.
    upsertPendingQuestionInCache("c1", question("qA", "spawn-a"));
    upsertPendingQuestionInCache("c1", question("qB", "spawn-b"));
    removePendingQuestionFromCache("c1", "qB");
    expect(
      queryClient
        .getQueryData<{ question_id: string }[]>(questionKeys.pendingList("c1"))
        ?.map((q) => q.question_id),
    ).toEqual(["qA"]);
  });
});
