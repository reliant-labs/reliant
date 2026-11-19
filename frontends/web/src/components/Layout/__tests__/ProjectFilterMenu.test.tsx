/**
 * The sidebar's project filter: "All projects" or one project, plus Add
 * project and Manage projects. Its items are a section of the Chats heading's
 * one menu (ChatListMenu): All projects and the filtered one, and every
 * other project in the project selector "Choose a project…" opens. The
 * project is a filter on the chat list, so choosing one reports the scope
 * and nothing else.
 */
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import type { Project } from "../../../hooks/project-queries";
import { ChatListMenu } from "../ChatListMenu";

// The selector reads the catalog; with none, it lists the plain projects (its fallback).
vi.mock("@/hooks/project-catalog-queries", () => ({
  useProjectCatalog: () => ({ catalog: undefined, isLoading: false, error: new Error("no catalog here"), refetch: () => {} }),
}));
vi.mock("@/hooks/sidebar-project-queries", () => ({
  useSidebarProjectPrefs: () => ({ prefs: { pinned: new Set(), hidden: new Set(), showAll: false } }),
}));
vi.mock("@/hooks/project-queries", async (original) => ({
  ...(await original<typeof import("@/hooks/project-queries")>()),
  useProjects: () => ({ projects, isLoading: false, error: null, refetch: () => {} }),
}));
vi.mock("@/components/Apps/startBuilding", () => ({ useStartBuildingApp: () => () => {} }));

import { projectFilterLabel } from "../ProjectFilterMenu";

const project = (id: string, name: string, last_active: string): Project =>
  ({ id, name, path: `/p/${id}`, last_active, is_git_repo: true, is_forge: false }) as Project;

const projects = [project("a", "Alpha", "2025-01-01T00:00:00Z"), project("b", "Beta", "2025-02-01T00:00:00Z")];

function renderMenu(
  filterId: string | null,
  extra: { needsYouByProject?: ReadonlyMap<string, number> } = {},
) {
  const props = {
    filterId,
    projects,
    onFilterChange: vi.fn(),
    onAddProject: vi.fn(),
    onManageProjects: vi.fn(),
    sortOrder: "newest_first" as const,
    onSortChange: vi.fn(),
    viewMode: "grouped" as const,
    onViewChange: vi.fn(),
    archived: false,
    onArchivedChange: vi.fn(),
    ...extra,
  };
  render(<ChatListMenu {...props} />);
  return props;
}

const projectGroup = () => screen.getByRole("group", { name: "Project" });

describe("the project filter, in the Chats menu", () => {
  it("names the scope it shows", () => {
    expect(projectFilterLabel(null, projects)).toBe("All projects");
    expect(projectFilterLabel("b", projects)).toBe("Beta");
    renderMenu("b");
    expect(screen.getByTestId("project-filter-trigger")).toHaveAccessibleName("Chat list options. Showing Beta");
  });

  it("lists All projects and the filtered project, and reports the choice", async () => {
    const props = renderMenu("b");
    await userEvent.click(screen.getByTestId("project-filter-trigger"));

    const items = within(projectGroup())
      .getAllByRole("menuitemradio")
      .map((item) => item.textContent?.replace(/\d+$/, ""));
    expect(items).toEqual(["All projects", "Beta"]);

    await userEvent.click(screen.getByTestId("project-filter-all"));
    expect(props.onFilterChange).toHaveBeenCalledWith(null);
  });

  it("picks any other project in the project selector, most recently used first", async () => {
    const props = renderMenu("b");
    await userEvent.click(screen.getByTestId("project-filter-trigger"));
    await userEvent.click(screen.getByTestId("project-filter-choose"));
    const dialog = await screen.findByRole("dialog", { name: "Show one project's chats" });
    const rows = within(dialog).getAllByRole("option").map((o) => o.dataset.testid);
    expect(rows).toEqual(["project-selector-all-projects", "project-selector-row-b", "project-selector-row-a"]);
    await userEvent.click(within(dialog).getByTestId("project-selector-row-a"));
    expect(props.onFilterChange).toHaveBeenCalledWith("a");
  });

  it("offers Add project and Manage projects", async () => {
    const props = renderMenu(null);
    await userEvent.click(screen.getByTestId("project-filter-trigger"));
    await userEvent.click(screen.getByTestId("project-filter-add"));
    expect(props.onAddProject).toHaveBeenCalledTimes(1);

    await userEvent.click(screen.getByTestId("project-filter-trigger"));
    await userEvent.click(screen.getByTestId("project-filter-manage"));
    expect(props.onManageProjects).toHaveBeenCalledTimes(1);
  });

  it("shows how many chats wait on the user in each project", async () => {
    renderMenu("a", { needsYouByProject: new Map([["b", 2]]) });
    await userEvent.click(screen.getByTestId("project-filter-trigger"));
    expect(screen.getByTestId("project-filter-all")).toHaveTextContent("2");
    await userEvent.click(screen.getByTestId("project-filter-choose"));
    expect(await screen.findByTestId("project-selector-row-b")).toHaveTextContent("2 waiting on you");
  });

  it("holds sort, view and the archived list in the same menu", async () => {
    const props = renderMenu(null);
    await userEvent.click(screen.getByTestId("project-filter-trigger"));
    await userEvent.click(screen.getByRole("menuitemradio", { name: "Oldest first" }));
    expect(props.onSortChange).toHaveBeenCalledWith("oldest_first");

    await userEvent.click(screen.getByTestId("project-filter-trigger"));
    await userEvent.click(screen.getByRole("menuitemradio", { name: "Flat list" }));
    expect(props.onViewChange).toHaveBeenCalledWith("flat");

    await userEvent.click(screen.getByTestId("project-filter-trigger"));
    await userEvent.click(screen.getByTestId("chat-list-archived"));
    expect(props.onArchivedChange).toHaveBeenCalledWith(true);
  });
});
