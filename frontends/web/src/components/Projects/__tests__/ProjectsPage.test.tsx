// Copyright (c) 2026 Reliant Labs

/**
 * /projects (U6): the person's apps — All, Apps, or Sites (every hosted site,
 * with its domains; the retired /sites page's list) — and their code
 * projects, each opening its chats.
 */

import { fireEvent, render, screen, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const state = vi.hoisted(() => ({
  apps: [] as unknown[],
  projects: [] as unknown[],
  navigate: vi.fn(),
  search: {} as Record<string, unknown>,
  setup: "ready" as string,
  several: null as "all" | "many" | null,
}));

vi.mock("@tanstack/react-router", () => ({ useNavigate: () => state.navigate, useSearch: () => state.search }));
vi.mock("@/hooks/project-queries", () => ({
  useProjects: () => ({ projects: state.projects, isLoading: false }),
}));
vi.mock("../../OnboardingFlow/useChatSetup", () => ({ useChatSetup: () => state.setup }));
const startBuilding = vi.fn();
vi.mock("../../Apps/startBuilding", () => ({ useStartBuildingApp: () => startBuilding }));
// The summary of several projects (the project shell's Home) has its own tests.
vi.mock("../../Forge/Console/ProjectsOverview", () => ({ ProjectsOverview: () => null }));
// One project in the shell: the page lists every project (the Home of several is ProjectsOverview's).
vi.mock("../../Layout/sidebar/shellProject", () => ({ useShellSeveral: () => ({ mode: state.several, rows: [] }) }));
vi.mock("../../Layout/AreaShell", () => ({ AreaShell: ({ children }: { children: ReactNode }) => <>{children}</> }));
// The apps list and the sites list are covered where they live: here, which one shows.
vi.mock("../../Apps/AppsPage", () => ({
  AppsBody: () => <div data-testid="apps-body">{(state.apps as { name: string }[]).map((app) => app.name).join(",")}</div>,
}));
vi.mock("../../ForgeShell/ForgeSitesPage", () => ({
  SitesList: ({ header }: { header?: boolean }) => <div data-testid="sites-list" data-header={String(header ?? true)} />,
}));

import { ProjectsPage } from "../ProjectsPage";

const live = { name: "shop", envs: [{ links: [{ url: "https://shop.example" }] }], previews: 0 };
const dark = { name: "worker", envs: [{ links: [] }], previews: 0 };

beforeEach(() => {
  state.apps = [live, dark];
  state.projects = [
    { id: "p1", name: "old", last_active: "2026-01-01T00:00:00Z" },
    { id: "p2", name: "new", last_active: "2026-11-01T00:00:00Z" },
  ];
  state.navigate.mockReset();
  state.search = {};
  state.setup = "ready";
  state.several = null;
});

describe("ProjectsPage with several projects in the shell", () => {
  it("is their Home: the summary alone, with a quiet way to browse every project", () => {
    state.several = "all";
    render(<ProjectsPage />);
    expect(screen.getByTestId("projects-page")).toHaveAttribute("data-mode", "home");
    expect(screen.queryByRole("heading", { name: "Projects" })).toBeNull();
    expect(screen.queryByTestId("apps-body")).toBeNull();
    expect(screen.queryByTestId("projects-code")).toBeNull();
    fireEvent.click(screen.getByTestId("projects-browse-all"));
    expect(state.navigate).toHaveBeenCalledWith({ href: "/projects?browse=1" });
  });

  it("lists every project when asked to browse", () => {
    state.several = "all";
    state.search = { browse: 1 };
    render(<ProjectsPage />);
    expect(screen.getByTestId("apps-body")).toBeInTheDocument();
  });
});

describe("ProjectsPage", () => {
  it("shows every app and the code, newest first, each opening its chats", () => {
    render(<ProjectsPage />);
    expect(screen.getByTestId("apps-body")).toHaveTextContent("shop,worker");
    const code = within(screen.getByTestId("projects-code"));
    const rows = code.getAllByRole("link");
    expect(rows.map((row) => row.textContent)).toEqual([expect.stringContaining("new"), expect.stringContaining("old")]);
    expect(rows[0]).toHaveAttribute("href", "/project/p2");
    fireEvent.click(rows[0]);
    expect(state.navigate).toHaveBeenCalledWith({ href: "/project/p2" });
  });

  it("keeps the filter in the URL", () => {
    render(<ProjectsPage />);
    fireEvent.click(screen.getByRole("tab", { name: "Sites" }));
    const [{ search, replace }] = state.navigate.mock.calls[0] as [{ search: (prev: object) => object; replace: boolean }];
    expect(search({ q: "x" })).toEqual({ q: "x", show: "sites" });
    expect(replace).toBe(true);
  });

  it("narrows to apps, or to the sites list (the retired /sites page's)", () => {
    state.search = { show: "apps" };
    const view = render(<ProjectsPage />);
    expect(screen.getByTestId("apps-body")).toHaveTextContent("shop,worker");
    expect(screen.queryByTestId("projects-code")).toBeNull();
    expect(screen.queryByTestId("sites-list")).toBeNull();
    view.unmount();
    state.search = { show: "sites" };
    render(<ProjectsPage />);
    // Headed by the page, so the list carries no header of its own.
    expect(screen.getByTestId("sites-list")).toHaveAttribute("data-header", "false");
    expect(screen.queryByTestId("apps-body")).toBeNull();
    expect(screen.queryByTestId("projects-code")).toBeNull();
  });
});

describe("ProjectsPage's Build an app", () => {
  it.each([[{}], [{ show: "apps" }]])("heads the page while it shows apps (%o)", (search) => {
    state.search = search;
    render(<ProjectsPage />);
    fireEvent.click(screen.getByRole("button", { name: "Build an app" }));
    expect(startBuilding).toHaveBeenCalledTimes(1);
    startBuilding.mockReset();
  });

  it("is not offered on the sites list", () => {
    state.search = { show: "sites" };
    render(<ProjectsPage />);
    expect(screen.queryByRole("button", { name: "Build an app" })).toBeNull();
  });
});

/**
 * The daemonless home (U6): a person who cannot run a chat yet lands here,
 * and the way to set chats up leads the page.
 */
describe("ProjectsPage's Set up chats card", () => {
  it.each([
    ["onboard", "/onboarding"],
    ["connect-machine", "/settings/machines"],
  ])("leads the page for %s, linking to %s", (setup, href) => {
    state.setup = setup;
    render(<ProjectsPage />);
    const card = screen.getByTestId("set-up-chats-card");
    expect(card).toHaveTextContent("Chats run your agents in an agent sandbox");
    const link = within(card).getByRole("link", { name: "Set up" });
    expect(link).toHaveAttribute("href", href);
    fireEvent.click(link);
    expect(state.navigate).toHaveBeenCalledWith({ href });
  });

  it.each(["ready", "checking", "unknown"])("is not there for %s", (setup) => {
    state.setup = setup;
    render(<ProjectsPage />);
    expect(screen.queryByTestId("set-up-chats-card")).toBeNull();
  });
});
