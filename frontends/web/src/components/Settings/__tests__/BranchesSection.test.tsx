import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClientProvider } from "@tanstack/react-query";
import { WorktreeStatus } from "../../../gen/reliant/v1/worktree_pb";

const mocks = vi.hoisted(() => ({
  deleteWorktree: vi.fn(async () => {}),
  unarchiveWorktree: vi.fn(async () => {}),
  updateWorktreeStatus: vi.fn(async () => {}),
  switchWorktreeContext: vi.fn(async () => {}),
  closeSettings: vi.fn(),
  navigate: vi.fn(),
  preferences: {
    worktree: {
      archiveMode: "ask_me",
      defaultDeleteBranch: false,
      branchCopyUncommittedFilesDefault: false,
    },
    skipDeleteConfirmation: false,
  },
}));

// Reads go through the generated hooks to an in-memory WorktreeService
// (test/worktreeServer.ts); the lifecycle writes are asserted as calls.
vi.mock("../../../hooks/worktree-actions", () => ({
  deleteWorktree: mocks.deleteWorktree,
  unarchiveWorktree: mocks.unarchiveWorktree,
  updateWorktreeStatus: mocks.updateWorktreeStatus,
}));

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

import { BranchesSection } from "../BranchesSection";
import { WORKTREE_PAGE_SIZE, type Worktree } from "../../../hooks/worktree-queries";
import { setTransport } from "../../../lib/connect";
import { queryClient } from "../../../lib/query-client";
import { mockTransport } from "../../../lib/test-utils";
import { useWorktreeStore } from "../../../store/worktreeStore";
import { selectWorktreeForTest } from "../../../test/worktreeCache";
import { worktreeServer, type WorktreeServer } from "../../../test/worktreeServer";

