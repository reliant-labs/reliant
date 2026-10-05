import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { WorktreeStatus } from "../../../gen/reliant/v1/worktree_pb";
import type { Worktree } from "../../../store/worktreeStore";

const mocks = vi.hoisted(() => ({
  worktrees: [] as Worktree[],
  loadWorktrees: vi.fn(async () => {}),
  deleteWorktree: vi.fn(async () => {}),
  unarchiveWorktree: vi.fn(async () => {}),
  updateWorktreeStatus: vi.fn(async () => {}),
  switchWorktreeContext: vi.fn(async () => {}),
  closeSettings: vi.fn(),
  navigate: vi.fn(),
  getGitStatus: vi.fn(),
  preferences: {
    worktree: {
      archiveMode: "ask_me",
      defaultDeleteDirectory: true,
      defaultDeleteBranch: false,
      branchCopyUncommittedFilesDefault: false,
    },
    skipDeleteConfirmation: false,
  },
}));

vi.mock("../../../store/worktreeStore", () => {
  const state = () => ({
    worktrees: mocks.worktrees,
    currentWorktree: mocks.worktrees.find((w) => w.is_main) ?? null,
    isLoading: false,
    deletingId: null,
    error: null,
    loadWorktrees: mocks.loadWorktrees,
    deleteWorktree: mocks.deleteWorktree,
    unarchiveWorktree: mocks.unarchiveWorktree,
    updateWorktreeStatus: mocks.updateWorktreeStatus,
    switchWorktreeContext: mocks.switchWorktreeContext,
  });
  return {
    useWorktreeStore: Object.assign((selector: (s: unknown) => unknown) => selector(state()), {
      getState: state,
    }),
  };
});

vi.mock("../../../store/projectStore", () => {
  const state = () => ({
    currentProject: { id: "project-1", name: "reliant", is_git_repo: true, default_branch: "main" },
    refreshCurrentProject: vi.fn(),
  });
  return {
    useProjectStore: Object.assign((selector: (s: unknown) => unknown) => selector(state()), {
      getState: state,
    }),
  };
});

vi.mock("../../../store/chatStore", () => ({
  useChatStore: { getState: () => ({ selectChat: vi.fn() }) },
}));
vi.mock("../../../store/chatNavigationStore", () => ({
  useChatNavigationStore: { getState: () => ({ navigateToChat: vi.fn() }) },
}));

vi.mock("../../../hooks/chat-queries", () => ({
  useChatList: () => ({
    data: [
      {
        id: "chat-1",
        title: "Fix the flaky test",
        worktreeId: "wt-feature",
        updatedAt: "2025-06-01T00:00:00Z",
        createdAt: "2025-06-01T00:00:00Z",
      },
    ],
  }),
}));

vi.mock("../../../hooks/settings-queries", () => ({
  usePreferences: () => ({ data: mocks.preferences }),
  useUpdatePreferences: () => ({ mutateAsync: vi.fn() }),
  useUpdateWorktreePreferences: () => ({ mutateAsync: vi.fn() }),
}));

vi.mock("../../../hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => ({ activeDaemon: { daemonId: "d-1", hostname: "studio.local" } }),
}));
vi.mock("../../../hooks/useSettingsClose", () => ({
  useSettingsClose: () => mocks.closeSettings,
}));
vi.mock("../../../hooks/useWindowContext", () => ({
  useWindowContext: () => ({ isElectron: false, openInNewWindow: vi.fn() }),
}));
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => mocks.navigate }));

vi.mock("../../../api/worktree-grpc", () => ({
  worktreeGrpc: { getGitStatus: mocks.getGitStatus },
}));

// Owned by other surfaces and covered by their own tests; stubbed so this
// test is about the workspaces page, not about git or the create flow.
vi.mock("../../Git/GitStatus", () => ({ GitStatus: () => <div data-testid="git-status" /> }));
vi.mock("../../Git/CommitHistory", () => ({
  CommitHistory: () => <div data-testid="commit-history" />,
}));
vi.mock("../../Git/InitializeGitModal", () => ({ InitializeGitModal: () => null }));
vi.mock("../../Worktrees/CreateWorktreeModal", () => ({ CreateWorktreeModal: () => null }));
vi.mock("../../Worktrees/DiscoverWorktreesModal", () => ({ DiscoverWorktreesModal: () => null }));
vi.mock("../../Worktrees/AddRepoModal", () => ({ AddRepoModal: () => null }));

