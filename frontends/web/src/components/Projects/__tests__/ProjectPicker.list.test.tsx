/**
 * ProjectPicker's project list: search, sort, expand, and overflow.
 *
 * These cover the behaviors a user hits with a realistic number of projects —
 * the list used to render every row unbounded inside a vertically-centered,
 * `overflow-hidden` shell, so anything past the fold was unreachable.
 *
 * The picker pulls a lot of daemon/cloud machinery on mount; we stub those
 * modules so the test exercises only the list. `capabilities.cloudDaemons` is
 * false here, which is the local/self-hosted shape and keeps the clone
 * affordances out of the tree.
 */
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

import type { Project } from "@/store/projectStore";

// The picker reads the OS colour-scheme preference on mount; jsdom has no
// matchMedia. Report "light" so the effect is a no-op.
vi.stubGlobal(
  "matchMedia",
  vi.fn(() => ({
    matches: false,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  })),
);

// ── Stub the picker's non-list dependencies ──────────────────

vi.mock("@/services/controlPlane/capabilities", () => ({
  capabilities: { cloudDaemons: false, managedCredits: false, gitConnections: false },
}));

// Only the network calls are stubbed; the status constants come from the real
// module. Re-declaring them here let the mock drift from the enum it was
// imitating, and adding a constant elsewhere then broke this suite for a
// reason that had nothing to do with what it tests.
vi.mock("@/services/controlPlane/daemon", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/controlPlane/daemon")>()),
  listDaemons: vi.fn(async () => ({ daemons: [] })),
  resumeDaemon: vi.fn(),
  deleteDaemon: vi.fn(),
  hasActiveDaemon: () => false,
}));

// An active daemon keeps the picker on its normal list branch instead of the
// "connect a daemon" empty state.
vi.mock("@/hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => ({
    daemons: [],
    activeDaemon: { daemonId: "d1", hostname: "local", status: 2 },
    loading: false,
    refresh: vi.fn(),
  }),
}));

vi.mock("@/hooks/useGitHubCredential", () => ({
  useGitHubCredential: () => ({ hasToken: false }),
}));

vi.mock("@/hooks/useOnboardingQueries", () => ({
  useResumeDaemon: () => ({ mutate: vi.fn(), isPending: false, variables: undefined }),
  useCreateDaemon: () => ({ mutateAsync: vi.fn(), isPending: false }),
}));

vi.mock("@/services/settingsSync", () => ({
  settingsSync: { getSetting: () => "", setSetting: vi.fn() },
  SETTINGS_KEYS: { THEME: "appearance.theme" },
}));

const mockProjects = vi.fn<() => Project[]>(() => []);
const mockUpdateProject = vi.fn(async () => {});
const mockDeleteProject = vi.fn(async () => {});

vi.mock("@/store/projectStore", () => {
  const useProjectStore = (selector: (s: unknown) => unknown) =>
    selector({
      projects: mockProjects(),
      loadProjects: vi.fn(),
      createProject: vi.fn(),
      selectProject: vi.fn(),
      updateProject: mockUpdateProject,
      deleteProject: mockDeleteProject,
    });
  useProjectStore.getState = () => ({ projects: mockProjects() });
  return { useProjectStore };
});

vi.mock("@/store/apiKeySetupStore", () => ({
  useApiKeySetupStore: (selector: (s: unknown) => unknown) =>
    selector({ ensureApiKeyOrShowModal: vi.fn() }),
}));

import { ProjectPicker } from "../ProjectPicker";

function makeProject(name: string, path: string, lastActiveISO: string): Project {
  return {
    id: `id-${name}`,
    name,
    path,
    is_git_repo: true,
    worktree_count: 0,
    last_active: lastActiveISO,
    created_at: lastActiveISO,
    updated_at: lastActiveISO,
  } as Project;
}

