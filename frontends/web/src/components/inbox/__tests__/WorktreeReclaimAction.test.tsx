import { create } from "@bufbuild/protobuf";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { InboxWorktreeReclaimSchema } from "@/gen/reliant/v1/inbox_pb";
import { WorktreeReclaimProposalSchema } from "@/gen/reliant/v1/tools_daemon_pb";
import { reclaimDetail, WorktreeReclaimAction } from "../WorktreeReclaimAction";

const api = vi.hoisted(() => ({ mutate: vi.fn() }));
vi.mock("@/hooks/inbox-queries", () => ({
  useDecideWorktreeReclaim: () => ({ mutate: api.mutate, isPending: false }),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const proposal = (key: string, extra: { decision?: string } = {}) =>
  create(WorktreeReclaimProposalSchema, {
    key,
    fingerprint: `fp-${key}`,
    path: `/tmp/${key}`,
    branch: "feat/x",
    reason: "dirty",
    workSummary: " M a.txt",
    idleSeconds: BigInt(6 * 86400),
    sizeBytes: BigInt(1 << 20),
    note: key === "a" ? "An abandoned experiment" : "",
    ...extra,
  });

const item = (online = true) =>
  create(InboxWorktreeReclaimSchema, {
    daemonId: "d1",
    daemonName: "default",
    online,
    mode: "on",
    total: 3,
    proposals: [
      proposal("a"),
      proposal("b"),
      proposal("c", { decision: "approve" }),
    ],
  });

describe("worktree reclaim review", () => {
  beforeEach(() => vi.resetAllMocks());

  it("summarizes what is waiting and what is already approved", () => {
    expect(reclaimDetail(item(false))).toBe(
      "2 worktrees proposed for removal (2.1 MB) · 1 approved · offline",
    );
  });

  it("sends one decision per worktree the person chose, each with the fingerprint shown", async () => {
    render(<WorktreeReclaimAction item={item()} label="default" />);
    fireEvent.click(screen.getByRole("button", { name: "Review" }));
    expect(screen.getByText(/An abandoned experiment/)).toBeInTheDocument();
    expect(screen.queryByText("/tmp/c")).not.toBeInTheDocument();
    const submit = () => screen.getByRole("button", { name: /^Submit/ });
    expect(submit()).toBeDisabled();
    fireEvent.click(screen.getAllByRole("button", { name: "Remove" })[0]);
    fireEvent.click(screen.getAllByRole("button", { name: "Keep" })[1]);
    expect(submit()).toHaveTextContent("Submit 2 decisions (1 to remove)");
    fireEvent.click(submit());
    await waitFor(() => expect(api.mutate).toHaveBeenCalledTimes(1));
    expect(api.mutate.mock.calls[0][0]).toEqual({
      daemonId: "d1",
      decisions: [
        { key: "a", fingerprint: "fp-a", approve: true },
        { key: "b", fingerprint: "fp-b", approve: false },
      ],
    });
  });

  it("cannot be reviewed while the machine is offline", () => {
    render(<WorktreeReclaimAction item={item(false)} label="default" />);
    expect(screen.getByRole("button", { name: "Review" })).toBeDisabled();
  });
});
