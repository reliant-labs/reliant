/**
 * Settings → Projects is a page about ALL of the user's projects.
 *
 * It used to render the current-project dashboard (ProjectPanel), so a user
 * with five projects could see exactly one here, and could neither switch to
 * nor remove any of the others. These pin the page's contract: every project
 * is listed with the current one marked, the page always offers Add project,
 * removing asks first and goes through the store, and the empty and error
 * states are real states rather than a blank table.
 */
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { Project } from "@/store/projectStore";
import { renderWithQuery } from "@/test/renderWithQuery";

function makeProject(overrides: Partial<Project> & Pick<Project, "id" | "name" | "path">): Project {
  return {
    is_git_repo: true,
    default_branch: "main",
    worktree_count: 0,
    last_active: "2025-01-01T00:00:00Z",
    created_at: "2025-01-01T00:00:00Z",
    updated_at: "2025-01-01T00:00:00Z",
    is_forge: false,
    ...overrides,
  };
}

const state = vi.hoisted(() => ({
  store: {} as Record<string, unknown>,
  navigate: vi.fn(),
  navigateToPicker: vi.fn(),
}));

vi.mock("@/store/projectStore", () => ({
  useProjectStore: (selector: (s: unknown) => unknown) => selector(state.store),
}));

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => state.navigate,
}));

vi.mock("@/hooks/useNavigateToProjectPicker", () => ({
  useNavigateToProjectPicker: () => state.navigateToPicker,
}));

vi.mock("@/hooks/chat-queries", () => ({
  useChatList: () => ({ data: [] }),
}));

vi.mock("@/store/worktreeStore", () => ({
  useWorktreeStore: (selector: (s: unknown) => unknown) =>
    selector({ worktrees: [], loadWorktrees: vi.fn(), switchWorktreeContext: vi.fn() }),
}));

vi.mock("@/store/chatStore", () => ({
  useChatStore: (selector: (s: unknown) => unknown) => selector({ selectChat: vi.fn() }),
}));

vi.mock("@/store/chatNavigationStore", () => ({
  useChatNavigationStore: (selector: (s: unknown) => unknown) => selector({ navigateToChat: vi.fn() }),
}));

vi.mock("@/api/project-grpc", () => ({
  projectGrpc: { getGitInfo: vi.fn(async () => { throw new Error("offline"); }) },
}));

vi.mock("@/lib/toast-manager", () => ({ toast: { error: vi.fn(), success: vi.fn() } }));

import { ProjectsSection } from "@/components/Settings/ProjectsSection";

const alpha = makeProject({
  id: "p-alpha",
  name: "alpha",
  path: "/Users/dev/src/alpha",
  remote_url: "git@github.com:acme/alpha.git",
  last_active: "2025-03-01T00:00:00Z",
});
const beta = makeProject({
  id: "p-beta",
  name: "beta",
  path: "/Users/dev/src/beta",
  is_git_repo: false,
  last_active: "2025-04-01T00:00:00Z",
});

function setStore(overrides: Record<string, unknown>) {
  state.store = {
    projects: [],
    currentProject: null,
    isLoading: false,
    loadProjects: vi.fn(async () => {}),
    selectProject: vi.fn(async () => {}),
    deleteProject: vi.fn(async () => {}),
    refreshCurrentProject: vi.fn(async () => {}),
    ...overrides,
  };
}

beforeEach(() => {
  state.navigate.mockReset();
  state.navigateToPicker.mockReset();
});

describe("Settings → Projects", () => {
  it("lists every project, not just the current one, and marks the current one", async () => {
    setStore({ projects: [beta, alpha], currentProject: alpha });
    renderWithQuery(<ProjectsSection />);

    const table = await screen.findByTestId("settings-projects-table");
    const rows = within(table).getAllByRole("row").slice(1);
    expect(rows).toHaveLength(2);
    // The current project sorts first even though beta is more recent.
    expect(rows[0]).toHaveAttribute("data-testid", "settings-project-row-p-alpha");
    expect(rows[0]).toHaveAttribute("aria-current", "true");
    expect(rows[1]).not.toHaveAttribute("aria-current");

    // Home is collapsed, and the source column names the remote.
    expect(within(rows[0]).getByText("acme/alpha")).toBeInTheDocument();
    expect(rows[0]).toHaveTextContent("~/src/");
    expect(within(rows[1]).getByText("Folder")).toBeInTheDocument();
  });

  it("offers Add project in the header, which goes to the picker", async () => {
    setStore({ projects: [alpha], currentProject: alpha });
    renderWithQuery(<ProjectsSection />);

    await userEvent.click(screen.getByTestId("settings-projects-add"));
    expect(state.navigateToPicker).toHaveBeenCalledTimes(1);
  });

  it("switches to another project and leaves Settings for it", async () => {
    setStore({ projects: [alpha, beta], currentProject: alpha });
    renderWithQuery(<ProjectsSection />);

    await userEvent.click(screen.getByRole("button", { name: "Open beta" }));
    await waitFor(() => expect(state.store.selectProject).toHaveBeenCalledWith(beta));
    expect(state.navigate).toHaveBeenCalledWith(
      expect.objectContaining({ to: "/project/$projectId", params: { projectId: "p-beta" } }),
    );
  });

  it("asks before removing a project, then removes it through the store", async () => {
    setStore({ projects: [alpha, beta], currentProject: alpha });
    renderWithQuery(<ProjectsSection />);

    await userEvent.click(screen.getByRole("button", { name: "Remove beta from Reliant" }));
    // Nothing is removed until the user confirms.
    expect(state.store.deleteProject).not.toHaveBeenCalled();
    expect(screen.getByText(/nothing on disk is deleted/i)).toBeInTheDocument();

    await userEvent.click(screen.getByTestId("remove-project-confirm"));
    await waitFor(() => expect(state.store.deleteProject).toHaveBeenCalledWith("p-beta"));
  });

  it("shows a designed empty state with Add project when there are no projects", async () => {
    setStore({ projects: [] });
    renderWithQuery(<ProjectsSection />);

    expect(await screen.findByText("No projects yet")).toBeInTheDocument();
    expect(screen.queryByTestId("settings-projects-table")).toBeNull();
    // Header action + empty-state action both lead to the picker.
    expect(screen.getAllByRole("button", { name: /add project/i })).toHaveLength(2);
  });

  it("shows an error with a retry when projects fail to load", async () => {
    const loadProjects = vi
      .fn()
      .mockRejectedValueOnce(new Error("daemon unreachable"))
      .mockResolvedValueOnce(undefined);
    setStore({ projects: [], loadProjects });
    renderWithQuery(<ProjectsSection />);

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("daemon unreachable");
    expect(screen.queryByText("No projects yet")).toBeNull();

    await userEvent.click(within(alert).getByRole("button", { name: /try again/i }));
    await waitFor(() => expect(loadProjects).toHaveBeenCalledTimes(2));
  });
});
