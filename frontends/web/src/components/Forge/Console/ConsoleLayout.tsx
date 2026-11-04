// Copyright (c) 2025 Reliant Labs

/**
 * The layout route for /forge/p/$project — every console page.
 *
 * It owns what is a property of the console rather than of one page:
 *
 * 1. WHICH RELIANT PROJECT the URL's forge project is (consoleContext.ts).
 *    The forge routes do not mount ModernApp, the only thing that loads the
 *    project list on a page load, so this loads it, finds the project that
 *    declares the forge project name (the current one first, if several do),
 *    and selects it — so the rest of the app agrees on a refresh. A name no
 *    project here declares is still a scope: the pages read the control plane
 *    by it, with no daemon.
 *
 * 2. THE ENVIRONMENT SCOPE (envScope.ts): resolved from `?env=`,
 *    the project's remembered scope, then prod; reflected back into the URL
 *    on every list page (replace, so it costs no history entry), remembered
 *    when the reader picks one.
 *
 * 3. THE CHROME: ConsoleShell — the nav, the top bar with the breadcrumb and
 *    scope, the rail (its collapsed state remembered per user).
 *
 * Escape and the way out belong to the parent `_forge` layout (ForgeLayout),
 * which also hosts the redirects from the retired URLs.
 */

import { useCallback, useEffect, useMemo, useState } from "react";
import { Outlet, useLocation, useNavigate, useParams, useSearch } from "@tanstack/react-router";

import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import { useForgeRoster } from "@/hooks/forge-queries";
import { useDashboard, useDashboardIndex } from "@/hooks/dashboard-queries";
import { useAlerts, useObservabilityOrg } from "@/hooks/observability-queries";
import { useTitleBarChrome } from "@/hooks/useTitleBarChrome";
import { getParentRouteNavigateOptions } from "@/lib/routeParent";
import { isQueued, queuedOnLabel } from "@/services/forge/live";
import { useAuthStore } from "@/store/authStore";
import { useProjectStore, type Project } from "@/store/projectStore";
import { useProjects } from "@/hooks/project-queries";

import { ForgeCloseButton } from "../ForgeHeader";
import { ObsQuotaBanner } from "../Observability/ObsStates";
import { ConsoleProjectSwitcher, ProjectMonogram } from "./ConsoleProjectSwitcher";
import { OrgSwitcher } from "@/components/Orgs/OrgSwitcher";
import { ConsoleShell, type ConsoleNavEnv } from "./ConsoleShell";
import { ForgeConsoleContext, type ForgeConsole } from "./consoleContext";
import { useOptionalViews } from "./useOptionalViews";
import { ENV_ALL, rememberedEnv, rememberEnv, resolveEnvScope, scopeOptions } from "./envScope";
import { APPS_PATH, consolePageOf, consolePath, pinnedEnvOf, withSearch, type ConsolePage } from "./paths";

const RAIL_KEY = "forge.console.rail";

/**
 * The Reliant project that declares `forgeProject`: the current one when it
 * does (two checkouts of one repo both declare it), else the first that does.
 */
export function projectDeclaring(
  forgeProject: string,
  projects: readonly Project[],
  current: Project | null
): Project | null {
  const declares = (project: Project) => (project.forge_project_name ?? "").trim() === forgeProject;
  if (current && declares(current)) return current;
  return projects.find(declares) ?? null;
}

/** Where switching to the forge app `app` lands: the same page there. */
export function switchTarget(app: string, page: ConsolePage): string {
  // An environment's page is about an environment the other project may not
  // have, and a release's, a workload's (or a dashboard's) about one it cannot
  // have: land on the list.
  const same: ConsolePage =
    page.kind === "environment"
      ? { kind: "environments" }
      : page.kind === "workload"
        ? { kind: "overview" }
        : page.kind === "release"
          ? { kind: "releases" }
          : page.kind === "dashboard"
            ? { kind: "obs-view", view: "dashboards" }
            : page;
  return consolePath(app, same);
}

