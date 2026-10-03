import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

const createWorktreeMock = vi.hoisted(() => vi.fn());
const listReposMock = vi.hoisted(() => vi.fn());

vi.mock("../../../store/worktreeStore", () => ({
  useWorktreeStore: Object.assign(
    (selector: (s: unknown) => unknown) =>
      selector({ createWorktree: createWorktreeMock }),
    { getState: () => ({ createWorktree: createWorktreeMock }) }
  ),
}));

vi.mock("../../../store/projectStore", () => ({
  useProjectStore: (selector: (s: unknown) => unknown) =>
    selector({
      currentProject: { id: "project-1", is_git_repo: true, default_branch: "main" },
      refreshCurrentProject: vi.fn(),
    }),
}));

vi.mock("../../../api/repo-grpc", () => ({
  repoGrpc: { list: listReposMock },
}));

vi.mock("../../../hooks/useBranches", () => ({
  useBranches: () => ({
    branches: [],
    isLoading: false,
    error: null,
    refetch: vi.fn(),
  }),
}));

import { CreateWorktreeModal } from "../CreateWorktreeModal";

describe("CreateWorktreeModal in a multi-repo project", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    listReposMock.mockResolvedValue({
      repos: [
        { id: "repo-a", name: "api", relative_path: "api" },
        { id: "repo-b", name: "web", relative_path: "web" },
      ],
    });
    createWorktreeMock.mockResolvedValue({ id: "wt-new", name: "feature-x" });
  });

  // Regression: this path used to call worktreeGrpc.batchCreate directly,
  // which left the Zustand store unaware of the new workspace — so it did not
  // appear in any picker until a page reload.
  it("creates through the worktree store so the store learns about it", async () => {
    const onWorktreeCreated = vi.fn();
    const user = userEvent.setup();

    render(
      <CreateWorktreeModal
        isOpen
        onClose={vi.fn()}
        onWorktreeCreated={onWorktreeCreated}
        projectId="project-1"
      />
    );

    await waitFor(() => expect(listReposMock).toHaveBeenCalled());

    const nameInput = await screen.findByPlaceholderText(/feature|name/i);
    await user.type(nameInput, "feature-x");

    const submit = screen.getByRole("button", { name: /create workspace/i });
    await user.click(submit);

    await waitFor(() => {
      expect(createWorktreeMock).toHaveBeenCalledWith(
        expect.objectContaining({
          project_id: "project-1",
          name: "feature-x",
        })
      );
    });

    await waitFor(() => expect(onWorktreeCreated).toHaveBeenCalledWith("wt-new"));
  });

  // copy_files are exact workspace-root paths. Nothing is pre-filled — the
  // old ".env, .env.local" default was a recursive search — and what the user
  // types is sent as-is.
  it("sends exactly the typed copy paths, and nothing by default", async () => {
    const user = userEvent.setup();
    render(
      <CreateWorktreeModal isOpen onClose={vi.fn()} onWorktreeCreated={vi.fn()} projectId="project-1" />
    );
    await waitFor(() => expect(listReposMock).toHaveBeenCalled());
    await user.type(await screen.findByPlaceholderText(/feature|name/i), "feature-x");
    await user.click(screen.getByRole("button", { name: /advanced/i }));

    const copyInput = await screen.findByPlaceholderText(/node_modules/);
    expect(copyInput).toHaveValue("");
    await user.type(copyInput, ".env, api/.env, web/node_modules/");
    await user.click(screen.getByRole("button", { name: /create workspace/i }));

    await waitFor(() =>
      expect(createWorktreeMock).toHaveBeenCalledWith(
        expect.objectContaining({ copy_files: [".env", "api/.env", "web/node_modules/"] })
      )
    );
  });

  it("rejects a copy path that leaves the workspace, without creating", async () => {
    const user = userEvent.setup();
    render(
      <CreateWorktreeModal isOpen onClose={vi.fn()} onWorktreeCreated={vi.fn()} projectId="project-1" />
    );
    await waitFor(() => expect(listReposMock).toHaveBeenCalled());

    await user.type(await screen.findByPlaceholderText(/feature|name/i), "feature-x");
    await user.click(screen.getByRole("button", { name: /advanced/i }));
    await user.type(await screen.findByPlaceholderText(/node_modules/), "../secrets");
    await user.click(screen.getByRole("button", { name: /create workspace/i }));

    expect(await screen.findByText(/leaves the workspace/)).toBeInTheDocument();
    expect(createWorktreeMock).not.toHaveBeenCalled();
  });
});
