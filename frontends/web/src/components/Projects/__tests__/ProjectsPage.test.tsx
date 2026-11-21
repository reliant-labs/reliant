// Copyright (c) 2025 Reliant Labs

/**
 * The Projects page (design §8.3): three sections, the filter tabs with their
 * counts, search, pin and hide through the `sidebar.projects` setting, "Get
 * the code" for an app-only row, the hosted-apps banners, and the designed
 * loading, error and empty states. In the one shell (U6): the Home of several
 * projects (the summary, with `?browse=1` to this list), Build an app, and the
 * daemonless home's Set up chats card.
 */

import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";

import { HostedAppsState, ProjectAppLink, ProjectInstallState, ProjectSchema } from "@/gen/reliant/v1/project_pb";
import { catalogFromResponse, type ProjectCatalog, type WireHostedApp, type WireProjectWithApps } from "@/services/projects/catalog";

const state = vi.hoisted(() => ({
  catalog: undefined as ProjectCatalog | undefined,
  isLoading: false,
  error: null as Error | null,
  refetch: vi.fn(),
  pinned: new Set<string>(),
  hidden: new Set<string>(),
  setPinned: vi.fn(),
  setHidden: vi.fn(),
  openAddProject: vi.fn(),
  daemons: [] as { daemonType: string }[],
  search: {} as Record<string, unknown>,
  navigate: vi.fn(),
  setup: "ready" as string,
  several: null as "all" | "many" | null,
  startBuilding: vi.fn(),
}));

vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  useNavigate: () => state.navigate,
  useSearch: () => state.search,
  Link: ({ to, children, ...rest }: { to: string; children: ReactNode }) => (
    <a href={to} {...rest}>
      {children}
    </a>
  ),
}));
vi.mock("@/hooks/project-catalog-queries", () => ({
  useProjectCatalog: () => ({ catalog: state.catalog, isLoading: state.isLoading, error: state.error, refetch: state.refetch }),
}));
vi.mock("@/hooks/sidebar-project-queries", () => ({
  useSidebarProjectPrefs: () => ({
    prefs: { pinned: state.pinned, hidden: state.hidden, showAll: false },
    writable: true,
    loadFailed: false,
    retry: vi.fn(),
    setPinned: state.setPinned,
    setHidden: state.setHidden,
    setShowAll: vi.fn(),
  }),
}));
vi.mock("@/hooks/useDaemonStatus", () => ({ useDaemonStatus: () => ({ daemons: state.daemons }) }));
vi.mock("@/store/addProjectDialogStore", () => ({ openAddProject: state.openAddProject }));
vi.mock("../../Layout/AreaShell", () => ({ AreaShell: ({ children }: { children: ReactNode }) => <>{children}</> }));
vi.mock("../../OnboardingFlow/useChatSetup", () => ({ useChatSetup: () => state.setup }));
vi.mock("../../Apps/startBuilding", () => ({ useStartBuildingApp: () => state.startBuilding }));
// The summary of several projects (the project shell's Home) has its own tests.
vi.mock("../../Forge/Console/ProjectsOverview", () => ({ ProjectsOverview: () => null }));
vi.mock("../../Layout/sidebar/shellProject", () => ({ useShellSeveral: () => ({ mode: state.several, rows: [] }) }));

import { ProjectsBody } from "../ProjectsPage";

function hosted(name: string, over: Partial<WireHostedApp> = {}): WireHostedApp {
  return { name, createdAt: "2026-01-01T00:00:00Z", environments: [], hasStaticSite: false, sources: [], warning: "", ...over };
}

function project(id: string, over: Partial<WireProjectWithApps> & { lastActive?: string; viewerAccess?: boolean } = {}): WireProjectWithApps {
  return {
    project: create(ProjectSchema, {
      id,
      name: id,
      path: `/home/me/${id}`,
      lastActive: over.lastActive ?? "2026-01-01T00:00:00Z",
      createdAt: "2025-01-01T00:00:00Z",
      updatedAt: "2025-01-01T00:00:00Z",
      viewerAccess: over.viewerAccess ?? false,
    }),
    repos: over.repos ?? [],
    apps: over.apps ?? [],
    checkouts: over.checkouts ?? [],
  };
}

function catalog(hostedAppsState = HostedAppsState.OK): ProjectCatalog {
  return catalogFromResponse({
    orgId: "org-1",
    hostedAppsState,
    projects: [
      project("shop", {
        lastActive: "2026-03-01T00:00:00Z",
        repos: [{ id: "r1", name: "shop", relativePath: "", remote: "github.com/acme/shop" }],
        apps: [{ name: "shop", repoId: "r1", subpath: "", link: ProjectAppLink.SOURCE, hosted: hosted("shop", { lastDeployedAt: "2026-02-01T00:00:00Z", hasStaticSite: true, environments: ["prod"] }) }],
        checkouts: [{ daemonId: "d1", daemonName: "cloud-machine", installState: ProjectInstallState.INSTALLED }],
      }),
      project("notes", { lastActive: "2026-02-15T00:00:00Z" }),
      project("team-api", { viewerAccess: true }),
    ],
    unlinkedApps: [hosted("ledger", { environments: ["prod", "staging"], sources: [{ remote: "github.com/acme/ledger", subpath: "", commit: "abc", lastSeenAt: "" }] })],
  });
}