export function ConsoleLayout() {
  const navigate = useNavigate();
  const { pathname } = useLocation();
  const { project: forgeProject = "" } = useParams({ strict: false }) as { project?: string };
  const search = useSearch({ strict: false }) as { env?: string };
  const page = useMemo<ConsolePage>(() => consolePageOf(pathname)?.page ?? { kind: "overview" }, [pathname]);
  const pinnedEnv = pinnedEnvOf(page);

  // ── 1. The Reliant project. ──
  // `listed` once the project list has answered (or failed): until then a
  // forge name matches nothing only because nothing is loaded yet.
  const { projects, isLoading: listing } = useProjects();
  const listed = projects.length > 0 || !listing;
  const currentProject = useProjectStore((state) => state.currentProject);
  const selectProject = useProjectStore((state) => state.selectProject);

  const match = listed ? projectDeclaring(forgeProject, projects, currentProject) : null;
  useEffect(() => {
    if (match && match.id !== currentProject?.id) void selectProject(match);
  }, [match, currentProject?.id, selectProject]);
  const projectId = match?.id ?? null;

  const roster = useForgeRoster(listed ? projectId : null, forgeProject);

  // ── 2. The environment scope. ──
  const settled = listed && !roster.isLoading && !roster.topology.isLoading;
  const known = useMemo(() => (settled ? roster.envs.map((env) => env.name) : null), [settled, roster.envs]);
  const env = resolveEnvScope(search.env, rememberedEnv(forgeProject), known);

  useEffect(() => {
    if (search.env && search.env === env) rememberEnv(forgeProject, env);
  }, [search.env, env, forgeProject]);

  useEffect(() => {
    if (pinnedEnv || search.env === env) return;
    void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, env }), replace: true });
  }, [pinnedEnv, search.env, env, navigate]);

  const setEnv = useCallback(
    (next: string) => {
      rememberEnv(forgeProject, next);
      void navigate({
        to: ".",
        // A selected secret belongs to the environment it was selected in.
        search: (prev: Record<string, unknown>) => ({ ...prev, env: next, secret: undefined }),
        replace: true,
      });
    },
    [forgeProject, navigate]
  );

  // ── 3. The chrome. ──
  const navEnvs: ConsoleNavEnv[] = useMemo(
    () =>
      roster.envs.map((candidate) => ({
        name: candidate.name,
        preview: candidate.live?.kind === "preview",
        queuedOn: candidate.live && isQueued(candidate.live) ? queuedOnLabel(candidate.live.holds) : undefined,
      })),
    [roster.envs]
  );
  const scope = useMemo(() => scopeOptions(roster.envs), [roster.envs]);

  // Firing alerts in scope, for the badge on Alerts. One org-wide read (shared
  // with the Alerts page's cache), narrowed here to this project's envs.
  const org = useObservabilityOrg();
  const alerts = useAlerts(org.orgId);
  const alertsFiring = useMemo(() => {
    const ids = new Set(
      roster.envs
        .filter((candidate) => candidate.live && (env === ENV_ALL || candidate.name === env))
        .map((candidate) => candidate.live!.id)
    );
    return (alerts.data ?? []).filter((alert) => alert.state === "firing" && ids.has(alert.envId)).length;
  }, [alerts.data, roster.envs, env]);

  // The caller's starred dashboards, nested under Dashboards — the same index
  // (and cache entry) the Dashboards page reads for this scope.
  const scopedEnvId = env === ENV_ALL ? undefined : roster.envs.find((candidate) => candidate.name === env)?.live?.id;
  // An unregistered scoped environment must not accidentally probe the whole project.
  const discoveryScope = useMemo(() => org.orgId && settled && (env === ENV_ALL || scopedEnvId)
    ? { orgId: org.orgId, projectId: forgeProject, envIds: scopedEnvId ? [scopedEnvId] : [], filters: [] }
    : null, [org.orgId, settled, env, forgeProject, scopedEnvId]);
  const optionalViews = useOptionalViews(discoveryScope);
  const dashboards = useDashboardIndex(org.orgId, forgeProject, scopedEnvId);
  const starred = useMemo(
    () =>
      (dashboards.data?.dashboards ?? []).flatMap((d) =>
        d.starred && d.ref.kind !== "template" ? [{ title: d.title, dashKind: d.ref.kind, key: d.ref.key }] : []
      ),
    [dashboards.data]
  );
  const dashboardTitle =
    page.kind === "dashboard"
      ? dashboards.data?.dashboards.find((d) => d.ref.kind === page.dashKind && d.ref.key === page.key)?.title
      : undefined;

  // The open dashboard's own scope (the same cached read the page makes): a
  // platform dashboard is not per environment, and the control must not imply it is.
  const openDashboard = useDashboard(
    org.orgId,
    page.kind === "dashboard" && page.dashKind !== "builtin" ? { kind: page.dashKind, projectId: forgeProject, key: page.key } : null
  );
  const platformWide = openDashboard.data?.spec.scope === "platform";

  const [collapsed, setCollapsed] = useState(() => readRail());
  const toggleCollapsed = useCallback(() => {
    setCollapsed((prev) => {
      writeRail(!prev);
      return !prev;
    });
  }, []);

  const authUser = useAuthStore((state) => state.user);
  const user = authUser
    ? {
        name:
          (authUser.user_metadata?.full_name as string | undefined) ??
          (authUser.user_metadata?.name as string | undefined) ??
          authUser.email?.split("@")[0] ??
          "You",
        email: authUser.email ?? undefined,
      }
    : undefined;

  const { trafficLightPadding, dragRegionStyle } = useTitleBarChrome({ collapsedPadding: "0px" });
  const onClose = useCallback(() => void navigate(getParentRouteNavigateOptions(pathname)), [navigate, pathname]);

  const href = useCallback(
    (target: ConsolePage, extra: Record<string, unknown> = {}) =>
      withSearch(consolePath(forgeProject, target), { env, ...extra }),
    [forgeProject, env]
  );

  const value: ForgeConsole = useMemo(
    () => ({ forgeProject, projectId, page, env, setEnv, roster, href }),
    [forgeProject, projectId, page, env, setEnv, roster, href]
  );

  return (
    <ConsoleShell
      project={forgeProject}
      orgSwitcher={<OrgSwitcher />}
      page={page}
      env={env}
      envs={navEnvs}
      alertsFiring={alertsFiring}
      starred={starred}
      dashboardTitle={dashboardTitle}
      optionalViews={optionalViews}
      scope={scope}
      platformWide={platformWide}
      onEnvChange={setEnv}
      user={user}
      collapsed={collapsed}
      onToggleCollapsed={toggleCollapsed}
      brand={
        <div className="flex min-w-0 items-center gap-1" style={{ paddingLeft: trafficLightPadding, ...dragRegionStyle }}>
          <ForgeCloseButton onClose={onClose} />
          <ConsoleProjectSwitcher
            forgeProject={forgeProject}
            onSelect={(app) => void navigate({ href: switchTarget(app, page) })}
            onSelectAll={() => void navigate({ href: APPS_PATH })}
          />
        </div>
      }
      brandCollapsed={<ProjectMonogram name={forgeProject} />}
    >
      {listed ? (
        <ForgeConsoleContext.Provider value={value}>
          <div className="empty:hidden [&:not(:empty)]:mb-4">
            <ObsQuotaBanner />
          </div>
          <Outlet />
        </ForgeConsoleContext.Provider>
      ) : (
        <div aria-busy="true" aria-label="Loading" data-testid="forge-console-loading" className="space-y-4">
          <SkeletonLoader variant="text" count={1} width="30%" />
          <SkeletonLoader variant="table-row" count={4} />
        </div>
      )}
    </ConsoleShell>
  );
}

function readRail(): boolean {
  try {
    return window.localStorage.getItem(RAIL_KEY) === "1";
  } catch {
    return false;
  }
}

function writeRail(collapsed: boolean): void {
  try {
    window.localStorage.setItem(RAIL_KEY, collapsed ? "1" : "0");
  } catch {
    // Not persisted; the rail still toggles for this visit.
  }
}
