/**
 * `/m/projects` — the chat list's project filter. Pins that a row sets the
 * filter through `setProjectFilter` (not a local re-implementation) and
 * returns to the chat list, that "All projects" is a choice of its own, and
 * that the active scope is marked without a round trip.
 */

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import { seedProjects, type ProjectFixture } from "@/test/projectList";
import { useWorkspaceStateStore } from "@/store/workspaceStateStore";

const navigate = vi.fn();

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => navigate,
}));

const setProjectFilter = vi.fn((_scope: string | null, _options?: unknown) => Promise.resolve());

vi.mock("../../../store/projectStore", () => ({
  useProjectStore: (selector: (s: unknown) => unknown) => selector({ setProjectFilter }),
}));

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

const { MobileProjectList } = await import("../MobileProjectList");

const rowOf = (text: string) => screen.getByText(text).closest("button")!;
const isChecked = (row: HTMLElement) => row.querySelector("svg.lucide-check") !== null;

beforeEach(() => {
  navigate.mockReset();
  setProjectFilter.mockClear();
  projects = [
    {
      id: "p1",
      name: "reliant",
      path: "/home/user/reliant",
      last_active: new Date(Date.now() - 3600_000).toISOString(),
    },
    { id: "p2", name: "forge", path: "/home/workspace/projects/forge" },
  ];
  useWorkspaceStateStore.setState({ projectFilterId: "p1" });
});

describe("MobileProjectList", () => {
  it("marks the filtered project with a check mark, not the others", () => {
    renderList();
    expect(isChecked(rowOf("reliant"))).toBe(true);
    expect(isChecked(rowOf("forge"))).toBe(false);
    expect(isChecked(rowOf("All projects"))).toBe(false);
  });

  it("marks All projects when there is no filter", () => {
    useWorkspaceStateStore.setState({ projectFilterId: null });
    renderList();
    expect(isChecked(rowOf("All projects"))).toBe(true);
  });

  it("filters to a different project and navigates back to the chat list", async () => {
    const user = userEvent.setup();
    renderList();

    await user.click(screen.getByText("forge"));

    expect(setProjectFilter).toHaveBeenCalledWith("p2", { restoreWorkspace: false });
    expect(navigate).toHaveBeenCalledWith({ to: "/m/chats" });
  });

  // The freeze: the switch used to be awaited before navigating, behind a row
  // spinner, so every serial RPC it made (chats, worktree, main worktree, the
  // remembered chat) — none with a timeout — held the user on this screen. A
  // switch that never settles must still take the user to the chat list.
  it("navigates without waiting for the switch to finish", async () => {
    setProjectFilter.mockImplementationOnce(() => new Promise<void>(() => {}));
    const user = userEvent.setup();
    renderList();

    await user.click(screen.getByText("forge"));

    expect(setProjectFilter).toHaveBeenCalledTimes(1);
    expect(navigate).toHaveBeenCalledWith({ to: "/m/chats" });
    // No row is left disabled behind a pending switch.
    expect(rowOf("forge")).not.toBeDisabled();
  });

  it("shows every project with All projects", async () => {
    const user = userEvent.setup();
    renderList();

    await user.click(screen.getByText("All projects"));

    expect(setProjectFilter).toHaveBeenCalledWith(null, { restoreWorkspace: false });
    expect(navigate).toHaveBeenCalledWith({ to: "/m/chats" });
  });

  it("tapping the active scope just navigates back", async () => {
    const user = userEvent.setup();
    renderList();

    await user.click(screen.getByText("reliant"));

    expect(setProjectFilter).not.toHaveBeenCalled();
    expect(navigate).toHaveBeenCalledWith({ to: "/m/chats" });
  });

  it("navigating back without choosing changes nothing", async () => {
    const user = userEvent.setup();
    renderList();

    await user.click(screen.getByRole("button", { name: "Back to chats" }));

    expect(setProjectFilter).not.toHaveBeenCalled();
    expect(navigate).toHaveBeenCalledWith({ to: "/m/chats" });
  });

  it("hides a cloud machine's checkout path, and shows last-active when there is one", () => {
    renderList();
    expect(screen.getByText("/home/user/reliant")).toBeInTheDocument();
    expect(rowOf("forge").textContent).not.toContain("/home/workspace");
    expect(screen.getByText(/active 1h ago/i)).toBeInTheDocument();
    expect(rowOf("forge").textContent).not.toMatch(/active/i);
  });
});
