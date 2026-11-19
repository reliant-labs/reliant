// Copyright (c) 2026 Reliant Labs

/**
 * /projects (U6): the person's apps — All, Apps, or only the ones with a live
 * site — and their code projects, each opening its chats.
 */

import { fireEvent, render, screen, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const state = vi.hoisted(() => ({
  apps: [] as unknown[],
  projects: [] as unknown[],
  navigate: vi.fn(),
  search: {} as Record<string, unknown>,
  bodyFilter: undefined as undefined | ((app: unknown) => boolean),
}));

vi.mock("@tanstack/react-router", () => ({ useNavigate: () => state.navigate, useSearch: () => state.search }));
vi.mock("@/hooks/apps-queries", () => ({
  useApps: () => ({ apps: state.apps, loading: false, availability: "available", detail: "" }),
}));
vi.mock("@/hooks/project-queries", () => ({
  useProjects: () => ({ projects: state.projects, isLoading: false }),
}));
vi.mock("@/lib/forgeFeature", () => ({ isForgeUIEnabled: () => true }));
vi.mock("../../Layout/AreaShell", () => ({ AreaShell: ({ children }: { children: ReactNode }) => <>{children}</> }));
// The apps list is the Apps page's (covered there): here it says which apps it was asked for.
vi.mock("../../Apps/AppsPage", () => ({
  AppsBody: ({ filter }: { filter?: (app: { name: string }) => boolean }) => {
    const shown = (state.apps as { name: string }[]).filter((app) => !filter || filter(app));
    return <div data-testid="apps-body">{shown.map((app) => app.name).join(",")}</div>;
  },
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

  it("narrows to apps, or to the apps with a live site", () => {
    state.search = { show: "apps" };
    const view = render(<ProjectsPage />);
    expect(screen.getByTestId("apps-body")).toHaveTextContent("shop,worker");
    expect(screen.queryByTestId("projects-code")).toBeNull();
    view.unmount();
    state.search = { show: "sites" };
    render(<ProjectsPage />);
    expect(screen.getByTestId("apps-body")).toHaveTextContent(/^shop$/);
  });

  it("says so when nothing is live yet", () => {
    state.apps = [dark];
    state.search = { show: "sites" };
    render(<ProjectsPage />);
    expect(screen.getByTestId("projects-no-sites")).toBeInTheDocument();
    expect(screen.queryByTestId("apps-body")).toBeNull();
  });
});
