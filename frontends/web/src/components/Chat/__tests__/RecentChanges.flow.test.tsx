import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { renderWithQuery } from "../../../test/renderWithQuery";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";
import { RecentChanges } from "../RecentChanges";
import { FileChangeStatus } from "../../../gen/reliant/v1/common_pb";
import { useProjectStore } from "../../../store/projectStore";

// The commit flow of the Changes panel: commit-all when nothing is staged,
// commit-and-push, discard confirmation, and error states that keep the
// file list on screen.

const getChangesMock = vi.fn();
const git = {
  getExistingPR: vi.fn(),
  commitChanges: vi.fn(),
  pushChanges: vi.fn(),
  pullChanges: vi.fn(),
  stageFiles: vi.fn(),
  unstageFiles: vi.fn(),
  revertFiles: vi.fn(),
};

vi.mock("../../../../src/api/worktree-grpc", () => ({
  worktreeGrpc: {
    getChanges: (...args: unknown[]) => getChangesMock(...args),
  },
}));

vi.mock("../../../../src/api/project-grpc", () => ({
  projectGrpc: { getChanges: vi.fn() },
}));

vi.mock("../../../../src/api/git", () => ({
  getExistingPR: (...args: unknown[]) => git.getExistingPR(...args),
  commitChanges: (...args: unknown[]) => git.commitChanges(...args),
  pushChanges: (...args: unknown[]) => git.pushChanges(...args),
  pullChanges: (...args: unknown[]) => git.pullChanges(...args),
  stageFiles: (...args: unknown[]) => git.stageFiles(...args),
  unstageFiles: (...args: unknown[]) => git.unstageFiles(...args),
  revertFiles: (...args: unknown[]) => git.revertFiles(...args),
}));

vi.mock("../../../../src/store/viewerStore", () => ({
  useViewerStore: (selector: (state: { openDiffViewer: () => void }) => unknown) =>
    selector({ openDiffViewer: vi.fn() }),
}));

vi.mock("../../ui/FileIcon", () => ({
  FileIcon: () => <div data-testid="file-icon" />,
}));

vi.mock("../../SourceControl/PRDialog", () => ({
  PRDialog: () => null,
}));

vi.mock("../../Git/GitNotInitialized", () => ({
  GitNotInitialized: () => <div>Git not initialized</div>,
}));

vi.mock("../../ui/Tooltip", () => ({
  Tooltip: ({ children }: { children: ReactNode }) => <>{children}</>,
}));

let worktreeSeq = 0;
// Each test gets its own worktree id so the module-level 5s status cache
// never serves one test's files to another.
const nextWorktreeId = () => `wt-flow-${++worktreeSeq}`;

function changes(files: Array<{ path: string; status: FileChangeStatus; diff?: string }>) {
  return {
    branch: "feature/x",
    files: files.map((f) => ({ diff: "", is_new: false, ...f })),
    total_files: files.length,
    ahead: 0,
    behind: 0,
    default_branch: "main",
  };
}

function typeMessage(text: string) {
  fireEvent.change(screen.getByLabelText("Commit message"), { target: { value: text } });
}