const sectionKeys = (id: string) =>
  within(screen.getByTestId(`projects-section-${id}`))
    .getAllByTestId("projects-row")
    .map((row) => row.getAttribute("data-row-key"));

beforeEach(() => {
  state.setup = "ready";
  state.several = null;
  state.startBuilding.mockReset();
  state.catalog = catalog();
  state.isLoading = false;
  state.error = null;
  state.pinned = new Set();
  state.hidden = new Set();
  state.daemons = [];
  state.search = {};
  vi.clearAllMocks();
});

describe("the Projects page", () => {
  it("lists your projects, shared ones and apps without code here, each row opening its page", () => {
    render(<ProjectsBody />);
    expect(sectionKeys("yours")).toEqual(["shop", "notes"]);
    expect(sectionKeys("shared")).toEqual(["team-api"]);
    expect(sectionKeys("apps")).toEqual(["app:ledger"]);
    expect(screen.getByRole("link", { name: "Open shop" })).toHaveAttribute("href", "/projects/shop");
    expect(screen.getByRole("link", { name: "Open the ledger app" })).toHaveAttribute("href", "/forge/p/ledger");
    expect(screen.getByTestId("projects-counts")).toHaveTextContent("3 projects · 2 apps");
    // The app-only row says where it runs and where its code is.
    const ledger = screen.getByRole("link", { name: "Open the ledger app" });
    expect(ledger).toHaveTextContent("github.com/acme/ledger");
    expect(ledger).toHaveTextContent("prod, staging");
  });

  it("filters by tab, with each tab's count", () => {
    render(<ProjectsBody />);
    const counts = Object.fromEntries(
      ["all", "with_apps", "sites", "code_only"].map((id) => [id, screen.getByTestId(`projects-filter-${id}`).textContent]),
    );
    expect(counts).toEqual({ all: "All4", with_apps: "With apps2", sites: "Sites1", code_only: "Code only2" });
    fireEvent.click(screen.getByTestId("projects-filter-sites"));
    expect(screen.getAllByTestId("projects-row").map((r) => r.getAttribute("data-row-key"))).toEqual(["shop"]);
    fireEvent.click(screen.getByTestId("projects-filter-code_only"));
    expect(screen.getAllByTestId("projects-row").map((r) => r.getAttribute("data-row-key"))).toEqual(["notes", "team-api"]);
  });

  it("keeps the filter in the URL, and opens on U6's ?show=apps|sites links", () => {
    state.search = { show: "sites" };
    render(<ProjectsBody />);
    expect(screen.getByTestId("projects-filter-sites")).toHaveAttribute("aria-pressed", "true");
    expect(screen.getAllByTestId("projects-row").map((r) => r.getAttribute("data-row-key"))).toEqual(["shop"]);
    fireEvent.click(screen.getByTestId("projects-filter-code_only"));
    const { search } = state.navigate.mock.calls.at(-1)![0] as { search: (prev: Record<string, unknown>) => Record<string, unknown> };
    expect(search({ show: "sites" })).toEqual({ show: "code" });
  });

  it("searches, and says so when nothing matches", () => {
    render(<ProjectsBody />);
    fireEvent.change(screen.getByTestId("projects-search"), { target: { value: "ledg" } });
    expect(screen.getAllByTestId("projects-row").map((r) => r.getAttribute("data-row-key"))).toEqual(["app:ledger"]);
    fireEvent.change(screen.getByTestId("projects-search"), { target: { value: "zzz" } });
    expect(screen.getByTestId("projects-no-match")).toHaveTextContent("Nothing matches “zzz”");
  });

  it("puts a pinned row first in its section and keeps hidden ones behind Show hidden", () => {
    state.pinned = new Set(["notes"]);
    state.hidden = new Set(["team-api"]);
    render(<ProjectsBody />);
    expect(sectionKeys("yours")).toEqual(["notes", "shop"]);
    expect(screen.queryByTestId("projects-section-shared")).toBeNull();
    fireEvent.click(screen.getByTestId("projects-show-hidden"));
    expect(sectionKeys("shared")).toEqual(["team-api"]);
  });

  it("pins and hides through the row menu", () => {
    render(<ProjectsBody />);
    const row = screen.getAllByTestId("projects-row").find((r) => r.getAttribute("data-row-key") === "app:ledger")!;
    fireEvent.click(within(row).getByTestId("projects-row-menu"));
    fireEvent.click(screen.getByTestId("projects-row-pin"));
    expect(state.setPinned).toHaveBeenCalledWith("app:ledger", true);
    fireEvent.click(within(row).getByTestId("projects-row-menu"));
    fireEvent.click(screen.getByTestId("projects-row-hide"));
    expect(state.setHidden).toHaveBeenCalledWith("app:ledger", true);
  });

  it("gets an app's code: the clone flow from its source when a cloud machine can clone, else the add-project choices", () => {
    state.daemons = [{ daemonType: "cloud" }];
    const { unmount } = render(<ProjectsBody />);
    fireEvent.click(screen.getByTestId("projects-get-code"));
    expect(state.openAddProject).toHaveBeenLastCalledWith("clone", { repoUrl: "https://github.com/acme/ledger" });
    unmount();
    state.daemons = [];
    render(<ProjectsBody />);
    fireEvent.click(screen.getByTestId("projects-get-code"));
    expect(state.openAddProject).toHaveBeenLastCalledWith(undefined, { repoUrl: "https://github.com/acme/ledger" });
  });

  it("says when the org's apps cannot be read", () => {
    state.catalog = catalog(HostedAppsState.NO_ACCESS);
    const { unmount } = render(<ProjectsBody />);
    expect(screen.getByTestId("projects-apps-no-access")).toBeInTheDocument();
    expect(screen.queryByTestId("projects-section-apps")).toBeNull();
    unmount();
    state.catalog = catalog(HostedAppsState.UNAVAILABLE);
    render(<ProjectsBody />);
    fireEvent.click(within(screen.getByTestId("projects-apps-unavailable")).getByRole("button", { name: "Try again" }));
    expect(state.refetch).toHaveBeenCalled();
  });

  it("has designed loading, error and empty states", () => {
    state.catalog = undefined;
    state.isLoading = true;
    const { rerender } = render(<ProjectsBody />);
    expect(screen.getByTestId("projects-loading")).toBeInTheDocument();
    state.isLoading = false;
    state.error = new Error("boom");
    rerender(<ProjectsBody />);
    expect(screen.getByTestId("projects-error")).toHaveTextContent("boom");
    state.error = null;
    state.catalog = catalogFromResponse({ orgId: "org-1", hostedAppsState: HostedAppsState.OK, projects: [], unlinkedApps: [] });
    rerender(<ProjectsBody />);
    fireEvent.click(within(screen.getByTestId("projects-empty")).getByRole("button", { name: "Clone a repository" }));
    expect(state.openAddProject).toHaveBeenCalledWith("clone");
  });

  it("links the org's Observability and adds a project from the header", () => {
    render(<ProjectsBody />);
    expect(screen.getByRole("link", { name: "Observability" })).toHaveAttribute("href", "/forge/observability");
    fireEvent.click(screen.getByRole("button", { name: "Add project" }));
    expect(state.openAddProject).toHaveBeenCalledWith();
  });
});

