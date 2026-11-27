// Copyright (c) 2026 Reliant Labs

/**
 * The public demo's nav body (DemoNav): the shell's one nav column with the
 * demo's config. Always the sample shop's sections (the URL of a demo page
 * names no app), no Find (no palette), the drill-in's own anchor on an
 * Observability route, and on a phone the demo's actions in the drawer.
 */

import { render, screen, within } from "@testing-library/react";
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

const narrow = vi.hoisted(() => ({ value: false }));
vi.mock("@/hooks/useNarrowViewport", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/useNarrowViewport")>()),
  useNarrowViewport: () => narrow.value,
}));
// The scope as an all-projects pick that names no app: DemoNav still draws shop's sections, one project.
vi.mock("@/components/Layout/sidebar/shellProject", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/components/Layout/sidebar/shellProject")>()),
  useShellProject: () => ({ mode: "all", ids: ["p1", "p2"], key: "p1", name: "shop", projectId: "p1", app: null, deployed: false, row: undefined, catalog: undefined }),
}));
vi.mock("@/components/Forge/Console/useConsoleNavModel", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/components/Forge/Console/useConsoleNavModel")>()),
  useConsoleNavModel: (project: string) => ({ project, page: { kind: "overview" }, env: "prod", envs: [], alertsFiring: 1, starred: [], customDashboards: [], navCounts: { tests: 1 } }),
}));
// The header's org row and switcher read the org and the catalog: their own tests.
vi.mock("@/components/Layout/sidebar/ProjectSwitcher", () => ({
  OrgHeaderRow: () => { throw new Error("Public demo must not mount live org controls"); },
  ProjectSwitcherRow: () => { throw new Error("Public demo must not mount live project controls"); },
}));
vi.mock("@/lib/changesFeature", () => ({ changesUIEnabled: () => false }));

import { DemoNav } from "../DemoNav";

function renderAt(href: string) {
  const [pathname, query = ""] = href.split("?");
  const search = Object.fromEntries(new URLSearchParams(query));
  const rootRoute = createRootRoute({ component: () => <DemoNav pathname={pathname} search={search} onNavigate={vi.fn()} /> });
  const router = createRouter({ routeTree: rootRoute, history: createMemoryHistory({ initialEntries: [href] }) });
  render(<RouterProvider router={router} />);
}

beforeEach(() => {
  narrow.value = false;
});

describe("the demo's nav", () => {
  it("draws the sample shop's sections on the demo's own page, Home lit, and none of the app's own rows", async () => {
    renderAt("/forge/demo?section=home");
    const top = await screen.findByTestId("nav-top");
    expect(within(top).getByTestId("nav-demo-home")).toHaveAttribute("aria-current", "page");
    expect(within(top).getByTestId("nav-demo-home")).toHaveAttribute("href", "/forge/demo?section=home");
    // One project, shop: its console rows, not the org-wide pages an all-projects pick would open.
    expect(within(top).getByTestId("sidebar-home-button")).toHaveAttribute("href", "/forge/p/shop");
    expect(within(top).getByTestId("nav-observability")).toHaveAttribute("href", "/forge/p/shop/observability?env=prod");
    expect(within(top).getByTestId("nav-data")).toHaveAttribute("href", "/forge/p/shop/data?env=prod");
    for (const id of ["nav-slot-inbox", "nav-agents", "nav-usage-billing", "sidebar-settings-button"]) expect(screen.queryByTestId(id)).toBeNull();
    // The footer: How it was built, a page of the demo's.
    expect(screen.getByTestId("nav-demo-built")).toHaveAttribute("href", "/forge/demo?section=built");
    // No Find: the demo's keyboard host has no palette.
    expect(screen.queryByTestId("nav-find")).toBeNull();
    expect(screen.getByTestId("demo-project-label")).toHaveTextContent("shop");
    expect(screen.queryByTestId("shell-project-switcher")).toBeNull();
    // The phone's actions are the drawer's only.
    expect(screen.queryByTestId("demo-drawer-actions")).toBeNull();
  });

  it("drills into Observability on its routes, under a header the tour can ring", async () => {
    renderAt("/forge/p/shop/observability/traces?env=prod");
    const drill = await screen.findByTestId("nav-drill-in");
    expect(drill).toHaveAttribute("data-area", "observability");
    expect(within(drill).getByTestId("nav-back")).toHaveAttribute("data-tour", "nav-drill-in");
    expect(within(drill).getByTestId("nav-back")).toHaveAttribute("href", "/forge/p/shop");
    expect(screen.queryByTestId("nav-top")).toBeNull();
    expect(document.querySelector("[data-tour='forge-console-nav']")).toBeNull();
    expect(within(drill).getByTestId("nav-obs-traces")).toHaveAttribute("aria-current", "page");
    expect(within(drill).getByTestId("nav-site-insights")).toBeInTheDocument();
  });

  it("puts the tour, Install Forge and the telemetry notice in a phone's drawer, every row drawn flat", async () => {
    narrow.value = true;
    renderAt("/forge/p/shop?env=prod");
    const actions = await screen.findByTestId("demo-drawer-actions");
    expect(within(actions).getByTestId("demo-start-tour")).toBeInTheDocument();
    expect(within(actions).getByRole("button", { name: "Install Forge" })).toBeInTheDocument();
    // Above the footer's own row, as the drawer had it.
    expect(actions.compareDocumentPosition(screen.getByTestId("nav-demo-built")) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });
});
