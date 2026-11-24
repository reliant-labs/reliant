// Copyright (c) 2025 Reliant Labs

/**
 * A project's page (design §8.4): the owner's overview (the way to deploy it,
 * apps into their consoles, repos with the apps found in each, checkouts per
 * machine, the shell's chats card and "New chat"), its tabs, the read-only shape a viewer of a shared
 * project gets, and "not in this organization" for a project the org's
 * catalog does not hold.
 */

import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";

import { HostedAppsState, ProjectAppLink, ProjectInstallState, ProjectSchema } from "@/gen/reliant/v1/project_pb";
import { catalogFromResponse, type ProjectCatalog, type WireHostedApp, type WireProjectWithApps } from "@/services/projects/catalog";

const state = vi.hoisted(() => ({
  params: { projectId: "shop" } as { projectId?: string },
  catalog: undefined as ProjectCatalog | undefined,
  navigate: vi.fn(),
  openNewChat: vi.fn(() => Promise.resolve()),
  currentProjectId: "shop" as string | null,
  selectProject: vi.fn(() => Promise.resolve()),
}));

vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  useNavigate: () => state.navigate,
  useParams: () => state.params,
  useRouter: () => ({ latestLocation: { pathname: "/projects/shop/workspaces" } }),
  Link: ({ to, search, children, ...rest }: { to: string; search?: Record<string, string>; children: ReactNode }) => (
    <a href={search ? `${to}?${new URLSearchParams(search)}` : to} {...rest}>
      {children}
    </a>
  ),
}));
vi.mock("@/hooks/project-catalog-queries", () => ({
  useProjectCatalog: () => ({ catalog: state.catalog, isLoading: false, error: null, refetch: vi.fn() }),
}));
vi.mock("@/hooks/useOrgs", () => ({ useActiveOrg: () => ({ active: { name: "Acme" }, canSwitch: false }) }));
vi.mock("@/hooks/sidebar-project-queries", () => ({
  useSidebarProjectPrefs: () => ({ prefs: { pinned: new Set(), hidden: new Set(), showAll: false }, writable: true, setHidden: vi.fn() }),
}));
vi.mock("@/store/openChat", () => ({ openNewChat: state.openNewChat }));
vi.mock("@/store/projectStore", () => ({
  useProjectStore: (select: (s: unknown) => unknown) =>
    select({
      currentProject: state.currentProjectId ? { id: state.currentProjectId } : null,
      selectProject: state.selectProject,
      updateProject: vi.fn(),
      deleteProject: vi.fn(),
    }),
}));
vi.mock("../../Layout/AreaShell", () => ({ AreaShell: ({ children }: { children: ReactNode }) => <>{children}</> }));
vi.mock("../../Settings/BranchesSection", () => ({ WorkspacesBody: () => <div data-testid="workspaces-body" /> }));
vi.mock("../../Settings/projects/ProjectMachines", () => ({ ProjectMachines: () => <div data-testid="project-machines" /> }));
// The Sharing section has its own tests (Sharing/__tests__/ProjectSharingSection): here, that Settings holds it.
vi.mock("../../Sharing/ProjectSharingSection", () => ({
  ProjectSharingSection: ({ project }: { project: { id: string } }) => <div data-testid="project-sharing" data-project={project.id} />,
}));
// The shell's chats card and its deploy empty state have their own tests: here, where they show.
vi.mock("../ProjectChats", () => ({ ProjectChats: ({ projectId }: { projectId: string }) => <div data-testid="project-landing-chats" data-project={projectId} /> }));
vi.mock("../../Forge/Console/AppSectionEmptyState", () => ({
  AppSectionEmptyState: ({ title, projectId }: { title: string; projectId: string }) => <div data-testid="app-section-empty" data-project={projectId}>{title}</div>,
}));

import { ProjectPageBody } from "../ProjectPage";

function hosted(name: string, over: Partial<WireHostedApp> = {}): WireHostedApp {
  return { name, createdAt: "2026-01-01T00:00:00Z", environments: [], hasStaticSite: false, sources: [], warning: "", ...over };
}