// Eight projects: more than the five-row "recent" cap, with deliberately
// out-of-order names/paths/timestamps so each sort mode is distinguishable.
const PROJECTS: Project[] = [
  makeProject("zulu", "/Users/dev/src/zulu", "2026-07-08T00:00:00Z"),
  makeProject("alpha", "/Users/dev/src/alpha", "2026-07-07T00:00:00Z"),
  makeProject("mike", "/Users/dev/work/mike", "2026-07-06T00:00:00Z"),
  makeProject("bravo", "/Users/dev/work/bravo", "2026-07-05T00:00:00Z"),
  makeProject("kilo", "/Users/dev/archive/kilo", "2026-07-04T00:00:00Z"),
  makeProject("delta", "/Users/dev/archive/delta", "2026-07-03T00:00:00Z"),
  makeProject("echo", "/home/dev/nested/echo", "2026-07-02T00:00:00Z"),
  makeProject("foxtrot", "/home/dev/nested/foxtrot", "2026-07-01T00:00:00Z"),
];

function renderPicker() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  return render(<ProjectPicker onProjectSelected={vi.fn()} />, { wrapper });
}

// The name lives in the row's first data cell (after the optional checkbox
// column). The path's leaf segment repeats the name as text elsewhere in the
// row, so target the cell rather than matching on content.
function rowNames(): string[] {
  return screen.getAllByTestId("project-item").map((row) => {
    const nameCell = row.querySelector("td:not(:has(input[type=checkbox]))");
    return nameCell?.textContent?.trim() ?? "";
  });
}

describe("ProjectPicker project list", () => {
  beforeEach(() => {
    mockProjects.mockReturnValue(PROJECTS);
  });

  it("shows every project with no View all gate", () => {
    renderPicker();

    // The list is never truncated — all 8 render immediately, and the old
    // expand toggle is gone.
    expect(screen.getAllByTestId("project-item")).toHaveLength(8);
    expect(screen.queryByRole("button", { name: /view all/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /show recent/i })).not.toBeInTheDocument();
  });

  it("keeps the list scrollable rather than letting it overflow", () => {
    renderPicker();

    // The rows share one scroll container. It is a flex item allowed to
    // shrink below its content (min-h-0) inside the page's fixed-height
    // column, so a long list scrolls in place instead of running off the
    // bottom of the shell.
    const list = screen.getAllByTestId("project-item")[0].closest("[data-testid=project-table]");
    expect(list).not.toBeNull();
    expect(list?.className).toMatch(/overflow-y-auto/);
    expect(list?.className).toMatch(/min-h-0/);
    // Every row lives in that one container.
    for (const row of screen.getAllByTestId("project-item")) {
      expect(list?.contains(row)).toBe(true);
    }
  });

  it("searches across name and path", async () => {
    const user = userEvent.setup();
    renderPicker();

    await user.type(screen.getByTestId("project-search"), "foxtrot");
    expect(rowNames()).toEqual(["foxtrot"]);

    // A path-only match: "archive" appears in no project name.
    await user.clear(screen.getByTestId("project-search"));
    await user.type(screen.getByTestId("project-search"), "archive");
    expect(rowNames().sort()).toEqual(["delta", "kilo"]);
  });

  it("reports when nothing matches", async () => {
    const user = userEvent.setup();
    renderPicker();

    await user.type(screen.getByTestId("project-search"), "nonexistent");
    expect(screen.queryAllByTestId("project-item")).toHaveLength(0);
    expect(screen.getByText(/no projects match/i)).toBeInTheDocument();
  });

  it("sorts by recency, name, and path from the column headers", async () => {
    const user = userEvent.setup();
    renderPicker();

    // Default: most recently active first.
    expect(rowNames()[0]).toBe("zulu");

    await user.click(screen.getByTestId("project-sort-name"));
    expect(rowNames()).toEqual([
      "alpha",
      "bravo",
      "delta",
      "echo",
      "foxtrot",
      "kilo",
      "mike",
      "zulu",
    ]);

    // Path sort groups by directory: ~/archive/… then ~/nested/… then ~/src/…,
    // ~/work/… — i.e. lexicographic on the home-collapsed path.
    await user.click(screen.getByTestId("project-sort-path"));
    expect(rowNames()).toEqual([
      "delta",
      "kilo",
      "echo",
      "foxtrot",
      "alpha",
      "zulu",
      "bravo",
      "mike",
    ]);
  });

  it("renders long paths without dropping the leaf directory", () => {
    const deep = makeProject(
      "deep",
      "/Users/dev/src/an/extremely/long/nesting/of/directories/deep",
      "2026-07-09T00:00:00Z",
    );
    mockProjects.mockReturnValue([deep, ...PROJECTS]);
    renderPicker();

    const row = screen.getAllByTestId("project-item")[0];
    // The leaf is rendered in its own non-shrinking span, so it survives
    // truncation of the path head.
    expect(within(row).getByText("deep", { selector: "span.shrink-0" })).toBeInTheDocument();
    // The full path is on the location cell, so hovering the column that got
    // truncated is what reveals it.
    const pathCell = within(row).getByTitle(
      "~/src/an/extremely/long/nesting/of/directories/deep",
    );
    expect(pathCell).toBeInTheDocument();
  });

  it("hides the search box when there are few projects", () => {
    mockProjects.mockReturnValue(PROJECTS.slice(0, 3));
    renderPicker();

    expect(screen.getAllByTestId("project-item")).toHaveLength(3);
    expect(screen.queryByTestId("project-search")).not.toBeInTheDocument();
    // Sorting lives in the column headers, which cost no extra space and so
    // stay available at any list length.
    expect(screen.getByTestId("project-sort-name")).toBeInTheDocument();
  });

  it("toggles sort direction when the active column is clicked again", async () => {
    const user = userEvent.setup();
    renderPicker();

    await user.click(screen.getByTestId("project-sort-name"));
    expect(rowNames()[0]).toBe("alpha");

    await user.click(screen.getByTestId("project-sort-name"));
    expect(rowNames()[0]).toBe("zulu");

    // Direction is exposed to assistive tech via aria-sort on the header.
    const header = screen.getByTestId("project-sort-name").closest("th");
    expect(header).toHaveAttribute("aria-sort", "descending");
  });

  it("shows a relative last-used column", () => {
    const recent = makeProject(
      "fresh",
      "/Users/dev/src/fresh",
      new Date(Date.now() - 2 * 3600 * 1000).toISOString(),
    );
    mockProjects.mockReturnValue([recent, ...PROJECTS]);
    renderPicker();

    expect(screen.getByText("2h ago")).toBeInTheDocument();
  });
});

