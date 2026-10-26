/**
 * The sidebar's project filter: "All projects" or one project, plus Add
 * project and Manage projects. The project is a filter on the chat list, so
 * choosing one reports the scope and nothing else.
 */
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import type { Project } from "../../../hooks/project-queries";
import { ProjectFilterMenu } from "../ProjectFilterMenu";

const project = (id: string, name: string, last_active: string): Project =>
  ({ id, name, path: `/p/${id}`, last_active, is_git_repo: true, is_forge: false }) as Project;

const projects = [project("a", "Alpha", "2025-01-01T00:00:00Z"), project("b", "Beta", "2025-02-01T00:00:00Z")];

function renderMenu(filterId: string | null, extra: Partial<Parameters<typeof ProjectFilterMenu>[0]> = {}) {
  const props = {
    filterId,
    projects,
    onChange: vi.fn(),
    onAddProject: vi.fn(),
    onManageProjects: vi.fn(),
    ...extra,
  };
  render(<ProjectFilterMenu {...props} />);
  return props;
}

describe("ProjectFilterMenu", () => {
  it("names the scope it shows", () => {
    renderMenu(null);
    expect(screen.getByTestId("project-filter-label")).toHaveTextContent("All projects");
  });

  it("names the filtered project", () => {
    renderMenu("b");
    expect(screen.getByTestId("project-filter-label")).toHaveTextContent("Beta");
  });

  it("lists All projects first, then projects by recent use, and reports the choice", async () => {
    const props = renderMenu("b");
    await userEvent.click(screen.getByTestId("project-filter-trigger"));

    const items = screen.getAllByRole("menuitemradio").map((item) => item.textContent);
    expect(items[0]).toContain("All projects");
    expect(items.slice(1).map((text) => text?.replace(/\d+$/, ""))).toEqual(["Beta", "Alpha"]);

    await userEvent.click(screen.getByTestId("project-filter-all"));
    expect(props.onChange).toHaveBeenCalledWith(null);
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
});