function project(id: string, over: Partial<WireProjectWithApps> & { viewerAccess?: boolean } = {}): WireProjectWithApps {
  return {
    project: create(ProjectSchema, {
      id,
      name: id,
      path: `/home/me/${id}`,
      lastActive: "2026-01-01T00:00:00Z",
      createdAt: "2025-01-01T00:00:00Z",
      updatedAt: "2025-01-01T00:00:00Z",
      viewerAccess: over.viewerAccess ?? false,
    }),
    repos: over.repos ?? [],
    apps: over.apps ?? [],
    checkouts: over.checkouts ?? [],
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  state.params = { projectId: "shop" };
  state.currentProjectId = "shop";
  state.catalog = catalogFromResponse({
    orgId: "org-1",
    hostedAppsState: HostedAppsState.OK,
    projects: [
      project("shop", {
        repos: [{ id: "r1", name: "shop", relativePath: "", remote: "github.com/acme/shop" }],
        apps: [
          { name: "shop", repoId: "r1", subpath: "", link: ProjectAppLink.SOURCE, hosted: hosted("shop", { lastDeployedAt: "2026-02-01T00:00:00Z", environments: ["prod", "staging"] }) },
          { name: "docs", repoId: "r1", subpath: "docs", link: ProjectAppLink.NOT_HOSTED },
        ],
        checkouts: [
          { daemonId: "d1", daemonName: "cloud-machine", installState: ProjectInstallState.INSTALLED },
          { daemonId: "d2", daemonName: "laptop", installState: ProjectInstallState.INSTALLING },
        ],
      }),
      project("team-api", { viewerAccess: true, repos: [{ id: "r2", name: "api", relativePath: "", remote: "" }] }),
    ],
    unlinkedApps: [],
  });
});

describe("a project's page", () => {
  it("shows its apps, its code and where it is checked out", () => {
    render(<ProjectPageBody tab="overview" />);
    expect(screen.getByTestId("project-page-name")).toHaveTextContent("shop");
    const apps = screen.getAllByTestId("project-app");
    expect(apps.map((a) => a.getAttribute("data-app"))).toEqual(["shop", "docs"]);
    expect(within(apps[0]).getByRole("link", { name: "Open console" })).toHaveAttribute("href", "/forge/p/shop?project=shop");
    expect(apps[0]).toHaveTextContent("prod, staging");
    expect(within(apps[1]).queryByRole("link", { name: "Open console" })).toBeNull();
    expect(apps[1]).toHaveTextContent("This org has no app of this name yet");
    const repo = screen.getByTestId("project-repo");
    expect(within(repo).getByRole("link", { name: /github\.com\/acme\/shop/ })).toHaveAttribute("href", "https://github.com/acme/shop");
    expect(screen.getAllByTestId("project-checkout").map((c) => c.textContent)).toEqual(["cloud-machineChecked out", "laptopCloning…"]);
  });

  it("offers the way to deploy it, its chats card, and a new chat in this project", () => {
    render(<ProjectPageBody tab="overview" />);
    expect(screen.getByTestId("app-section-empty")).toHaveTextContent("isn't deployed yet");
    expect(screen.getByTestId("app-section-empty")).toHaveAttribute("data-project", "shop");
    expect(screen.getByTestId("project-landing-chats")).toHaveAttribute("data-project", "shop");
    fireEvent.click(screen.getByTestId("project-new-chat"));
    expect(state.openNewChat).toHaveBeenCalledWith({ projectId: "shop" });
  });

  it("has Overview, Workspaces and Settings tabs; Workspaces makes the project current first", async () => {
    state.currentProjectId = "other";
    const { rerender } = render(<ProjectPageBody tab="workspaces" />);
    expect(screen.getByTestId("project-tab-workspaces")).toHaveAttribute("aria-current", "page");
    expect(screen.getByTestId("project-tab-settings")).toHaveAttribute("href", "/projects/shop/settings");
    expect(screen.queryByTestId("workspaces-body")).toBeNull();
    await waitFor(() => expect(state.selectProject).toHaveBeenCalledWith(expect.objectContaining({ id: "shop" })));
    state.currentProjectId = "shop";
    rerender(<ProjectPageBody tab="workspaces" />);
    expect(screen.getByTestId("workspaces-body")).toBeInTheDocument();
    rerender(<ProjectPageBody tab="settings" />);
    expect(screen.getByTestId("project-settings")).toBeInTheDocument();
    expect(screen.getByTestId("project-machines")).toBeInTheDocument();
    expect(screen.getByTestId("project-sharing")).toHaveAttribute("data-project", "shop");
  });

  it("shows a shared project read-only: no checkouts, chats or tabs", () => {
    state.params = { projectId: "team-api" };
    render(<ProjectPageBody tab="settings" />);
    expect(screen.getByTestId("project-shared-badge")).toBeInTheDocument();
    expect(screen.queryByRole("navigation", { name: "Project" })).toBeNull();
    expect(screen.queryByTestId("project-settings")).toBeNull();
    expect(screen.queryByTestId("project-landing-chats")).toBeNull();
    expect(screen.queryByTestId("app-section-empty")).toBeNull();
    expect(screen.queryByTestId("project-checkout")).toBeNull();
    expect(screen.queryByTestId("project-new-chat")).toBeNull();
    expect(screen.getByTestId("project-repo")).toHaveTextContent("Remote not shared");
  });

  it("never shows a project the org's catalog does not hold", () => {
    state.params = { projectId: "elsewhere" };
    render(<ProjectPageBody tab="overview" />);
    expect(screen.getByTestId("project-not-found")).toHaveTextContent("No such project in this organization");
  });
});
