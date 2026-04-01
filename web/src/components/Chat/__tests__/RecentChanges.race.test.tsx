import { render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { RecentChanges } from "../RecentChanges";
import { FileChangeStatus } from "../../../gen/reliant/v1/common_pb";
import { useProjectStore } from "../../../store/projectStore";
import type { ReactNode } from "react";

const getChangesMock = vi.fn();
const getExistingPRMock = vi.fn();

vi.mock("../../../../src/api/worktree-grpc", () => ({
  worktreeGrpc: {
    getChanges: (...args: unknown[]) => getChangesMock(...args),
  },
}));

vi.mock("../../../../src/api/project-grpc", () => ({
  projectGrpc: {
    getChanges: vi.fn(),
  },
}));

vi.mock("../../../../src/api/git", () => ({
  getExistingPR: (...args: unknown[]) => getExistingPRMock(...args),
  commitChanges: vi.fn(),
  pushChanges: vi.fn(),
  pullChanges: vi.fn(),
  stageFiles: vi.fn(),
  unstageFiles: vi.fn(),
  revertFiles: vi.fn(),
}));

vi.mock("../../../../src/store/viewerStore", () => ({
  useViewerStore: (selector: (state: { openDiffViewer: ReturnType<typeof vi.fn> }) => unknown) =>
    selector({ openDiffViewer: vi.fn() }),
}));

vi.mock("../../../../src/store/refetchStore", async () => {
  const actual = await vi.importActual<typeof import("../../../store/refetchStore")>(
    "../../../store/refetchStore",
  );
  return actual;
});

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

vi.mock("../../RightSidebar/shared", () => ({
  SidebarSection: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  SidebarEmptyState: ({ title, description }: { title: string; description: string }) => (
    <div>
      <div>{title}</div>
      <div>{description}</div>
    </div>
  ),
  SidebarInput: (props: React.InputHTMLAttributes<HTMLInputElement>) => <input {...props} />,
}));

describe("RecentChanges", () => {
  beforeEach(() => {
    getChangesMock.mockReset();
    getExistingPRMock.mockReset();
    getExistingPRMock.mockResolvedValue({ exists: false });

    useProjectStore.setState({
      currentProject: {
        id: "project-1",
        name: "Project",
        path: "/tmp/project",
        is_git_repo: true,
        default_branch: "main",
        worktree_count: 2,
        last_active: new Date().toISOString(),
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
      },
    });
  });

  it("ignores stale worktree responses after switching chats", async () => {
    let resolveOld!: (value: unknown) => void;
    let resolveNew!: (value: unknown) => void;

    getChangesMock
      .mockImplementationOnce(
        () =>
          new Promise((resolve) => {
            resolveOld = resolve;
          }),
      )
      .mockImplementationOnce(
        () =>
          new Promise((resolve) => {
            resolveNew = resolve;
          }),
      );

    const { rerender } = render(
      <RecentChanges worktreeId="wt-old" projectId="project-1" onClose={() => {}} />,
    );

    rerender(<RecentChanges worktreeId="wt-new" projectId="project-1" onClose={() => {}} />);

    resolveOld({
      branch: "old-branch",
      files: [
        {
          path: "old-file.ts",
          status: FileChangeStatus.MODIFIED,
          diff: "+old",
          is_new: false,
        },
      ],
      total_files: 1,
      ahead: 0,
      behind: 0,
      default_branch: "main",
    });

    await Promise.resolve();

    expect(screen.queryByText("old-file.ts")).not.toBeInTheDocument();

    resolveNew({
      branch: "new-branch",
      files: [
        {
          path: "new-file.ts",
          status: FileChangeStatus.MODIFIED,
          diff: "+new",
          is_new: false,
        },
      ],
      total_files: 1,
      ahead: 0,
      behind: 0,
      default_branch: "main",
    });

    await waitFor(() => {
      expect(screen.getByText("new-file.ts")).toBeInTheDocument();
    });

    expect(screen.queryByText("old-file.ts")).not.toBeInTheDocument();
    expect(screen.getByText(/new-branch • 1 files changed/i)).toBeInTheDocument();
  });
});
