import { create } from "@bufbuild/protobuf";
import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { InboxWorktreeMarkedSchema } from "@/gen/reliant/v1/inbox_pb";
import { MarkedWorktreeAction, markedDetail } from "../MarkedWorktreeAction";

const api = vi.hoisted(() => ({ keep: vi.fn(), reclaim: vi.fn() }));
vi.mock("@/hooks/inbox-queries", () => ({
  useKeepMarkedWorktree: () => ({ mutate: api.keep, isPending: false }),
  useReclaimMarkedWorktreeNow: () => ({ mutate: api.reclaim, isPending: false }),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const now = Date.parse("2026-11-11T12:00:00Z");
const marked = (extra: { approved?: boolean; reason?: string } = {}) =>
  create(InboxWorktreeMarkedSchema, {
    worktreeId: "wt-1",
    name: "fix-login",
    path: "/home/u/.reliant/worktrees/proj/fix-login",
    markId: "m-1",
    markedAt: "2026-11-11T09:00:00Z",
    reclaimAt: "2026-11-12T09:00:00Z",
    reason: extra.reason ?? "landed",
    approved: extra.approved ?? false,
  });

describe("markedDetail", () => {
  it("says why it qualified and when it goes", () => {
    expect(markedDetail(marked(), now)).toBe(
      "Quiet for a day and its work has landed · reclaimed in 21 hours unless it is used · /home/u/.reliant/worktrees/proj/fix-login",
    );
    expect(markedDetail(marked({ reason: "recoverable-idle" }), now)).toContain("clean, pushed and unused");
  });

  it("says an approved one goes at the next check", () => {
    expect(markedDetail(marked({ approved: true }), now)).toContain("reclaim approved");
  });
});

describe("MarkedWorktreeAction", () => {
  beforeEach(() => {
    api.keep.mockReset();
    api.reclaim.mockReset();
  });

  it("acts on the mark shown", () => {
    render(<MarkedWorktreeAction item={marked()} />);
    fireEvent.click(screen.getByRole("button", { name: "Keep" }));
    expect(api.keep).toHaveBeenCalledWith({ worktreeId: "wt-1", markId: "m-1" }, expect.anything());
    fireEvent.click(screen.getByRole("button", { name: "Reclaim now" }));
    expect(api.reclaim).toHaveBeenCalledWith({ worktreeId: "wt-1", markId: "m-1" }, expect.anything());
  });

  it("does not approve twice", () => {
    render(<MarkedWorktreeAction item={marked({ approved: true })} />);
    expect(screen.getByRole("button", { name: "Reclaim now" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Keep" })).toBeEnabled();
  });
});
