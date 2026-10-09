import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { DiscoveredWorktree, StaleWorktree } from "../../../api/worktree-grpc";

const h = vi.hoisted(() => ({
  state: {} as Record<string, unknown>,
  importWorktree: vi.fn(),
  pruneWorktrees: vi.fn(),
  discoverWorktrees: vi.fn(),
}));

vi.mock("../../../store/worktreeStore", () => ({
  useWorktreeStore: (selector: (s: unknown) => unknown) => selector(h.state),
}));

import { DiscoverWorktreesModal } from "../DiscoverWorktreesModal";

const wt = (o: Partial<DiscoveredWorktree>): DiscoveredWorktree => ({
  path: "/w/a",
  name: "a",
  branch: "feat/a",
  repo_id: "r1",
  repo_name: "api",
  head: "abc",
  locked: false,
  moves_on_import: false,
  ...o,
});

function setup(discovered: DiscoveredWorktree[], stale: StaleWorktree[] = []) {
  h.state = {
    discoveredWorktrees: discovered,
    staleWorktrees: stale,
    workspacesRoot: "/root/ws",
    isDiscovering: false,
    error: null,
    discoverWorktrees: h.discoverWorktrees,
    importWorktree: h.importWorktree,
    pruneWorktrees: h.pruneWorktrees,
  };
  render(
    <DiscoverWorktreesModal isOpen onClose={vi.fn()} onWorktreesImported={vi.fn()} projectId="p1" />
  );
}

describe("DiscoverWorktreesModal", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    h.importWorktree.mockResolvedValue({ id: "w1" });
  });

  it("groups checkouts by repo and shows detached", () => {
    setup([
      wt({ path: "/w/a" }),
      wt({ path: "/w/b", repo_id: "r2", repo_name: "web", branch: "" }),
    ]);
    expect(within(screen.getByRole("region", { name: "api" })).getByText("feat/a")).toBeTruthy();
    const web = screen.getByRole("region", { name: "web" });
    expect(within(web).getByText("detached")).toBeTruthy();
    expect(within(web).getByText("/w/b")).toBeTruthy();
  });

  it("warns about running processes before a move", async () => {
    setup([wt({ moves_on_import: true })]);
    await userEvent.click(screen.getByRole("button", { name: "Adopt" }));
    expect(screen.getByText(/This moves \/w\/a to \/root\/ws\/a-…\/api/)).toBeTruthy();
    expect(screen.getByText(/lose its working directory/)).toBeTruthy();
    expect(screen.getByText(/other repos on branch feat\/a/)).toBeTruthy();
    const dialogAdopt = screen.getAllByRole("button", { name: "Adopt" }).at(-1)!;
    await userEvent.click(dialogAdopt);
    await waitFor(() =>
      expect(h.importWorktree).toHaveBeenCalledWith(
        expect.objectContaining({ path: "/w/a", repo_id: "r1", confirm_move: true, project_id: "p1" })
      )
    );
  });

  it("explains in-place tracking", async () => {
    setup([wt({})]);
    await userEvent.click(screen.getByRole("button", { name: "Adopt" }));
    expect(screen.getByText(/Reliant will track \/w\/a as a workspace/)).toBeTruthy();
    expect(screen.queryByText(/lose its working directory/)).toBeNull();
  });

  it("disables adopt for a locked worktree that would move", async () => {
    setup([wt({ locked: true, moves_on_import: true })]);
    await userEvent.click(screen.getByRole("button", { name: "Adopt" }));
    expect(screen.getByText(/git worktree unlock \/w\/a/)).toBeTruthy();
    const dialogAdopt = screen.getAllByRole("button", { name: "Adopt" }).at(-1)!;
    expect((dialogAdopt as HTMLButtonElement).disabled).toBe(true);
  });

  it("shows the server error for a failed adopt", async () => {
    h.importWorktree.mockRejectedValue(new Error("boom"));
    setup([wt({})]);
    await userEvent.click(screen.getByRole("button", { name: "Adopt" }));
    await userEvent.click(screen.getAllByRole("button", { name: "Adopt" }).at(-1)!);
    expect(await screen.findByText("boom")).toBeTruthy();
  });

  it("disables clean up at 0 stale records", () => {
    setup([]);
    expect((screen.getByRole("button", { name: /Clean up/ }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("prunes stale records and shows what was removed", async () => {
    h.pruneWorktrees.mockResolvedValue([{ path: "/gone", repo_id: "r1", repo_name: "api", reason: "" }]);
    setup([], [{ path: "/gone", repo_id: "r1", repo_name: "api", reason: "" }]);
    expect(screen.getByText(/1 stale git record/)).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: /Clean up/ }));
    const result = await screen.findByTestId("prune-result");
    expect(result.textContent).toContain("/gone");
  });
});
