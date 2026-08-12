/**
 * `/m/projects` — the project switcher. Pins that selecting a project calls
 * `selectProject` (not some local re-implementation) and returns to the chat
 * list, and that the current project is marked without needing a round trip.
 */

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

const navigate = vi.fn();

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => navigate,
}));

const loadProjects = vi.fn();
const selectProject = vi.fn(() => Promise.resolve());

interface StoreState {
  projects: Array<{
    id: string;
    name: string;
    path: string;
    worktree_count?: number;
    last_active?: string;
  }>;
  currentProject: { id: string; name: string; path: string } | null;
  isLoading: boolean;
  loadProjects: typeof loadProjects;
  selectProject: typeof selectProject;
}

let storeState: StoreState;

vi.mock("../../../store/projectStore", () => ({
  useProjectStore: (selector: (s: unknown) => unknown) => selector(storeState),
}));

const { MobileProjectList } = await import("../MobileProjectList");

beforeEach(() => {
  navigate.mockReset();
  loadProjects.mockReset();
  selectProject.mockClear();
  storeState = {
    projects: [
      {
        id: "p1",
        name: "reliant",
        path: "/home/user/reliant",
        worktree_count: 3,
        last_active: new Date(Date.now() - 3600_000).toISOString(),
      },
      { id: "p2", name: "forge", path: "/home/user/forge" },
    ],
    currentProject: { id: "p1", name: "reliant", path: "/home/user/reliant" },
    isLoading: false,
    loadProjects,
    selectProject,
  };
});

describe("MobileProjectList", () => {
  it("marks the current project with a check mark, not the other one", () => {
    render(<MobileProjectList />);
    const reliantRow = screen.getByText("reliant").closest("button")!;
    const forgeRow = screen.getByText("forge").closest("button")!;
    expect(reliantRow.querySelector("svg.lucide-check")).not.toBeNull();
    expect(forgeRow.querySelector("svg.lucide-check")).toBeNull();
  });

  it("lists every project", () => {
    render(<MobileProjectList />);
    expect(screen.getByText("reliant")).toBeInTheDocument();
    expect(screen.getByText("forge")).toBeInTheDocument();
  });

  it("selects a different project and navigates back to the chat list", async () => {
    const user = userEvent.setup();
    render(<MobileProjectList />);

    await user.click(screen.getByText("forge"));

    expect(selectProject).toHaveBeenCalledWith(
      expect.objectContaining({ id: "p2", name: "forge" }),
    );
    expect(navigate).toHaveBeenCalledWith({ to: "/m/chats" });
  });

  it("navigating back without selecting doesn't call selectProject", async () => {
    const user = userEvent.setup();
    render(<MobileProjectList />);

    await user.click(screen.getByRole("button", { name: "Back to chats" }));

    expect(selectProject).not.toHaveBeenCalled();
    expect(navigate).toHaveBeenCalledWith({ to: "/m/chats" });
  });

  it("tapping the already-current project just navigates back", async () => {
    const user = userEvent.setup();
    render(<MobileProjectList />);

    await user.click(screen.getByText("reliant"));

    expect(selectProject).not.toHaveBeenCalled();
    expect(navigate).toHaveBeenCalledWith({ to: "/m/chats" });
  });

  it("shows workspace count and last-active for a project that has them", () => {
    render(<MobileProjectList />);
    expect(screen.getByText(/3 workspaces/)).toBeInTheDocument();
    expect(screen.getByText(/active 1h ago/i)).toBeInTheDocument();
  });

  it("omits the metadata line entirely for a project with none of it", () => {
    render(<MobileProjectList />);
    const forgeRow = screen.getByText("forge").closest("button")!;
    expect(forgeRow.textContent).not.toMatch(/workspace/);
    expect(forgeRow.textContent).not.toMatch(/active/i);
  });
});