function worktree(overrides: Partial<Worktree>): Worktree {
  return {
    project_id: "project-1",
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

let server: WorktreeServer;

/**
 * Serve these workspaces, and answer git status for the feature workspace.
 * `pageSize` caps the server's pages below the request's limit, so a paging
 * test renders a few rows instead of WORKTREE_PAGE_SIZE: a hundred rendered
 * rows took over 15s under load and timed the test out.
 */
function serve(rows: Worktree[], pageSize?: number) {
  server = worktreeServer(rows, { pageSize });
  setTransport(
    mockTransport({
      ...server.handlers,
      "WorktreeService.GetWorktreeGitStatus": {
        response: { clean: false, branch: "fix/flaky-test", modifiedFiles: ["a.ts", "b.ts"] },
      },
    }),
  );
}

/** The page size the paging tests serve: small, so a second page costs nothing to render. */
const SMALL_PAGE = 3;

function renderSection() {
  return render(
    <QueryClientProvider client={queryClient}>
      <BranchesSection />
    </QueryClientProvider>,
  );
}

/** Render and wait for the first page of live workspaces. */
async function renderLoaded() {
  const view = renderSection();
  await waitFor(() => {
    expect(server.calls("ListWorktrees")).toBeGreaterThan(0);
    expect(queryClient.isFetching()).toBe(0);
  });
  return view;
}

describe("Settings → Branches", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.preferences.worktree.archiveMode = "ask_me";
    serve([MAIN, FEATURE, ARCHIVED]);
    selectWorktreeForTest(MAIN);
    useWorktreeStore.setState({ switchWorktreeContext: mocks.switchWorktreeContext });
  });

  afterEach(() => {
    // Unmount first: a mounted query refetches when its cache is cleared.
    cleanup();
    queryClient.clear();
    setTransport(null);
    useWorktreeStore.getState().reset();
  });

  it("explains the page and scopes it to the open project and machine", async () => {
    await renderLoaded();
    expect(screen.getByRole("heading", { level: 1, name: "Branches" })).toBeInTheDocument();
    expect(screen.getByText(/each branched chat works in its own git worktree/i)).toBeInTheDocument();
    expect(screen.getByTitle("Branches are listed for the open project")).toHaveTextContent("reliant");
    expect(screen.getByText("studio.local")).toBeInTheDocument();
  });

  it("lists active workspaces with branch, git state and linked chat, main first", async () => {
    await renderLoaded();
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
    await renderLoaded();

    await user.click(screen.getByRole("button", { name: /^flaky-test-fix\b/ }));

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
    await renderLoaded();

    await user.click(screen.getByTestId("open-workspace-wt-feature"));

    await waitFor(() =>
      expect(mocks.switchWorktreeContext).toHaveBeenCalledWith("project-1", expect.objectContaining({ id: FEATURE.id })),
    );
    expect(mocks.closeSettings).toHaveBeenCalled();
  });

  it("does not offer to archive the main checkout", async () => {
    await renderLoaded();
    expect(screen.getByTestId("archive-workspace-wt-main")).toBeDisabled();
  });

  it("asks before archiving when the cleanup setting is Ask every time", async () => {
    const user = userEvent.setup();
    await renderLoaded();

    await user.click(screen.getByTestId("archive-workspace-wt-feature"));
    expect(mocks.deleteWorktree).not.toHaveBeenCalled();

    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).queryByLabelText(/worktree directory/i)).not.toBeInTheDocument();
    expect(within(dialog).getByLabelText(/git branch/i)).not.toBeChecked();
    await user.click(within(dialog).getByRole("button", { name: "Archive workspace" }));

    await waitFor(() =>
      expect(mocks.deleteWorktree).toHaveBeenCalledWith("wt-feature", {
        deleteGitBranch: false,
      }),
    );
  });

  it("archives at once, keeping files, when the setting is Keep everything", async () => {
    mocks.preferences.worktree.archiveMode = "always_keep";
    const user = userEvent.setup();
    await renderLoaded();

    await user.click(screen.getByTestId("archive-workspace-wt-feature"));

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(mocks.deleteWorktree).toHaveBeenCalledWith("wt-feature", {
      deleteGitBranch: false,
    });
  });

  it("restores and permanently deletes from the Archived tab", async () => {
    const user = userEvent.setup();
    await renderLoaded();

    await user.click(screen.getByRole("tab", { name: /archived/i }));
    expect(await screen.findByTestId("archived-row-wt-old")).toBeInTheDocument();
    expect(screen.queryByTestId("archived-row-wt-feature")).not.toBeInTheDocument();

    await user.click(screen.getByTestId("restore-workspace-wt-old"));
    expect(mocks.unarchiveWorktree).toHaveBeenCalledWith("wt-old");

    // Permanent delete defaults to removing everything. The dialog reads its
    // defaults at mount, so this also pins that it remounts per workspace
    // rather than keeping the archive-time defaults from an earlier mount.
    await user.click(screen.getByTestId("delete-workspace-wt-old"));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).queryByLabelText(/worktree directory/i)).not.toBeInTheDocument();
    expect(within(dialog).getByLabelText(/git branch/i)).toBeChecked();
    await user.click(within(dialog).getByRole("button", { name: "Delete permanently" }));

    await waitFor(() =>
      expect(mocks.deleteWorktree).toHaveBeenCalledWith("wt-old", {
        deleteGitBranch: true,
      }),
    );
  });

  it("explains each cleanup setting", async () => {
    const user = userEvent.setup();
    await renderLoaded();

    await user.click(screen.getByRole("tab", { name: /cleanup settings/i }));

    const settings = screen.getByTestId("worktree-settings");
    expect(within(settings).getByRole("radio", { name: /ask every time/i })).toBeChecked();
    expect(within(settings).queryByRole("switch", { name: /worktree directory/i })).not.toBeInTheDocument();
    expect(
      within(settings).getByRole("switch", { name: "Delete the git branch" }),
    ).toBeInTheDocument();
  });

  it("loads only the live workspaces on open, never the archived ones", async () => {
    await renderLoaded();
    expect(server.requests("ListWorktrees")).toContainEqual(expect.objectContaining({ projectId: "project-1" }));
    expect(server.calls("ListArchivedWorktrees")).toBe(0);
  });

  it("loads the archived workspaces when the Archived tab is opened", async () => {
    const user = userEvent.setup();
    await renderLoaded();

    await user.click(screen.getByRole("tab", { name: /archived/i }));

    await waitFor(() =>
      expect(server.requests("ListArchivedWorktrees")).toContainEqual(expect.objectContaining({ projectId: "project-1" })),
    );
  });

  it("offers the next page of live workspaces and fetches it on request", async () => {
    serve(
      [
        MAIN,
        FEATURE,
        worktree({ id: "wt-first-page", name: "first-page" }),
        worktree({ id: "wt-agent", name: "agent" }),
      ],
      SMALL_PAGE,
    );
    const user = userEvent.setup();
    await renderLoaded();
    expect(server.requests("ListWorktrees").at(-1)?.limit).toBe(WORKTREE_PAGE_SIZE);
    expect(screen.queryByRole("button", { name: /^agent\b/ })).not.toBeInTheDocument();

    expect(screen.queryByTestId("workspace-row-wt-agent")).not.toBeInTheDocument();
    expect(server.requests("ListWorktrees")[0]).toMatchObject({ limit: WORKTREE_PAGE_SIZE });
    await user.click(screen.getByTestId("load-more-workspaces"));

    await waitFor(() => expect(server.requests("ListWorktrees").at(-1)?.pageToken).toBe(String(SMALL_PAGE)));
    expect(await screen.findByTestId("workspace-row-wt-agent")).toBeInTheDocument();
    expect(screen.getByTestId("workspace-row-wt-feature")).toBeInTheDocument();
    expect(await screen.findByRole("button", { name: /^agent\b/ })).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByTestId("load-more-workspaces")).not.toBeInTheDocument());
  });

  it("offers no load-more on the last page", async () => {
    await renderLoaded();
    expect(screen.queryByTestId("load-more-workspaces")).not.toBeInTheDocument();
  });

  it("pages the archived workspaces on their own token", async () => {
    serve(
      [
        MAIN,
        ...Array.from({ length: SMALL_PAGE + 1 }, (_, i) =>
          worktree({ id: `wt-old-${i}`, name: `old-${i}`, deleted_at: "2025-05-01T00:00:00Z" }),
        ),
      ],
      SMALL_PAGE,
    );
    const user = userEvent.setup();
    await renderLoaded();

    await user.click(screen.getByRole("tab", { name: /archived/i }));
    const more = await screen.findByTestId("load-more-archived");
    expect(screen.queryByTestId(`archived-row-wt-old-${SMALL_PAGE}`)).not.toBeInTheDocument();
    expect(server.requests("ListArchivedWorktrees")[0]).toMatchObject({ limit: WORKTREE_PAGE_SIZE });
    await user.click(more);

    await waitFor(() =>
      expect(server.requests("ListArchivedWorktrees").at(-1)?.pageToken).toBe(String(SMALL_PAGE)),
    );
    expect(await screen.findByTestId(`archived-row-wt-old-${SMALL_PAGE}`)).toBeInTheDocument();
    expect(screen.getByTestId("archived-row-wt-old-0")).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByTestId("load-more-archived")).not.toBeInTheDocument());
    expect(server.requests("ListWorktrees").every((r) => !r.pageToken)).toBe(true);
  });

  it("explains what a workspace is when there are none", async () => {
    serve([]);
    selectWorktreeForTest(null);
    await renderLoaded();
    expect(screen.getByText("No workspaces yet")).toBeInTheDocument();
    expect(screen.getByText(/when you branch a chat/i)).toBeInTheDocument();
  });
});
