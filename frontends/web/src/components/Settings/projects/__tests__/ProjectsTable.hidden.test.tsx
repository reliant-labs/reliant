import { fireEvent, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";

import type { Project } from "@/store/projectStore";

import { ProjectsTable } from "../ProjectsTable";

vi.mock("@/components/forge-ui/tooltip", () => ({
  default: ({ children }: { children: ReactNode }) => <>{children}</>,
}));

const project = (id: string): Project => ({
  id,
  name: `Project ${id}`,
  path: `/p/${id}`,
  is_git_repo: true,
  is_forge: false,
  created_at: "2025-01-01T00:00:00.000Z",
  updated_at: "2025-01-01T00:00:00.000Z",
  last_active: "2025-01-01T00:00:00.000Z",
});

describe("ProjectsTable sidebar visibility", () => {
  it("marks a project hidden from the sidebar and offers Unhide; the others offer Hide", () => {
    const onSetHidden = vi.fn();
    render(
      <ProjectsTable
        projects={[project("a"), project("b")]}
        onOpen={vi.fn()}
        onRemove={vi.fn()}
        hiddenProjectIds={new Set(["a"])}
        onSetHidden={onSetHidden}
      />,
    );
    expect(screen.getByTestId("settings-project-hidden-a")).toHaveTextContent("Hidden from sidebar");
    expect(screen.queryByTestId("settings-project-hidden-b")).toBeNull();

    fireEvent.click(screen.getByTestId("settings-project-unhide-a"));
    expect(onSetHidden).toHaveBeenLastCalledWith("a", false);
    fireEvent.click(screen.getByTestId("settings-project-hide-b"));
    expect(onSetHidden).toHaveBeenLastCalledWith("b", true);
  });

  it("has no hide action without onSetHidden", () => {
    render(<ProjectsTable projects={[project("a")]} onOpen={vi.fn()} onRemove={vi.fn()} />);
    expect(screen.queryByTestId("settings-project-hide-a")).toBeNull();
  });
});
