// @vitest-environment jsdom
import { act, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import { ApprovalStatus } from "#gen/reliant/v1/approval_pb.js";
import { harness, settle } from "../test/react-harness.js";
import { approvalData, CHAT, caughtUpFrame, snapshotFrame, textMessage, updatesFrame } from "../test/scripted-server.js";
import { approvalStatusFromFeed, isApprovalNotActionable, notActionableMessage } from "./approvals.js";
import { useApprovals } from "./useApprovals.js";

type Row = { id: string; chatId: string; status: ApprovalStatus; title: string };

function rows(): Row[] {
  return [
    { id: "a1", chatId: CHAT, status: ApprovalStatus.PENDING, title: "Deploy?" },
    { id: "a2", chatId: CHAT, status: ApprovalStatus.PENDING, title: "Migrate?" },
  ];
}

/** A server holding `server`, answering the reads and the four writes. */
function approvalServer(options: { fail?: () => Error | null } = {}) {
  let server = rows();
  const state = { listCalls: 0, writes: [] as Array<[string, Record<string, unknown>]> };
  const resolve = (name: string, ids: string[], status: ApprovalStatus, req: Record<string, unknown>) => {
    state.writes.push([name, req]);
    const err = options.fail?.();
    if (err) throw err;
    server = server.map((a) => (ids.includes(a.id) ? { ...a, status } : a));
    return { success: true };
  };
  return {
    state,
    expire: (id: string) => (server = server.map((a) => (a.id === id ? { ...a, status: ApprovalStatus.EXPIRED } : a))),
    get server() {
      return server;
    },
    handlers: {
      ListApprovalsByChat: () => {
        state.listCalls++;
        return { approvals: server, total: server.length };
      },
      Approve: (r: Record<string, unknown>) => resolve("approve", [r.requestId as string], ApprovalStatus.APPROVED, r),
      Deny: (r: Record<string, unknown>) => resolve("deny", [r.requestId as string], ApprovalStatus.DENIED, r),
      BatchApprove: (r: Record<string, unknown>) => resolve("batchApprove", r.requestIds as string[], ApprovalStatus.APPROVED, r),
      BatchDeny: (r: Record<string, unknown>) => resolve("batchDeny", r.requestIds as string[], ApprovalStatus.DENIED, r),
    },
  };
}

const statuses = (list: { id: string; status: ApprovalStatus }[]) => list.map((a) => [a.id, a.status]);

describe("useApprovals", () => {
  it("reads a chat's approvals once, and splits out the pending ones", async () => {
    const s = approvalServer();
    const h = harness(s.handlers);
    const a = h.render(() => useApprovals(CHAT, { live: false }));
    const b = h.render(() => useApprovals(CHAT, { live: false }));
    await waitFor(() => expect(a.result.current.approvals).toHaveLength(2));
    expect(b.result.current.pending.map((x) => x.id)).toEqual(["a1", "a2"]);
    expect(s.state.listCalls).toBe(1); // one cache entry, shared
  });

  it("approving flips the status at once, then reads what the server recorded", async () => {
    const s = approvalServer();
    const h = harness(s.handlers);
    const { result } = h.render(() => useApprovals(CHAT, { live: false }));
    await waitFor(() => expect(result.current.approvals).toHaveLength(2));

    await act(() => result.current.approve("a1", { actionTaken: "Deploy Now" }));
    expect(s.state.writes[0]).toEqual(["approve", expect.objectContaining({ requestId: "a1", actionTaken: "Deploy Now" })]);
    await waitFor(() => expect(s.state.listCalls).toBe(2));
    await waitFor(() => expect(result.current.pending.map((x) => x.id)).toEqual(["a2"]));
    expect(statuses(result.current.approvals)).toEqual([
      ["a1", ApprovalStatus.APPROVED],
      ["a2", ApprovalStatus.PENDING],
    ]);
  });

  it("denies one, and approves and denies several, each with its own request", async () => {
    const s = approvalServer();
    const h = harness(s.handlers);
    const { result } = h.render(() => useApprovals(CHAT, { live: false }));
    await waitFor(() => expect(result.current.approvals).toHaveLength(2));

    await act(() => result.current.deny("a1", { denialReason: "not now" }));
    await act(() => result.current.approveMany(["a2"], { actionTaken: "ok" }));
    await act(() => result.current.denyMany(["a1", "a2"], { denialReason: "no" }));
    expect(s.state.writes.map(([name]) => name)).toEqual(["deny", "batchApprove", "batchDeny"]);
    expect(s.state.writes[0]![1]).toMatchObject({ requestId: "a1", denialReason: "not now" });
    expect(s.state.writes[1]![1]).toMatchObject({ requestIds: ["a2"], actionTaken: "ok" });
    expect(s.state.writes[2]![1]).toMatchObject({ requestIds: ["a1", "a2"], denialReason: "no" });
  });

  it("a failed write rolls the optimistic flip back and rejects", async () => {
    let failing: Error | null = null;
    const s = approvalServer({ fail: () => failing });
    const h = harness(s.handlers);
    const { result } = h.render(() => useApprovals(CHAT, { live: false }));
    await waitFor(() => expect(result.current.approvals).toHaveLength(2));

    failing = new ConnectError("nope", Code.FailedPrecondition);
    await act(async () => {
      await expect(result.current.denyMany(["a1", "a2"])).rejects.toThrow("nope");
    });
    expect(statuses(result.current.approvals)).toEqual([
      ["a1", ApprovalStatus.PENDING],
      ["a2", ApprovalStatus.PENDING],
    ]);
    expect(result.current.busy).toBe(false);
  });

  it("a write that lost to the approval's own end drops it, tells why, and does not roll back", async () => {
    const expired = "This request expired: No one answered before the request's time limit; nothing was approved.";
    let failing: Error | null = new ConnectError(expired, Code.FailedPrecondition, { "x-forge-error-reason": "approval_not_pending" });
    const s = approvalServer({
      fail: () => {
        // The approval did end: the list the write then reads again says so.
        s.expire("a1");
        return failing;
      },
    });
    const told: string[] = [];
    const h = harness(s.handlers);
    const { result } = h.render(() => useApprovals(CHAT, { live: false, onNotActionable: (m) => told.push(m) }));
    await waitFor(() => expect(result.current.approvals).toHaveLength(2));

    await act(async () => {
      await expect(result.current.approve("a1")).rejects.toThrow();
    });
    expect(told).toEqual([expired]);
    await waitFor(() => expect(result.current.pending.map((x) => x.id)).toEqual(["a2"]));
    expect(result.current.approvals.find((x) => x.id === "a1")?.status).not.toBe(ApprovalStatus.PENDING);
    failing = null;
  });

  it("decides with no chat in hand (an inbox row), reading nothing", async () => {
    const s = approvalServer();
    const h = harness(s.handlers);
    const { result } = h.render(() => useApprovals(undefined));
    await settle();
    expect(s.state.listCalls).toBe(0);
    expect(h.server.connections).toHaveLength(0);
    await act(() => result.current.approve("a1"));
    expect(s.state.writes).toEqual([["approve", expect.objectContaining({ requestId: "a1" })]]);
  });

  it("takes the chat stream's approvals into the list live, merging a resolution over the title", async () => {
    const s = approvalServer();
    const h = harness({ ...s.handlers, ListApprovalsByChat: () => ({ approvals: [], total: 0 }) });
    const { result } = h.render(() => useApprovals(CHAT));
    await settle();

    const conn = h.server.chatStreams()[0]!;
    act(() => {
      conn.push(snapshotFrame(10, [textMessage("m-1", 1, 1, "go")], [approvalData(10, "pending")]));
      conn.push(caughtUpFrame(10));
    });
    await waitFor(() => expect(result.current.pending).toHaveLength(1));
    expect(result.current.pending[0]).toMatchObject({ id: "appr-1", title: "Run the migration", entityId: "tc-1" });

    // A resolution carries the new status, not what the approval asked.
    act(() => {
      conn.push(updatesFrame([{ ...approvalData(11, "approved"), dataJson: JSON.stringify({ id: "appr-1", chat_id: CHAT, status: "approved", sequence_number: 11 }) }], 11));
    });
    await waitFor(() => expect(result.current.pending).toEqual([]));
    expect(result.current.approvals[0]).toMatchObject({ status: ApprovalStatus.APPROVED, title: "Run the migration" });
  });

  it("reads a status it does not know as a resolution, never as pending", () => {
    expect(approvalStatusFromFeed("pending")).toBe(ApprovalStatus.PENDING);
    expect(approvalStatusFromFeed("timeout")).toBe(ApprovalStatus.EXPIRED);
    expect(approvalStatusFromFeed("superseded")).toBe(ApprovalStatus.CANCELLED);
    expect(approvalStatusFromFeed(undefined)).toBe(ApprovalStatus.CANCELLED);
  });

  it("classifies a lost write by the server's reason or a not-found, and words it from the server's sentence", () => {
    const notPending = new ConnectError("expired", Code.FailedPrecondition, { "x-forge-error-reason": "approval_not_pending" });
    expect(isApprovalNotActionable(notPending)).toBe(true);
    expect(isApprovalNotActionable(new ConnectError("gone", Code.NotFound))).toBe(true);
    expect(isApprovalNotActionable(new ConnectError("denied", Code.PermissionDenied))).toBe(false);
    expect(isApprovalNotActionable(new Error("boom"))).toBe(false);
    expect(notActionableMessage(notPending)).toBe("expired");
    expect(notActionableMessage(new Error("x"))).toBe("This request was already resolved.");
  });
});
