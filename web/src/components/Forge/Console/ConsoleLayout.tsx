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
 * 2. THE ENVIRONMENT SCOPE (envScope.ts, decision D1): resolved from `?env=`,
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
import { useAlerts, useObservabilityOrg } from "@/hooks/observability-queries";
import { useTitleBarChrome } from "@/hooks/useTitleBarChrome";
import { getParentRouteNavigateOptions } from "@/lib/routeParent";
import { isQueued, queuedOnLabel } from "@/services/forge/live";
import { useAuthStore } from "@/store/authStore";
import { useProjectStore, type Project } from "@/store/projectStore";

import { ForgeCloseButton } from "../ForgeHeader";
import { ConsoleProjectSwitcher, ProjectMonogram } from "./ConsoleProjectSwitcher";
import { ConsoleShell, type ConsoleNavEnv } from "./ConsoleShell";
import { ForgeConsoleContext, type ForgeConsole } from "./consoleContext";
import { ENV_ALL, rememberedEnv, rememberEnv, resolveEnvScope, scopeOptions } from "./envScope";
import { consolePageOf, consolePath, pinnedEnvOf, withSearch, type ConsolePage } from "./paths";

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

/** Where switching to `project` lands: the same page there, when it has a forge name to address it by. */
export function switchTarget(project: Project, page: ConsolePage): string {
  const name = (project.forge_project_name ?? "").trim();
  if (!name) return withSearch("/forge", { project: project.id });
  // An environment's page is about an environment the other project may not have.
  const same = page.kind === "environment" ? { kind: "environments" as const } : page;
  return consolePath(name, same);
}

export function ConsoleLayout() {
  const navigate = useNavigate();
  const { pathname } = useLocation();
  const { project: forgeProject = "" } = useParams({ strict: false }) as { project?: string };
  const search = useSearch({ strict: false }) as { env?: string };
  const page = useMemo<ConsolePage>(() => consolePageOf(pathname)?.page ?? { kind: "overview" }, [pathname]);
  const pinnedEnv = pinnedEnvOf(page);

  // ── 1. The Reliant project. ──
  const projects = useProjectStore((state) => state.projects);
  const currentProject = useProjectStore((state) => state.currentProject);
  const loadProjects = useProjectStore((state) => state.loadProjects);
  const selectProject = useProjectStore((state) => state.selectProject);
  const [listed, setListed] = useState(projects.length > 0);
  useEffect(() => {
    if (listed) return;
    let live = true;
    void loadProjects().finally(() => {
      if (live) setListed(true);
    });
    return () => {
      live = false;
    };
  }, [listed, loadProjects]);

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
      page={page}
      env={env}
      envs={navEnvs}
      alertsFiring={alertsFiring}
      scope={scope}
      onEnvChange={setEnv}
      user={user}
      collapsed={collapsed}
      onToggleCollapsed={toggleCollapsed}
      brand={
        <div className="flex min-w-0 items-center gap-1" style={{ paddingLeft: trafficLightPadding, ...dragRegionStyle }}>
          <ForgeCloseButton onClose={onClose} />
          <ConsoleProjectSwitcher
            forgeProject={forgeProject}
            onSelect={(project) => void navigate({ href: switchTarget(project, page) })}
          />
        </div>
      }
      brandCollapsed={<ProjectMonogram name={forgeProject} />}
    >
      {listed ? (
        <ForgeConsoleContext.Provider value={value}>
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