describe("RecentChanges commit flow", () => {
  beforeEach(() => {
    getChangesMock.mockReset();
    Object.values(git).forEach((fn) => fn.mockReset());
    git.getExistingPR.mockResolvedValue({ exists: false });
    git.stageFiles.mockResolvedValue({ message: "ok" });
    git.commitChanges.mockResolvedValue({ message: "ok" });
    git.pushChanges.mockResolvedValue({ message: "ok" });
    git.revertFiles.mockResolvedValue({ message: "ok" });

    useProjectStore.setState({
      currentProject: {
        id: "project-1",
        name: "Project",
        path: "/tmp/project",
        is_git_repo: true,
        default_branch: "main",
        worktree_count: 1,
        last_active: new Date().toISOString(),
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
      },
    });
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("stages every change and commits when nothing is staged", async () => {
    const worktreeId = nextWorktreeId();
    getChangesMock.mockResolvedValue(
      changes([
        { path: "src/a.ts", status: FileChangeStatus.MODIFIED },
        { path: "b.ts", status: FileChangeStatus.UNTRACKED },
      ]),
    );

    renderWithQuery(<RecentChanges worktreeId={worktreeId} projectId="project-1" onClose={() => {}} />);
    await screen.findByText("a.ts");

    typeMessage("Add things");
    fireEvent.click(screen.getByRole("button", { name: "Commit all changes" }));

    await waitFor(() => expect(git.commitChanges).toHaveBeenCalledWith(worktreeId, "Add things", undefined));
    expect(git.stageFiles).toHaveBeenCalledWith(worktreeId, ["src/a.ts", "b.ts"], undefined);
    expect(git.stageFiles.mock.invocationCallOrder[0]).toBeLessThan(git.commitChanges.mock.invocationCallOrder[0]);
    expect(git.pushChanges).not.toHaveBeenCalled();
  });

  it("commits only staged files, then pushes, from the commit-and-push button", async () => {
    const worktreeId = nextWorktreeId();
    getChangesMock.mockResolvedValue(
      changes([
        { path: "staged.ts", status: FileChangeStatus.STAGED },
        { path: "unstaged.ts", status: FileChangeStatus.MODIFIED },
      ]),
    );

    renderWithQuery(<RecentChanges worktreeId={worktreeId} projectId="project-1" onClose={() => {}} />);
    await screen.findByText("staged.ts");

    expect(screen.getByRole("button", { name: "Commit 1 file" })).toBeInTheDocument();
    typeMessage("Ship it");
    fireEvent.click(screen.getByRole("button", { name: "Commit and push" }));

    await waitFor(() => expect(git.pushChanges).toHaveBeenCalledWith(worktreeId, undefined));
    expect(git.stageFiles).not.toHaveBeenCalled();
    expect(git.commitChanges).toHaveBeenCalledWith(worktreeId, "Ship it", undefined);
    expect(git.commitChanges.mock.invocationCallOrder[0]).toBeLessThan(git.pushChanges.mock.invocationCallOrder[0]);
  });

  it("confirms a discard in a dialog instead of window.confirm", async () => {
    const worktreeId = nextWorktreeId();
    const nativeConfirm = vi.spyOn(window, "confirm");
    getChangesMock.mockResolvedValue(changes([{ path: "a.ts", status: FileChangeStatus.MODIFIED }]));

    renderWithQuery(<RecentChanges worktreeId={worktreeId} projectId="project-1" onClose={() => {}} />);
    await screen.findByText("a.ts");

    fireEvent.click(screen.getByRole("button", { name: "Discard changes" }));
    let dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText("Discard changes to a.ts?")).toBeInTheDocument();

    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(git.revertFiles).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Discard changes" }));
    dialog = screen.getByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "Discard changes" }));

    await waitFor(() => expect(git.revertFiles).toHaveBeenCalledWith(worktreeId, ["a.ts"], undefined));
    expect(nativeConfirm).not.toHaveBeenCalled();
  });

  it("keeps the file list on screen when a commit fails", async () => {
    const worktreeId = nextWorktreeId();
    getChangesMock.mockResolvedValue(changes([{ path: "staged.ts", status: FileChangeStatus.STAGED }]));
    git.commitChanges.mockRejectedValue(new Error("pre-commit hook failed"));

    renderWithQuery(<RecentChanges worktreeId={worktreeId} projectId="project-1" onClose={() => {}} />);
    await screen.findByText("staged.ts");

    typeMessage("Broken");
    fireEvent.click(screen.getByRole("button", { name: "Commit 1 file" }));

    expect(await screen.findByText("pre-commit hook failed")).toBeInTheDocument();
    expect(screen.getByText("staged.ts")).toBeInTheDocument();
    // The message survives a failed commit so the user can retry.
    expect(screen.getByLabelText("Commit message")).toHaveValue("Broken");
  });

  it("offers a retry when the changes fail to load", async () => {
    const worktreeId = nextWorktreeId();
    getChangesMock
      .mockRejectedValueOnce(new Error("daemon unreachable"))
      .mockResolvedValue(changes([{ path: "back.ts", status: FileChangeStatus.MODIFIED }]));

    renderWithQuery(<RecentChanges worktreeId={worktreeId} projectId="project-1" onClose={() => {}} />);

    expect(await screen.findByText("Couldn't load changes")).toBeInTheDocument();
    expect(screen.getByText("daemon unreachable")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByText("back.ts")).toBeInTheDocument();
    expect(screen.queryByText("Couldn't load changes")).not.toBeInTheDocument();
  });

  it("says the tree is clean when there are no changes", async () => {
    getChangesMock.mockResolvedValue(changes([]));
    renderWithQuery(<RecentChanges worktreeId={nextWorktreeId()} projectId="project-1" onClose={() => {}} />);
    expect(await screen.findByText("No changes")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Commit" })).toBeDisabled();
  });
});