import { WorkspacesSection } from "../WorkspacesSection";

function worktree(overrides: Partial<Worktree>): Worktree {
  return {
    id: "wt",
    name: "wt",
    path: "/Users/me/.reliant/worktrees/wt",
    branch: "wt",
    base_branch: "main",
    status: WorktreeStatus.ACTIVE,
    is_main: false,
    created_at: "2025-01-01T00:00:00Z",
    updated_at: "2025-01-01T00:00:00Z",
    last_active: "2025-01-01T00:00:00Z",
    deleted_at: null,
    ...overrides,
  };
}

const MAIN = worktree({ id: "wt-main", name: "reliant", branch: "main", is_main: true });
const FEATURE = worktree({
  id: "wt-feature",
  name: "flaky-test-fix",
  branch: "fix/flaky-test",
  last_active: "2025-06-01T00:00:00Z",
});
const ARCHIVED = worktree({
  id: "wt-old",
  name: "old-spike",
  branch: "spike/old",
  deleted_at: "2025-05-01T00:00:00Z",
});

function renderSection() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <WorkspacesSection />
    </QueryClientProvider>,
  );
}

describe("Settings → Workspaces", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.worktrees = [MAIN, FEATURE, ARCHIVED];
    mocks.preferences.worktree.archiveMode = "ask_me";
    mocks.getGitStatus.mockResolvedValue({
      is_clean: false,
      current_branch: "fix/flaky-test",
      ahead: 0,
      behind: 0,
      staged_files: [],
      modified_files: ["a.ts", "b.ts"],
      untracked_files: [],
      has_remote: false,
    });
  });

  it("explains the page and scopes it to the open project and machine", () => {
    renderSection();
    expect(screen.getByRole("heading", { level: 1, name: "Workspaces" })).toBeInTheDocument();
    expect(screen.getByText(/each branched chat gets its own git worktree/i)).toBeInTheDocument();
    expect(screen.getByText("reliant", { selector: "span" })).toBeInTheDocument();
    expect(screen.getByText("studio.local")).toBeInTheDocument();
  });

  it("lists active workspaces with branch, git state and linked chat, main first", async () => {
    renderSection();
    const tab = screen.getByRole("tab", { name: /active/i });
    expect(tab).toHaveAttribute("aria-selected", "true");

    const rows = within(screen.getByTestId("active-workspaces")).getAllByRole("row").slice(1);
    expect(rows.map((row) => row.getAttribute("data-testid"))).toEqual([
      "workspace-row-wt-main",
      "workspace-row-wt-feature",
    ]);
    expect(screen.queryByTestId("workspace-row-wt-old")).not.toBeInTheDocument();

    const featureRow = screen.getByTestId("workspace-row-wt-feature");
    expect(within(featureRow).getAllByText("fix/flaky-test").length).toBeGreaterThan(0);
    expect(
      within(featureRow).getByRole("button", { name: "Open chat Fix the flaky test" }),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(within(featureRow).getByTestId("git-state-wt-feature")).toHaveTextContent(
        "2 uncommitted",
      ),
    );
  });

  it("opens a workspace's detail in the page without switching the app into it", async () => {
    const user = userEvent.setup();
    renderSection();

    await user.click(screen.getByRole("button", { name: "View details for flaky-test-fix" }));

    const detail = screen.getByTestId("workspace-detail");
    expect(within(detail).getByRole("heading", { name: "flaky-test-fix" })).toBeInTheDocument();
    expect(within(detail).getByTestId("git-status")).toBeInTheDocument();
    expect(within(detail).getByTestId("commit-history")).toBeInTheDocument();
    expect(mocks.switchWorktreeContext).not.toHaveBeenCalled();

    await user.click(within(detail).getByRole("button", { name: "Workspaces" }));
    expect(screen.queryByTestId("workspace-detail")).not.toBeInTheDocument();
    expect(screen.getByTestId("active-workspaces")).toBeInTheDocument();
  });

  it("Open switches into the workspace and then leaves Settings", async () => {
    const user = userEvent.setup();
    renderSection();

    await user.click(screen.getByTestId("open-workspace-wt-feature"));

    await waitFor(() =>
      expect(mocks.switchWorktreeContext).toHaveBeenCalledWith("project-1", FEATURE),
    );
    expect(mocks.closeSettings).toHaveBeenCalled();
  });

  it("does not offer to archive the main checkout", () => {
    renderSection();
    expect(screen.getByTestId("archive-workspace-wt-main")).toBeDisabled();
  });

  it("asks before archiving when the cleanup setting is Ask every time", async () => {
    const user = userEvent.setup();
    renderSection();

    await user.click(screen.getByTestId("archive-workspace-wt-feature"));
    expect(mocks.deleteWorktree).not.toHaveBeenCalled();

    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByLabelText(/worktree directory/i)).toBeChecked();
    expect(within(dialog).getByLabelText(/git branch/i)).not.toBeChecked();
    await user.click(within(dialog).getByRole("button", { name: "Archive workspace" }));

    await waitFor(() =>
      expect(mocks.deleteWorktree).toHaveBeenCalledWith("wt-feature", {
        deleteGitBranch: false,
        deleteLocalDirectory: true,
      }),
    );
  });

  it("archives at once, keeping files, when the setting is Keep everything", async () => {
    mocks.preferences.worktree.archiveMode = "always_keep";
    const user = userEvent.setup();
    renderSection();

    await user.click(screen.getByTestId("archive-workspace-wt-feature"));

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(mocks.deleteWorktree).toHaveBeenCalledWith("wt-feature", {
      deleteGitBranch: false,
      deleteLocalDirectory: false,
    });
  });

  it("restores and permanently deletes from the Archived tab", async () => {
    const user = userEvent.setup();
    renderSection();

    await user.click(screen.getByRole("tab", { name: /archived/i }));
    expect(screen.getByTestId("archived-row-wt-old")).toBeInTheDocument();
    expect(screen.queryByTestId("archived-row-wt-feature")).not.toBeInTheDocument();

    await user.click(screen.getByTestId("restore-workspace-wt-old"));
    expect(mocks.unarchiveWorktree).toHaveBeenCalledWith("wt-old");

    // Permanent delete defaults to removing everything. The dialog reads its
    // defaults at mount, so this also pins that it remounts per workspace
    // rather than keeping the archive-time defaults from an earlier mount.
    await user.click(screen.getByTestId("delete-workspace-wt-old"));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByLabelText(/worktree directory/i)).toBeChecked();
    expect(within(dialog).getByLabelText(/git branch/i)).toBeChecked();
    await user.click(within(dialog).getByRole("button", { name: "Delete permanently" }));

    await waitFor(() =>
      expect(mocks.deleteWorktree).toHaveBeenCalledWith("wt-old", {
        deleteGitBranch: true,
        deleteLocalDirectory: true,
      }),
    );
  });

  it("explains each cleanup setting", async () => {
    const user = userEvent.setup();
    renderSection();

    await user.click(screen.getByRole("tab", { name: /cleanup settings/i }));

    const settings = screen.getByTestId("worktree-settings");
    expect(within(settings).getByRole("radio", { name: /ask every time/i })).toBeChecked();
    expect(within(settings).getByText(/uncommitted changes in it are lost/i)).toBeInTheDocument();
    expect(
      within(settings).getByRole("switch", { name: "Delete the git branch" }),
    ).toBeInTheDocument();
  });

  it("explains what a workspace is when there are none", () => {
    mocks.worktrees = [];
    renderSection();
    expect(screen.getByText("No workspaces yet")).toBeInTheDocument();
    expect(screen.getByText(/when you branch a chat/i)).toBeInTheDocument();
  });
});
