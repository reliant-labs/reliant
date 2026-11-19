/**
 * The sidebar's project filter: "All projects" or one project, plus Add
 * project and Manage projects. Its items are a section of the Chats heading's
 * one menu (ChatListMenu). The project is a filter on the chat list, so
 * choosing one reports the scope and nothing else.
 */
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import type { Project } from "../../../hooks/project-queries";
import { ChatListMenu } from "../ChatListMenu";
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

  it("lists All projects first, then projects by recent use, and reports the choice", async () => {
    const props = renderMenu("b");
    await userEvent.click(screen.getByTestId("project-filter-trigger"));

    const items = within(projectGroup())
      .getAllByRole("menuitemradio")
      .map((item) => item.textContent);
    expect(items[0]).toContain("All projects");
    expect(items.slice(1).map((text) => text?.replace(/\d+$/, ""))).toEqual(["Beta", "Alpha"]);

    await userEvent.click(screen.getByTestId("project-filter-all"));
    expect(props.onFilterChange).toHaveBeenCalledWith(null);
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
    expect(screen.getByTestId("project-filter-b")).toHaveTextContent("2");
    expect(screen.getByTestId("project-filter-all")).toHaveTextContent("2");
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