describe("ProjectPicker row management", () => {
  beforeEach(() => {
    mockProjects.mockReturnValue(PROJECTS);
    mockUpdateProject.mockClear();
    mockDeleteProject.mockClear();
  });

  async function enterManageMode(user: ReturnType<typeof userEvent.setup>) {
    await user.click(screen.getByTestId("project-manage-toggle"));
  }

  it("keeps selection checkboxes hidden until Select is clicked", async () => {
    const user = userEvent.setup();
    renderPicker();

    expect(screen.queryAllByTestId("project-select")).toHaveLength(0);
    expect(screen.queryByTestId("project-select-all")).not.toBeInTheDocument();

    await enterManageMode(user);
    expect(screen.getAllByTestId("project-select")).toHaveLength(8);
    expect(screen.getByTestId("project-select-all")).toBeInTheDocument();
  });

  it("offers rename and remove on every row without entering selection mode", () => {
    renderPicker();

    // Per-row actions are in the DOM (revealed on hover/focus by CSS), each
    // with an accessible name that says which project it acts on.
    expect(screen.getAllByTestId("project-rename")).toHaveLength(8);
    expect(screen.getAllByTestId("project-remove")).toHaveLength(8);
    expect(screen.getByRole("button", { name: "Rename zulu" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Remove zulu" })).toBeInTheDocument();
  });

  it("row actions act on the row without opening the project", async () => {
    const onSelected = vi.fn();
    const user = userEvent.setup();
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<ProjectPicker onProjectSelected={onSelected} />, {
      wrapper: ({ children }) => (
        <QueryClientProvider client={client}>{children}</QueryClientProvider>
      ),
    });

    await user.click(screen.getAllByTestId("project-remove")[0]);
    expect(screen.getByTestId("confirm-remove-projects")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /^cancel$/i }));

    await user.click(screen.getAllByTestId("project-rename")[0]);
    expect(screen.getByTestId("project-rename-input")).toBeInTheDocument();

    expect(onSelected).not.toHaveBeenCalled();
  });

  it("selects rows instead of opening them while managing", async () => {
    const onSelected = vi.fn();
    const user = userEvent.setup();
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<ProjectPicker onProjectSelected={onSelected} />, {
      wrapper: ({ children }) => (
        <QueryClientProvider client={client}>{children}</QueryClientProvider>
      ),
    });

    await enterManageMode(user);
    await user.click(screen.getAllByTestId("project-item")[0]);

    // A mis-click during cleanup must not navigate into a workspace.
    expect(onSelected).not.toHaveBeenCalled();
    expect(screen.getByText("1 selected")).toBeInTheDocument();
  });

  it("select-all only covers rows matching the current search", async () => {
    const user = userEvent.setup();
    renderPicker();
    await enterManageMode(user);

    await user.type(screen.getByTestId("project-search"), "archive");
    await user.click(screen.getByTestId("project-select-all"));

    // Two projects live under ~/archive; the other six must stay unselected.
    expect(screen.getByText("2 selected")).toBeInTheDocument();
  });

  it("renames a project inline and persists on Enter", async () => {
    const user = userEvent.setup();
    renderPicker();

    await user.click(screen.getAllByTestId("project-rename")[0]);
    const input = screen.getByTestId("project-rename-input");
    await user.clear(input);
    await user.type(input, "renamed-project{Enter}");

    expect(mockUpdateProject).toHaveBeenCalledWith("id-zulu", { name: "renamed-project" });
  });

  it("abandons a rename on Escape without writing", async () => {
    const user = userEvent.setup();
    renderPicker();

    await user.click(screen.getAllByTestId("project-rename")[0]);
    const input = screen.getByTestId("project-rename-input");
    await user.clear(input);
    await user.type(input, "discard-me{Escape}");

    expect(mockUpdateProject).not.toHaveBeenCalled();
    expect(screen.queryByTestId("project-rename-input")).not.toBeInTheDocument();
  });

  it("confirms before removing, then deletes each selected project", async () => {
    const user = userEvent.setup();
    renderPicker();
    await enterManageMode(user);

    await user.click(screen.getAllByTestId("project-select")[0]);
    await user.click(screen.getAllByTestId("project-select")[1]);
    await user.click(screen.getByTestId("project-bulk-remove"));

    // Nothing is deleted until the dialog is confirmed. ("Remove 2 projects"
    // is both the dialog title and its confirm button, hence getAllByText.)
    expect(mockDeleteProject).not.toHaveBeenCalled();
    expect(screen.getAllByText(/remove 2 projects/i).length).toBeGreaterThan(0);
    // The dialog must state that files on disk are untouched.
    expect(screen.getByText(/stay exactly as they are on disk/i)).toBeInTheDocument();

    await user.click(screen.getByTestId("confirm-remove-projects"));
    expect(mockDeleteProject).toHaveBeenCalledTimes(2);
    expect(mockDeleteProject).toHaveBeenCalledWith("id-zulu");
    expect(mockDeleteProject).toHaveBeenCalledWith("id-alpha");
  });

  it("cancelling the confirm leaves everything in place", async () => {
    const user = userEvent.setup();
    renderPicker();

    await user.click(screen.getAllByTestId("project-remove")[0]);
    await user.click(screen.getByRole("button", { name: /^cancel$/i }));

    expect(mockDeleteProject).not.toHaveBeenCalled();
    expect(screen.getAllByTestId("project-item")).toHaveLength(8);
  });

  it("clears selection when leaving management mode", async () => {
    const user = userEvent.setup();
    renderPicker();
    await enterManageMode(user);

    await user.click(screen.getAllByTestId("project-select")[0]);
    expect(screen.getByText("1 selected")).toBeInTheDocument();

    // Exit and re-enter: a stale selection must not survive. The bulk bar is
    // only rendered while something is selected, so its absence is the check.
    await user.click(screen.getByTestId("project-manage-toggle"));
    await enterManageMode(user);
    expect(screen.queryByText(/\d+ selected/)).not.toBeInTheDocument();
    expect(screen.queryByTestId("project-bulk-remove")).not.toBeInTheDocument();
  });
});
