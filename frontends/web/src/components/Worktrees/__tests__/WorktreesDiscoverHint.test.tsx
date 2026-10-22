import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

const discover = vi.hoisted(() => vi.fn());
vi.mock("../../../api/worktree-grpc", () => ({ worktreeGrpc: { discover } }));

import { WorktreesDiscoverHint } from "../WorktreesDiscoverHint";

const list = (n: number) => ({
  discovered: Array.from({ length: n }, (_, i) => ({ path: `/w/${i}` })),
  stale: [],
  workspacesRoot: "/r",
});

function mount() {
  const qc = new QueryClient();
  const onReview = vi.fn();
  const ui = (
    <QueryClientProvider client={qc}>
      <WorktreesDiscoverHint projectId="p1" onReview={onReview} />
    </QueryClientProvider>
  );
  const r = render(ui);
  return { qc, onReview, rerender: () => r.rerender(ui) };
}

describe("WorktreesDiscoverHint", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
  });

  it("is hidden at 0", async () => {
    discover.mockResolvedValue(list(0));
    mount();
    await waitFor(() => expect(discover).toHaveBeenCalled());
    expect(screen.queryByTestId("worktrees-discover-hint")).toBeNull();
  });

  it("shows the count and opens review", async () => {
    discover.mockResolvedValue(list(2));
    const { onReview } = mount();
    expect(await screen.findByText(/2 worktrees made outside Reliant/)).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Review" }));
    expect(onReview).toHaveBeenCalled();
  });

  it("hides silently when discover fails", async () => {
    discover.mockRejectedValue(new Error("offline"));
    mount();
    await waitFor(() => expect(discover).toHaveBeenCalled());
    expect(screen.queryByTestId("worktrees-discover-hint")).toBeNull();
  });

  it("stays dismissed, and reappears when the count grows", async () => {
    discover.mockResolvedValue(list(2));
    const { qc } = mount();
    await screen.findByText(/2 worktrees/);
    await userEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(screen.queryByTestId("worktrees-discover-hint")).toBeNull();
    expect(localStorage.getItem("reliant.worktreeHint.dismissed.p1")).toBe("2");

    discover.mockResolvedValue(list(3));
    await qc.invalidateQueries({ queryKey: ["external-worktrees", "p1"] });
    expect(await screen.findByText(/3 worktrees/)).toBeTruthy();
  });
});