describe("ProjectsBody with several projects in the shell", () => {
  it("is their Home: the summary alone, with a quiet way to browse every project", () => {
    state.several = "all";
    render(<ProjectsBody />);
    expect(screen.getByTestId("projects-page")).toHaveAttribute("data-mode", "home");
    expect(screen.queryByRole("heading", { name: "Projects" })).toBeNull();
    expect(screen.queryByTestId("projects-row")).toBeNull();
    fireEvent.click(screen.getByTestId("projects-browse-all"));
    expect(state.navigate).toHaveBeenCalledWith({ href: "/projects?browse=1" });
  });

  it.each(["all", "many"] as const)("shows the list a filter names (the retired /apps and /sites), with %s projects", (several) => {
    state.several = several;
    for (const show of ["apps", "sites", "code"]) {
      state.search = { show };
      const view = render(<ProjectsBody />);
      expect(screen.getByTestId("projects-page")).not.toHaveAttribute("data-mode", "home");
      view.unmount();
    }
  });

  it("lists every project when asked to browse", () => {
    state.several = "all";
    state.search = { browse: 1 };
    render(<ProjectsBody />);
    expect(screen.getAllByTestId("projects-row").length).toBeGreaterThan(0);
  });
});

describe("ProjectsBody's Build an app", () => {
  it("starts building an app", () => {
    render(<ProjectsBody />);
    fireEvent.click(screen.getByRole("button", { name: "Build an app" }));
    expect(state.startBuilding).toHaveBeenCalledTimes(1);
  });
});

/**
 * The daemonless home (U6): a person who cannot run a chat yet lands here,
 * and the way to set chats up leads the page.
 */
describe("ProjectsBody's Set up chats card", () => {
  it.each([
    ["onboard", "/onboarding"],
    ["connect-machine", "/settings/machines"],
  ])("leads the page for %s, linking to %s", (setup, href) => {
    state.setup = setup;
    render(<ProjectsBody />);
    const card = screen.getByTestId("set-up-chats-card");
    expect(card).toHaveTextContent("Chats run your agents in an agent sandbox");
    const link = within(card).getByRole("link", { name: "Set up" });
    expect(link).toHaveAttribute("href", href);
    fireEvent.click(link);
    expect(state.navigate).toHaveBeenCalledWith({ href });
  });

  it.each(["ready", "checking", "unknown"])("is not there for %s", (setup) => {
    state.setup = setup;
    render(<ProjectsBody />);
    expect(screen.queryByTestId("set-up-chats-card")).toBeNull();
  });
});
