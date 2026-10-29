import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { useProjectStore } from "@/store/projectStore";
import type { Project } from "@/hooks/project-queries";

import { HomeBuildApp } from "../HomeBuildApp";

// The new-chat screen's middle offers "Build an app" only to a project that is
// not a forge project. A forge project's apps are the sidebar's (SidebarApps),
// never a block in the middle of the screen.

function setProject(overrides: Partial<Project>) {
  useProjectStore.setState({
    currentProject: {
      id: "proj-1",
      user_id: "user-1",
      name: "my-project",
      path: "/home/workspace/projects/my-project",
      is_git_repo: true,
      last_active: "2026-10-01T00:00:00Z",
      created_at: "2026-10-01T00:00:00Z",
      updated_at: "2026-10-01T00:00:00Z",
      is_forge: false,
      ...overrides,
    } as Project,
  });
}

afterEach(() => {
  useProjectStore.setState({ currentProject: null });
});

describe("HomeBuildApp", () => {
  it("offers to build an app on a project that is not a forge project", () => {
    setProject({});
    const onStart = vi.fn();
    render(<HomeBuildApp onStart={onStart} />);

    fireEvent.click(screen.getByRole("button", { name: /build an app/i }));
    expect(onStart).toHaveBeenCalledTimes(1);
  });

  it("renders nothing on a forge project", () => {
    setProject({ is_forge: true });
    const { container } = render(<HomeBuildApp onStart={() => {}} />);
    expect(container).toBeEmptyDOMElement();
  });

  it("renders nothing once a daemon has reported the forge project name", () => {
    setProject({ forge_project_name: "storefront" });
    const { container } = render(<HomeBuildApp onStart={() => {}} />);
    expect(container).toBeEmptyDOMElement();
  });
});
