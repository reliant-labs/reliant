/**
 * `/m/projects` — the project switcher. Pins that selecting a project calls
 * `selectProject` (not some local re-implementation) and returns to the chat
 * list, and that the current project is marked without needing a round trip.
 */

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import { seedProjects, type ProjectFixture } from "@/test/projectList";

const navigate = vi.fn();

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => navigate,
}));

const selectProject = vi.fn(() => Promise.resolve());

interface StoreState {
  currentProject: { id: string; name: string; path: string } | null;
  selectProject: typeof selectProject;
}

let storeState: StoreState;
// The project list (ListProjects, read through useProjects).
let projects: ProjectFixture[];

function renderList() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  seedProjects(client, projects);
  return render(
    <QueryClientProvider client={client}>
      <MobileProjectList />
    </QueryClientProvider>,
  );
}

vi.mock("../../../store/projectStore", () => ({
  useProjectStore: (selector: (s: unknown) => unknown) => selector(storeState),
}));

const { MobileProjectList } = await import("../MobileProjectList");

beforeEach(() => {
  navigate.mockReset();
  selectProject.mockClear();
  projects = [
    {
      id: "p1",
      name: "reliant",
      path: "/home/user/reliant",
      last_active: new Date(Date.now() - 3600_000).toISOString(),
    },
    { id: "p2", name: "forge", path: "/home/user/forge" },
  ];
  storeState = {
    currentProject: { id: "p1", name: "reliant", path: "/home/user/reliant" },
    selectProject,
  };
});

describe("MobileProjectList", () => {
  it("marks the current project with a check mark, not the other one", () => {
    renderList();
    const reliantRow = screen.getByText("reliant").closest("button")!;
    const forgeRow = screen.getByText("forge").closest("button")!;
    expect(reliantRow.querySelector("svg.lucide-check")).not.toBeNull();
    expect(forgeRow.querySelector("svg.lucide-check")).toBeNull();
  });

  it("lists every project", () => {
    renderList();
    expect(screen.getByText("reliant")).toBeInTheDocument();
    expect(screen.getByText("forge")).toBeInTheDocument();
  });

  it("selects a different project and navigates back to the chat list", async () => {
    const user = userEvent.setup();
    renderList();

    await user.click(screen.getByText("forge"));

    expect(selectProject).toHaveBeenCalledWith(
      expect.objectContaining({ id: "p2", name: "forge" }),
      { restoreWorkspace: false },
    );
    expect(navigate).toHaveBeenCalledWith({ to: "/m/chats" });
  });

  // The freeze: the switch used to be awaited before navigating, behind a row
  // spinner, so every serial RPC it made (chats, worktree, main worktree, the
  // remembered chat) — none with a timeout — held the user on this screen. A
  // switch that never settles must still take the user to the chat list.
  it("navigates without waiting for the switch to finish", async () => {
    selectProject.mockImplementationOnce(() => new Promise<void>(() => {}));
    const user = userEvent.setup();
    renderList();

    await user.click(screen.getByText("forge"));

    expect(selectProject).toHaveBeenCalledTimes(1);
    expect(navigate).toHaveBeenCalledWith({ to: "/m/chats" });
    // No row is left disabled behind a pending switch.
    expect(screen.getByText("forge").closest("button")).not.toBeDisabled();
  });

  it("navigating back without selecting doesn't call selectProject", async () => {
    const user = userEvent.setup();
    renderList();

    await user.click(screen.getByRole("button", { name: "Back to chats" }));

    expect(selectProject).not.toHaveBeenCalled();
    expect(navigate).toHaveBeenCalledWith({ to: "/m/chats" });
  });

  it("tapping the already-current project just navigates back", async () => {
    const user = userEvent.setup();
    renderList();

    await user.click(screen.getByText("reliant"));

    expect(selectProject).not.toHaveBeenCalled();
    expect(navigate).toHaveBeenCalledWith({ to: "/m/chats" });
  });

  it("shows last-active for a project that has it", () => {
    renderList();
    expect(screen.getByText(/active 1h ago/i)).toBeInTheDocument();
  });

  it("omits the metadata line entirely for a project with none of it", () => {
    renderList();
    const forgeRow = screen.getByText("forge").closest("button")!;
    expect(forgeRow.textContent).not.toMatch(/active/i);
  });
});
