/**
 * openPathInFileViewer: the "Open in file viewer" action on an image
 * present_image showed. It opens the source file the tool read, inside the
 * project or outside it (/tmp/shot.png opens read-only, on the chat's
 * worktree's machine), and says why in a toast when it cannot.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";

const openFileViewer = vi.fn();
const toastError = vi.fn();
let mockProjectState: { currentProject: { id: string; path: string } | null } = {
  currentProject: { id: "proj-1", path: "/home/user/my-project" },
};

vi.mock("../../store/projectStore", () => ({
  useProjectStore: Object.assign(
    (selector?: (state: unknown) => unknown) => (selector ? selector(mockProjectState) : mockProjectState),
    { getState: () => mockProjectState },
  ),
}));

vi.mock("../../store/viewerStore", () => ({
  useViewerStore: { getState: () => ({ openFileViewer }) },
}));

vi.mock("../toast-manager", () => ({ toast: { error: (msg: string) => toastError(msg) } }));

import { openPathInFileViewer } from "../fileOpener";
import { queryClient } from "../query-client";
import { setWorktreesForTest } from "../../test/worktreeCache";

const mainWorktree = {
  id: "wt-main",
  name: "main",
  path: "/home/user/my-project",
  branch: "main",
  base_branch: "main",
  is_main: true,
  deleted_at: null,
  project_id: "proj-1",
};
const branchWorktree = {
  id: "wt-branch",
  name: "feature",
  path: "/home/user/.reliant/worktrees/abc/feature",
  branch: "feature",
  base_branch: "main",
  is_main: false,
  deleted_at: null,
  project_id: "proj-1",
};

describe("openPathInFileViewer", () => {
  beforeEach(() => {
    openFileViewer.mockClear();
    toastError.mockClear();
    mockProjectState = { currentProject: { id: "proj-1", path: "/home/user/my-project" } };
    queryClient.clear();
    setWorktreesForTest({ worktrees: [mainWorktree, branchWorktree], currentWorktree: mainWorktree });
  });

  it("opens a file in the chat's worktree in that worktree's viewer", () => {
    expect(openPathInFileViewer("/home/user/.reliant/worktrees/abc/feature/out/chart.png", "wt-branch")).toBe(true);
    expect(openFileViewer).toHaveBeenCalledTimes(1);
    const [file, projectId, worktreeId] = openFileViewer.mock.calls[0];
    expect(file).toMatchObject({ name: "chart.png", path: "/home/user/.reliant/worktrees/abc/feature/out/chart.png", type: "file" });
    expect(projectId).toBe("proj-1");
    expect(worktreeId).toBe("wt-branch");
  });

  it("opens a file outside the project (read-only) against the chat's worktree", () => {
    expect(openPathInFileViewer("/tmp/shot.png", "wt-branch")).toBe(true);
    const [file, , worktreeId] = openFileViewer.mock.calls[0];
    expect(file).toMatchObject({ name: "shot.png", path: "/tmp/shot.png" });
    expect(worktreeId).toBe("wt-branch");
    expect(toastError).not.toHaveBeenCalled();
  });

  it("says why, and opens nothing, with no project to open it in", () => {
    mockProjectState = { currentProject: null };
    expect(openPathInFileViewer("/tmp/shot.png", "wt-branch")).toBe(false);
    expect(openFileViewer).not.toHaveBeenCalled();
    expect(toastError).toHaveBeenCalledWith(expect.stringContaining("Open a project"));
  });

  it("says why, and opens nothing, for a path it cannot parse", () => {
    expect(openPathInFileViewer("", "wt-branch")).toBe(false);
    expect(openFileViewer).not.toHaveBeenCalled();
    expect(toastError).toHaveBeenCalled();
  });
});
