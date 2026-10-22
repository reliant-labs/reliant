/**
 * The mobile create-workspace sheet: what it sends to the same store action
 * the desktop modal calls, and how it recovers when the phone drops the reply.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

const createWorktree = vi.fn();
const refreshWorktrees = vi.fn();
const store = vi.hoisted(() => ({
  worktrees: [] as Array<{ id: string; name: string; deleted_at?: string | null }>,
}));

vi.mock("../../../store/worktreeStore", () => ({
  useWorktreeStore: Object.assign(
    (selector: (s: unknown) => unknown) => selector({ createWorktree }),
    { getState: () => ({ worktrees: store.worktrees, refreshWorktrees }) },
  ),
}));

const project = vi.hoisted(() => ({ isGitRepo: true }));
vi.mock("../../../store/projectStore", () => ({
  useProjectStore: (selector: (s: unknown) => unknown) =>
    selector({ currentProject: { id: "p1", is_git_repo: project.isGitRepo } }),
}));

const repos = vi.hoisted(() => ({ current: [{ id: "r1" }] as Array<{ id: string }> }));
vi.mock("../../../api/repo-grpc", () => ({
  repoGrpc: { list: () => Promise.resolve({ repos: repos.current }) },
}));

const useBranches = vi.fn();
vi.mock("../../../hooks/useBranches", () => ({
  useBranches: (...args: unknown[]) => useBranches(...args),
}));

const { MobileCreateWorkspaceSheet, baseBranchOptions } = await import(
  "../MobileCreateWorkspaceSheet"
);

const BRANCHES = [
  { name: "develop", is_current: true, is_remote: false, last_commit_age: 60 },
  { name: "main", is_current: false, is_remote: false, last_commit_age: 600 },
  { name: "origin/release", is_current: false, is_remote: true },
];

function renderSheet(onCreated = vi.fn(), onClose = vi.fn()) {
  render(<MobileCreateWorkspaceSheet projectId="p1" onClose={onClose} onCreated={onCreated} />);
  return { onCreated, onClose };
}

beforeEach(() => {
  createWorktree.mockReset();
  refreshWorktrees.mockReset().mockResolvedValue(undefined);
  useBranches.mockReset().mockReturnValue({ branches: BRANCHES, isLoading: false, error: null });
  store.worktrees = [{ id: "wt-main", name: "main" }];
  repos.current = [{ id: "r1" }];
  project.isGitRepo = true;
});

describe("MobileCreateWorkspaceSheet", () => {
  it("creates from the typed name, branching from the main checkout's current branch", async () => {
    const created = { id: "wt-new", name: "fix-login" };
    createWorktree.mockResolvedValue(created);
    const { onCreated } = renderSheet();

    // Branch picker shows the default once repos and branches are in.
    expect(await screen.findByText("develop")).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Name"), "  fix   login ");
    // The name becomes a branch; say which one before it is made.
    expect(screen.getByText("fix-login")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /Create workspace/ }));

    await waitFor(() => expect(onCreated).toHaveBeenCalledWith(created));
    expect(createWorktree).toHaveBeenCalledWith(
      expect.objectContaining({
        project_id: "p1",
        name: "fix-login",
        branch: "fix-login",
        base_branch: "develop",
        force: false,
      }),
    );
    // Same gitignored files desktop copies by default.
    expect(createWorktree.mock.calls[0][0].copy_files).toContain(".env");
    // Scoped to the single repo, as the branches RPC requires.
    expect(useBranches).toHaveBeenLastCalledWith("p1", "r1");
  });

  it("sends the base branch the user picked", async () => {
    createWorktree.mockResolvedValue({ id: "wt-new", name: "x" });
    renderSheet();

    // Wait for the picker, not the "Loading…" placeholder row it replaces.
    await userEvent.click(await screen.findByText("develop"));
    await userEvent.click(screen.getByRole("button", { name: /^main/ }));
    await userEvent.type(screen.getByLabelText("Name"), "x");
    await userEvent.click(screen.getByRole("button", { name: /Create workspace/ }));

    await waitFor(() => expect(createWorktree).toHaveBeenCalled());
    expect(createWorktree.mock.calls[0][0].base_branch).toBe("main");
  });

  it("lets each repo use its default branch in a multi-repo project", async () => {
    repos.current = [{ id: "r1" }, { id: "r2" }];
    createWorktree.mockResolvedValue({ id: "wt-new", name: "x" });
    renderSheet();

    expect(await screen.findByText("Each repo's default")).toBeInTheDocument();
    // Never asks for branches without a repo id; that RPC rejects it.
    expect(useBranches).toHaveBeenLastCalledWith(undefined, undefined);
    await userEvent.type(screen.getByLabelText("Name"), "x");
    await userEvent.click(screen.getByRole("button", { name: /Create workspace/ }));

    await waitFor(() => expect(createWorktree).toHaveBeenCalled());
    expect(createWorktree.mock.calls[0][0].base_branch).toBeUndefined();
  });

  it("leaves the base to the daemon when branches could not be listed", async () => {
    useBranches.mockReturnValue({ branches: [], isLoading: false, error: "boom" });
    createWorktree.mockResolvedValue({ id: "wt-new", name: "x" });
    renderSheet();

    expect(await screen.findByText("Default branch")).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Name"), "x");
    await userEvent.click(screen.getByRole("button", { name: /Create workspace/ }));

    await waitFor(() => expect(createWorktree).toHaveBeenCalled());
    // Not a guessed "main": the daemon detects the repo's real default.
    expect(createWorktree.mock.calls[0][0].base_branch).toBeUndefined();
  });

  it("keeps the name and reports the error when creation fails", async () => {
    createWorktree.mockRejectedValue(new Error("branch already exists"));
    const { onCreated } = renderSheet();

    await userEvent.type(screen.getByLabelText("Name"), "dupe");
    await userEvent.click(screen.getByRole("button", { name: /Create workspace/ }));

    expect(await screen.findByRole("alert")).toHaveTextContent("branch already exists");
    expect(screen.getByLabelText("Name")).toHaveValue("dupe");
    expect(onCreated).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: /Create workspace/ })).toBeEnabled();
  });

  it("adopts the workspace the server made when the reply was lost", async () => {
    // The phone backgrounded or dropped the network after the server acted:
    // the row exists, but the client saw an error.
    createWorktree.mockRejectedValue(new Error("Failed to fetch"));
    refreshWorktrees.mockImplementation(async () => {
      store.worktrees = [...store.worktrees, { id: "wt-made", name: "lost-reply" }];
    });
    const { onCreated } = renderSheet();

    await userEvent.type(screen.getByLabelText("Name"), "lost reply");
    await userEvent.click(screen.getByRole("button", { name: /Create workspace/ }));

    await waitFor(() =>
      expect(onCreated).toHaveBeenCalledWith(expect.objectContaining({ id: "wt-made" })),
    );
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("does not adopt a workspace that already existed under the same name", async () => {
    store.worktrees = [{ id: "wt-old", name: "taken" }];
    createWorktree.mockRejectedValue(new Error("already exists"));
    const { onCreated } = renderSheet();

    await userEvent.type(screen.getByLabelText("Name"), "taken");
    await userEvent.click(screen.getByRole("button", { name: /Create workspace/ }));

    expect(await screen.findByRole("alert")).toHaveTextContent("already exists");
    expect(onCreated).not.toHaveBeenCalled();
  });

  it("will not submit an empty name", async () => {
    renderSheet();
    expect(screen.getByRole("button", { name: /Create workspace/ })).toBeDisabled();
    await userEvent.type(screen.getByLabelText("Name"), "   ");
    expect(screen.getByRole("button", { name: /Create workspace/ })).toBeDisabled();
  });

  it("explains instead of offering a form when the project has no git", () => {
    project.isGitRepo = false;
    renderSheet();
    expect(screen.getByText(/isn't a git repository/)).toBeInTheDocument();
    expect(screen.queryByLabelText("Name")).not.toBeInTheDocument();
  });
});

describe("baseBranchOptions", () => {
  it("orders detached HEAD, main, recent local, then remote", () => {
    const options = baseBranchOptions([
      { name: "old", is_current: false, is_remote: false, last_commit_age: 9000 },
      { name: "origin/x", is_current: false, is_remote: true },
      { name: "recent", is_current: true, is_remote: false, last_commit_age: 10 },
      { name: "main", is_current: false, is_remote: false, last_commit_age: 500 },
      { name: "abc1234", is_current: false, is_remote: false, is_detached: true, commit_sha: "abc1234ffff" },
    ]);
    expect(options.map((o) => o.value)).toEqual([
      "abc1234ffff",
      "main",
      "recent",
      "old",
      "origin/x",
    ]);
    expect(options[4].label).toBe("x");
  });
});
