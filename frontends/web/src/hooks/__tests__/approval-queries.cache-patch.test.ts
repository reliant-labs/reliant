import { beforeEach, afterEach, describe, expect, it } from "vitest";
import { ApprovalStatus, type ListApprovalsByChatResponse } from "../../gen/reliant/v1/approval_pb";
import { queryClient } from "../../lib/query-client";
import { patchApprovalsCache, upsertApprovalInCache, type ApprovalFields } from "../approval-queries";
import { approvalServiceKeys } from "../approval-service-hooks_gen";
import { create } from "@bufbuild/protobuf";
import { ApprovalSchema } from "../../gen/reliant/v1/approval_pb";

const now = "2026-01-01T00:00:00.000Z";

function approval(overrides: ApprovalFields & { id?: string }): ApprovalFields & { id: string } {
  return {
    id: "a1",
    chatId: "c1",
    entityId: "cb1",
    status: ApprovalStatus.PENDING,
    createdAt: now,
    ...overrides,
  };
}

// The stream patches the entry the generated useListApprovalsByChat reads.
const list = (chatId: string) =>
  queryClient.getQueryData<ListApprovalsByChatResponse>(approvalServiceKeys.listApprovalsByChat({ chatId }))
    ?.approvals;

// queryClient is a module singleton — clear between tests to avoid bleed.
beforeEach(() => queryClient.clear());
afterEach(() => queryClient.clear());

describe("approval cache patching", () => {
  it("upserts a new approval into an empty cache", () => {
    upsertApprovalInCache("c1", approval({ id: "a1" }));
    expect(list("c1")?.map((a) => a.id)).toEqual(["a1"]);
  });

  it("replaces an existing approval by id (no duplicate)", () => {
    upsertApprovalInCache("c1", approval({ id: "a1", status: ApprovalStatus.PENDING }));
    upsertApprovalInCache("c1", approval({ id: "a1", status: ApprovalStatus.APPROVED }));
    expect(list("c1")).toHaveLength(1);
    expect(list("c1")?.[0].status).toBe(ApprovalStatus.APPROVED);
  });

  it("keeps what an approval asked about when a resolution event lands", () => {
    upsertApprovalInCache("c1", approval({ id: "a1", title: "Post message in #general?", description: "to #general" }));
    upsertApprovalInCache("c1", { id: "a1", status: ApprovalStatus.APPROVED, createdAt: undefined, title: undefined });
    const [merged] = list("c1") ?? [];
    expect(merged.status).toBe(ApprovalStatus.APPROVED);
    expect(merged.title).toBe("Post message in #general?");
    expect(merged.description).toBe("to #general");
    expect(merged.createdAt).toBe(now);
  });

  it("patchApprovalsCache seeds an absent cache rather than dropping the event", () => {
    patchApprovalsCache("c1", (prev) => [...prev, create(ApprovalSchema, approval({ id: "a2" }))]);
    expect(list("c1")?.map((a) => a.id)).toEqual(["a2"]);
  });

  it("keeps separate chats' approval lists independent", () => {
    upsertApprovalInCache("c1", approval({ id: "a1", chatId: "c1" }));
    upsertApprovalInCache("c2", approval({ id: "b1", chatId: "c2" }));
    expect(list("c1")).toHaveLength(1);
    expect(list("c2")).toHaveLength(1);
  });
});
