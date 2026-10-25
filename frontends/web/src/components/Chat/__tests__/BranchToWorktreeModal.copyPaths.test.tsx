import { beforeEach, describe, expect, it, vi } from "vitest";
import { waitFor } from "@testing-library/react";
import type { ReactElement } from "react";
import { mockTransport, renderWithTransport } from "@/lib/test-utils";

const getGitStatusMock = vi.hoisted(() => vi.fn());
const listReposMock = vi.hoisted(() => vi.fn());
const createWorktreeModalProps = vi.hoisted(() => ({ current: null as null | Record<string, unknown> }));

// The create modal is what receives the paths; capture its props.
vi.mock("../../Worktrees/CreateWorktreeModal", () => ({
  CreateWorktreeModal: (props: Record<string, unknown>) => {
    createWorktreeModalProps.current = props;
    return null;
  },
}));

// RepoService answers through the real transport seam, so the component's
// generated useListRepos hook is what is under test.
const render = (ui: ReactElement) =>
  renderWithTransport(ui, {
    transport: mockTransport({ "RepoService.ListRepos": (req) => listReposMock(req) }),
  });
vi.mock("../../../store/worktreeStore", () => ({
  useWorktreeStore: (selector: (s: unknown) => unknown) => selector({ switchWorktreeContext: vi.fn() }),
}));
// The source workspace is read by id; its git status per repo through the
// GetWorktreeGitStatus entry.
vi.mock("../../../hooks/worktree-queries", () => ({
  useWorktreeById: (id?: string) =>
    id === "wt-src" ? { id: "wt-src", name: "src", is_main: false, project_id: "p1" } : undefined,
  useMainWorktree: () => undefined,
  getCachedWorktree: () => undefined,
}));
vi.mock("../../../hooks/worktree-git-queries", () => ({
  fetchWorktreeGitStatus: (...args: unknown[]) => getGitStatusMock(...args),
}));
vi.mock("../../../store/chatStore", () => ({ useChatStore: { getState: () => ({ selectChat: vi.fn() }) } }));
vi.mock("../../../hooks/message-queries", () => ({ useBranchChat: () => ({ mutateAsync: vi.fn() }) }));
vi.mock("../../../hooks/settings-queries", () => ({
  // Copying uncommitted files on by default, so the paths reach the modal.
  usePreferences: () => ({ data: { worktree: { branchCopyUncommittedFilesDefault: true } } }),
  useUpdateWorktreePreferences: () => ({ mutateAsync: vi.fn() }),
}));
vi.mock("../../../lib/logger", () => ({ logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn() } }));

import { BranchToWorktreeModal } from "../BranchToWorktreeModal";

const status = (files: { modified?: string[]; untracked?: string[] }, branch = "feat") => ({
  current_branch: branch,
  modified_files: files.modified ?? [],
  staged_files: [],
  untracked_files: files.untracked ?? [],
});

describe("BranchToWorktreeModal copy paths", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    createWorktreeModalProps.current = null;
  });

  // git status is per repo and repo-relative; copy_files are workspace-root
  // relative. A multi-repo workspace used to make ONE status call with no repo
  // id, which the server rejects — so uncommitted files were never copied.
  it("asks every repo and prefixes its paths with the repo's location", async () => {
    listReposMock.mockResolvedValue({
      repos: [
        { id: "r-api", relativePath: "api" },
        { id: "r-web", relativePath: "web" },
      ],
    });
    getGitStatusMock.mockImplementation((_wt: string, repoId?: string) =>
      Promise.resolve(
        repoId === "r-api"
          ? status({ modified: ["main.go"] })
          : status({ untracked: ["src/new.ts"] }),
      ),
    );

    render(<BranchToWorktreeModal isOpen onClose={vi.fn()} chatId="c1" messageId="m1" projectId="p1" sourceWorktreeId="wt-src" />);

    await waitFor(() =>
      expect(createWorktreeModalProps.current?.additionalCopyFiles).toEqual(
        expect.arrayContaining(["api/main.go", "web/src/new.ts"]),
      ),
    );
    expect(getGitStatusMock).toHaveBeenCalledWith("wt-src", "r-api");
    expect(getGitStatusMock).toHaveBeenCalledWith("wt-src", "r-web");
    // No single base branch for a multi-repo workspace.
    expect(createWorktreeModalProps.current?.sourceWorktreeBranch).toBeUndefined();
  });

  // The server places a new workspace on the machine of the chat it is
  // created for. Without the chat id it fell back to the user's default
  // machine, and the branch chat followed its workspace there.
  it("names the chat it branches from, so the workspace lands on that chat's machine", async () => {
    listReposMock.mockResolvedValue({ repos: [] });
    getGitStatusMock.mockResolvedValue(status({}));

    render(<BranchToWorktreeModal isOpen onClose={vi.fn()} chatId="c1" messageId="m1" projectId="p1" sourceWorktreeId="wt-src" />);

    await waitFor(() => expect(createWorktreeModalProps.current?.chatId).toBe("c1"));
  });

  it("passes a single root repo's paths through unchanged", async () => {
    listReposMock.mockResolvedValue({ repos: [{ id: "r-root", relativePath: "" }] });
    getGitStatusMock.mockResolvedValue(status({ modified: ["src/a.ts"] }, "main"));

    render(<BranchToWorktreeModal isOpen onClose={vi.fn()} chatId="c1" messageId="m1" projectId="p1" sourceWorktreeId="wt-src" />);

    await waitFor(() => expect(createWorktreeModalProps.current?.additionalCopyFiles).toEqual(["src/a.ts"]));
    expect(createWorktreeModalProps.current?.sourceWorktreeBranch).toBe("main");
  });
});
