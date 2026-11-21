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
}));

vi.mock("@tanstack/react-router", () => ({ useNavigate: () => state.navigate, useSearch: () => state.search }));
vi.mock("@/hooks/project-queries", () => ({
  useProjects: () => ({ projects: state.projects, isLoading: false }),
}));
const startBuilding = vi.fn();
vi.mock("../../Apps/startBuilding", () => ({ useStartBuildingApp: () => startBuilding }));
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
