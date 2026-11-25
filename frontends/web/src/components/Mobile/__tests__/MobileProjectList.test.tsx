/**
 * `/m/projects` — the chat list's project scope. Pins that a row only
 * navigates: to the chat list with the pick in the URL (`?projects=<id>|all`,
 * a push, so Back undoes it), never through a store; that "All projects" is a
 * choice of its own; and that the check mark reads the URL's scope.
 */

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import { seedProjects, type ProjectFixture } from "@/test/projectList";

const navigate = vi.fn();
// The page's URL search: the chat list's scope rides along to /m/projects.
let routeSearch: Record<string, unknown> = {};

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => navigate,
  useSearch: () => routeSearch,
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

/** Where the last navigation went, with the search it wrote over `routeSearch`. */
function landed() {
  const call = navigate.mock.calls.at(-1)?.[0] as { to: string; search?: (prev: Record<string, unknown>) => Record<string, unknown>; replace?: boolean };
  return { to: call.to, search: call.search ? call.search(routeSearch) : routeSearch, replace: call.replace };
}

beforeEach(() => {
  navigate.mockReset();
  projects = [
    {
      id: "p1",
      name: "reliant",
      path: "/home/user/reliant",
      last_active: new Date(Date.now() - 3600_000).toISOString(),
    },
    { id: "p2", name: "forge", path: "/home/workspace/projects/forge" },
  ];
  routeSearch = { projects: "p1", tour: "t" };
});

describe("MobileProjectList", () => {
  it("marks the URL's project with a check mark, not the others", () => {
    renderList();
    expect(isChecked(rowOf("reliant"))).toBe(true);
    expect(isChecked(rowOf("forge"))).toBe(false);
    expect(isChecked(rowOf("All projects"))).toBe(false);
  });

  it("marks All projects when the URL scopes to every project, or names none", () => {
    routeSearch = { projects: "all" };
    const view = renderList();
    expect(isChecked(rowOf("All projects"))).toBe(true);
    view.unmount();
    routeSearch = {};
    renderList();
    expect(isChecked(rowOf("All projects"))).toBe(true);
  });

  it("puts a picked project in the chat list's URL (a push)", async () => {
    const user = userEvent.setup();
    renderList();

    await user.click(screen.getByText("forge"));

    expect(navigate).toHaveBeenCalledTimes(1);
    expect(landed()).toEqual({ to: "/m/chats", search: { projects: "p2", focus: undefined, tour: "t" }, replace: undefined });
  });

  it("shows every project with All projects", async () => {
    const user = userEvent.setup();
    renderList();

    await user.click(screen.getByText("All projects"));

    expect(landed()).toEqual({ to: "/m/chats", search: { projects: "all", focus: undefined, tour: "t" }, replace: undefined });
  });

  it("tapping the active scope lands on the same scope", async () => {
    const user = userEvent.setup();
    renderList();

    await user.click(screen.getByText("reliant"));

    expect(landed().search).toMatchObject({ projects: "p1" });
  });

  it("navigating back without choosing changes nothing", async () => {
    const user = userEvent.setup();
    renderList();

    await user.click(screen.getByRole("button", { name: "Back to chats" }));

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
