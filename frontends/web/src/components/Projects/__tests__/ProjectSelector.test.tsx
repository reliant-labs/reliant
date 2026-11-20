// Copyright (c) 2025 Reliant Labs

/**
 * The project selector: its rows per mode, keyboard, picking, the fallback
 * for a server without ListProjectsWithApps, and that hundreds of rows render
 * only what is on screen.
 */

import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { Project } from "@/hooks/project-queries";
import type { ProjectCatalog, ProjectCatalogRow, AppOnlyCatalogRow, AppChipModel } from "@/services/projects/catalog";

const navigate = vi.fn();
const startBuilding = vi.fn();
const openAddProject = vi.fn();
const catalogView: { catalog: ProjectCatalog | undefined; isLoading: boolean; error: Error | null } = { catalog: undefined, isLoading: false, error: null };
const plainProjects: { projects: Project[]; isLoading: boolean } = { projects: [], isLoading: false };
let pinned = new Set<string>();

vi.mock("@tanstack/react-router", async (orig) => ({ ...(await orig<object>()), useNavigate: () => navigate }));
vi.mock("@/components/Apps/startBuilding", () => ({ useStartBuildingApp: () => startBuilding }));
vi.mock("@/store/addProjectDialogStore", () => ({ openAddProject: () => openAddProject() }));
vi.mock("@/hooks/project-catalog-queries", () => ({ useProjectCatalog: () => ({ ...catalogView, refetch: () => {} }) }));
vi.mock("@/hooks/project-queries", () => ({ useProjects: () => ({ ...plainProjects, error: null, refetch: () => {} }) }));
vi.mock("@/hooks/sidebar-project-queries", () => ({
  useSidebarProjectPrefs: () => ({ prefs: { pinned, hidden: new Set(["p-hidden"]), showAll: false } }),
}));
// The chips are links; the selector's rows render them inert, but the module still imports the router's Link.
vi.mock("@/components/Projects/AppChip", () => ({
  AppChip: ({ app }: { app: AppChipModel }) => <span data-testid="app-chip">{app.name}</span>,
}));

import { buildItems, ProjectSelectorDialog, ProjectSelectorPanel } from "../ProjectSelector";

function project(id: string, at: number, over: Partial<ProjectCatalogRow> = {}): ProjectCatalogRow {
  return {
    kind: "project",
    key: id,
    name: `Project ${id}`,
    activityAt: at,
    project: { id, name: `Project ${id}`, path: `/src/${id}`, is_git_repo: true, is_forge: false, last_active: "", created_at: "", updated_at: "" },
    shared: false,
    repos: [],
    apps: [],
    checkouts: [],
    hasCheckout: false,
    ...over,
  };
}
const chip = (name: string): AppChipModel => ({ name, link: "source", state: "live", isStaticSite: false, environments: [] });
function appOnly(name: string, at: number): AppOnlyCatalogRow {
  return { kind: "app", key: `app:${name}`, name, activityAt: at, app: chip(name) };
}

function catalog(rows: (ProjectCatalogRow | AppOnlyCatalogRow)[]): ProjectCatalog {
  return { orgId: "org-1", rows, hostedAppsState: "ok" };
}

const ROWS = catalog([
  project("a", 9, { apps: [chip("shop"), chip("shop-admin")] }),
  project("b", 8),
  project("c", 7),
  project("d", 6),
  project("e", 5),
  project("f", 4),
  project("g", 3),
  project("p-hidden", 10, { name: "Secret" }),
  appOnly("billing", 2),
]);

