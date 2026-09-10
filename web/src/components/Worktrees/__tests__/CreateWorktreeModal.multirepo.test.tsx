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
});
