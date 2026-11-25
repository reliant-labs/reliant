/**
 * The Chats heading's one menu (ChatListMenu) keeps Add project and Manage
 * projects; which projects the list shows is the shell's project scope (the
 * nav's switcher), not a control in the heading.
 */
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import type { Project } from "../../../hooks/project-queries";
import { ChatListMenu } from "../ChatListMenu";

// The selector reads the catalog; with none, it lists the plain projects (its fallback).
vi.mock("@/hooks/project-catalog-queries", () => ({
  useProjectCatalog: () => ({ catalog: undefined, isLoading: false, error: new Error("no catalog here"), refetch: () => {} }),
}));
vi.mock("@/hooks/sidebar-project-queries", () => ({
  useSidebarProjectPrefs: () => ({ prefs: { pinned: new Set(), hidden: new Set(), showAll: false, filter: [] } }),
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


describe("the project filter, in the Chats menu", () => {
  it("names the scope it shows", () => {
    expect(projectFilterLabel(null, projects)).toBe("All projects");
    expect(projectFilterLabel("b", projects)).toBe("Beta");
    renderMenu("b");
    expect(screen.getByTestId("project-filter-trigger")).toHaveAccessibleName("Chat list options. Showing Beta");
  });

  it("lists no projects of its own: the filter beside it does", async () => {
    renderMenu("b");
    await userEvent.click(screen.getByTestId("project-filter-trigger"));
    expect(screen.queryByRole("group", { name: "Project" })).toBeNull();
    expect(screen.queryByTestId("project-filter-all")).toBeNull();
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