beforeEach(() => {
  catalogView.catalog = ROWS;
  catalogView.isLoading = false;
  catalogView.error = null;
  plainProjects.projects = [];
  pinned = new Set(["f"]);
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

const NO_PREFS = { pinned: new Set<string>(["f"]), hidden: new Set<string>(["p-hidden"]) };
const shape = (items: ReturnType<typeof buildItems>) => items.map((i) => (i.type === "header" ? `# ${i.label}` : i.key));

describe("buildItems", () => {
  it("groups Pinned, Recent and All, hidden rows out, app-only rows in (navigate)", () => {
    expect(shape(buildItems({ mode: "navigate", catalog: ROWS, prefs: NO_PREFS, query: "" }))).toEqual([
      "# Pinned", "f",
      "# Recent", "a", "b", "c", "d", "e",
      "# All", "g", "app:billing",
    ]);
  });

  it("lists projects only to pick a chat's project", () => {
    expect(shape(buildItems({ mode: "chat", catalog: ROWS, prefs: NO_PREFS, query: "" }))).not.toContain("app:billing");
  });

  it("lists one row per app to pick an app, and the names the caller adds", () => {
    expect(shape(buildItems({ mode: "app", catalog: ROWS, prefs: NO_PREFS, query: "", extraApps: ["from-url", "shop"] }))).toEqual([
      "# Apps", "app:shop", "app:shop-admin", "app:billing", "app:from-url",
    ]);
  });

  it("matches an app by its own name or its project's, not a sibling's", () => {
    expect(shape(buildItems({ mode: "app", catalog: ROWS, prefs: NO_PREFS, query: "admin" }))).toEqual(["# Results", "app:shop-admin"]);
    expect(shape(buildItems({ mode: "app", catalog: ROWS, prefs: NO_PREFS, query: "project a" }))).toEqual(["# Results", "app:shop", "app:shop-admin"]);
  });

  it("finds a hidden row while searching, and keeps lead items that match", () => {
    const lead = [{ key: "all", label: "All projects", onSelect: () => {} }];
    expect(shape(buildItems({ mode: "navigate", catalog: ROWS, prefs: NO_PREFS, query: "secret", leadItems: lead }))).toEqual(["# Results", "p-hidden"]);
    expect(shape(buildItems({ mode: "navigate", catalog: ROWS, prefs: NO_PREFS, query: "all", leadItems: lead })).slice(0, 1)).toEqual(["lead:all"]);
  });
});

describe("ProjectSelectorPanel", () => {
  it("opens on the current choice, moves with the arrows past headers and picks with Enter", () => {
    const onSelect = vi.fn();
    const onClose = vi.fn();
    render(<ProjectSelectorPanel mode="chat" selectedKey="b" onSelect={onSelect} onClose={onClose} />);
    const search = screen.getByTestId("project-selector-search");
    const activeId = () => search.getAttribute("aria-activedescendant");
    expect(document.getElementById(activeId()!)?.dataset.testid).toBe("project-selector-row-b");
    fireEvent.keyDown(search, { key: "ArrowUp" });
    fireEvent.keyDown(search, { key: "ArrowUp" }); // a → over the Recent header → f (pinned)
    expect(document.getElementById(activeId()!)?.dataset.testid).toBe("project-selector-row-f");
    fireEvent.keyDown(search, { key: "Enter" });
    expect(onClose).toHaveBeenCalled();
    expect(onSelect).toHaveBeenCalledWith({ kind: "project", row: expect.objectContaining({ key: "f" }) });
  });

  it("starts a search at its first match and says when nothing matches", () => {
    render(<ProjectSelectorPanel mode="navigate" onClose={() => {}} />);
    const search = screen.getByTestId("project-selector-search");
    fireEvent.change(search, { target: { value: "billing" } });
    expect(document.getElementById(search.getAttribute("aria-activedescendant")!)?.dataset.testid).toBe("project-selector-row-app:billing");
    fireEvent.change(search, { target: { value: "zzz" } });
    expect(screen.getByTestId("project-selector-empty").textContent).toContain("zzz");
  });

  it("navigates to a project's page or an app's console by default", () => {
    render(<ProjectSelectorPanel mode="navigate" onClose={() => {}} />);
    fireEvent.click(screen.getByTestId("project-selector-row-a"));
    expect(navigate).toHaveBeenLastCalledWith({ to: "/projects/a" });
    fireEvent.click(screen.getByTestId("project-selector-row-app:billing"));
    expect(navigate).toHaveBeenLastCalledWith({ to: "/forge/p/billing" });
  });

  it("will not pick a row its caller disabled", () => {
    const onSelect = vi.fn();
    render(<ProjectSelectorPanel mode="chat" onSelect={onSelect} onClose={() => {}} rowState={(row) => (row.key === "a" ? { disabled: true, hint: "Folder elsewhere" } : undefined)} />);
    const row = screen.getByTestId("project-selector-row-a");
    expect(row.getAttribute("aria-disabled")).toBe("true");
    expect(within(row).getByText("Folder elsewhere")).toBeTruthy();
    fireEvent.click(row);
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("offers New project and Add an app at the bottom", () => {
    const onClose = vi.fn();
    render(<ProjectSelectorPanel mode="navigate" onClose={onClose} />);
    fireEvent.click(screen.getByTestId("project-selector-new-project"));
    expect(openAddProject).toHaveBeenCalled();
    fireEvent.click(screen.getByTestId("project-selector-add-app"));
    expect(startBuilding).toHaveBeenCalled();
    expect(onClose).toHaveBeenCalledTimes(2);
  });

  it("falls back to the plain project list when the server cannot list apps", () => {
    catalogView.catalog = undefined;
    catalogView.error = new Error("unimplemented");
    plainProjects.projects = [{ id: "old", name: "Old server project", path: "/old", is_git_repo: false, is_forge: false, last_active: "", created_at: "", updated_at: "" }];
    render(<ProjectSelectorPanel mode="chat" onClose={() => {}} />);
    expect(screen.getByTestId("project-selector-row-old").textContent).toContain("Old server project");
  });

  it("says it is loading before the first answer", () => {
    catalogView.catalog = undefined;
    catalogView.isLoading = true;
    render(<ProjectSelectorPanel mode="chat" onClose={() => {}} />);
    expect(screen.getByTestId("project-selector-loading")).toBeTruthy();
  });

  it("renders only the rows on screen of hundreds", () => {
    catalogView.catalog = catalog(Array.from({ length: 500 }, (_, i) => project(`n${i}`, 1000 - i)));
    pinned = new Set();
    render(<ProjectSelectorPanel mode="chat" onClose={() => {}} />);
    const options = screen.getAllByRole("option");
    expect(options.length).toBeGreaterThan(3);
    expect(options.length).toBeLessThan(40);
  });
});

describe("ProjectSelectorDialog", () => {
  it("is a modal dialog that focuses its search and closes on the backdrop", () => {
    const onClose = vi.fn();
    render(<ProjectSelectorDialog open mode="navigate" onClose={onClose} />);
    expect(screen.getByRole("dialog", { name: "Switch project" })).toBeTruthy();
    expect(document.activeElement).toBe(screen.getByTestId("project-selector-search"));
    fireEvent.pointerDown(screen.getByTestId("project-selector-dialog"));
    expect(onClose).toHaveBeenCalled();
  });
});
