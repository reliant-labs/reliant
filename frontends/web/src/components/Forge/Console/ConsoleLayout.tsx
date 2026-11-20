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
 * 3. THE CHROME. Inside the one shell (Layout/shell/AppShell.tsx), the
 *    shell's left nav draws this app's sections inline (the same model,
 *    useConsoleNavModel.ts) and the console is a page under a slim bar with
 *    the breadcrumb and scope (ConsoleFrame). Without the shell (the forge
 *    host until it runs the one router), ConsoleShell — the nav, the top bar,
 *    the rail (its collapsed state remembered per user). Either way in the
 *    led console (ledConsole.tsx): pages that lead with one sentence and the
 *    item worth opening first.
 *
 * Escape and the way out belong to the parent `_forge` layout (ForgeLayout),
 * which also hosts the redirects from the retired URLs.
 */

import { useCallback, useEffect, useMemo, useState } from "react";
import { Outlet, useLocation, useNavigate, useParams, useSearch } from "@tanstack/react-router";

import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import { DOCS } from "../GettingStarted";
import { useShellFrame } from "@/components/Layout/shell/shellFrame";
import { useTitleBarChrome } from "@/hooks/useTitleBarChrome";
import { getParentRouteNavigateOptions } from "@/lib/routeParent";
import { isForgeShell } from "@/lib/shell";
import { ForgeMark } from "@/components/ForgeShell/ForgeBrand";

import { ForgeCloseButton } from "../ForgeHeader";
import { ObsQuotaBanner } from "../Observability/ObsStates";
import { ConsoleProjectSwitcher, ProjectMonogram } from "./ConsoleProjectSwitcher";
import { OrgSwitcher } from "@/components/Orgs/OrgSwitcher";
import { useShellLastPick } from "@/components/Layout/sidebar/shellProject";

import { AppSectionEmptyState } from "./AppSectionEmptyState";
import { ConsoleFrame } from "./ConsoleFrame";
import { ConsoleShell } from "./ConsoleShell";
import { LedConsoleProvider, type LedConsoleHost } from "./ledConsole";
import { ForgeConsoleContext, rosterReadFailed, type ForgeConsole } from "./consoleContext";
import { useConsoleUser } from "./consoleHost";
import { useConsoleNavModel } from "./useConsoleNavModel";
import { useObsNavOpen } from "./obsNavOpen";
import { rememberEnv } from "./envScope";
import { consolePath, pinnedEnvOf, withSearch, type ConsolePage } from "./paths";
import { projectsHref } from "@/lib/paths";

const RAIL_KEY = "forge.console.rail";

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
  const { inShell } = useShellFrame();

  // ── 1–2. The Reliant project, the roster and the environment scope: the
  // same model the shell's nav draws this app's sections from. ──
  const model = useConsoleNavModel(forgeProject, pathname, search);
  const { page, env, listed, projectId, roster, envs: navEnvs, alertsFiring, starred, dashboardTitle, optionalViews, scope, platformWide, productAnalytics } = model;
  const pinnedEnv = pinnedEnvOf(page);
  const forgeShell = isForgeShell();

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
  const [collapsed, setCollapsed] = useState(() => readRail());
  const toggleCollapsed = useCallback(() => {
    setCollapsed((prev) => {
      writeRail(!prev);
      return !prev;
    });
  }, []);

  const [obsOpen, setObsOpen] = useObsNavOpen();

  const user = useConsoleUser();

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

  // The led console is the console's default (ledConsole.tsx): Product analytics leads to the project's hub, when it has one.
  const ledHost = useMemo<LedConsoleHost>(
    () => ({ productAnalytics, releasesExplainer: { href: DOCS.deploys, label: "How releases work" } }),
    [productAnalytics]
  );

  // ConsoleShell is memoized: every element it takes is built once per input,
  // so a URL change that moves none of them (a tour step, a filter) leaves the
  // chrome alone, and the context value above leaves the page alone.
  const orgSwitcher = useMemo(() => <OrgSwitcher />, []);
  const brand = useMemo(
    () => (
      <div className="flex min-w-0 items-center gap-1" style={{ paddingLeft: trafficLightPadding, ...dragRegionStyle }}>
        {/* The forge shell has no app to close back to: its mark leads
            home (Projects' apps), and Reliant is a link in its own chrome. */}
        {forgeShell ? (
          <a href={projectsHref("apps")} aria-label="Forge by Reliant: all apps" className="rounded-md p-0.5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring" data-testid="forge-shell-home">
            <ForgeMark className="h-6 w-6" />
          </a>
        ) : (
          <ForgeCloseButton onClose={onClose} />
        )}
        <ConsoleProjectSwitcher
          forgeProject={forgeProject}
          onSelect={(app) => void navigate({ href: switchTarget(app, page) })}
          onSelectAll={() => void navigate({ href: projectsHref("apps") })}
        />
      </div>
    ),
    [trafficLightPadding, dragRegionStyle, forgeShell, onClose, forgeProject, navigate, page]
  );
  const brandCollapsed = useMemo(() => <ProjectMonogram name={forgeProject} />, [forgeProject]);
  // Nothing deployed yet (the roster answered with no environment): inside
  // the shell every page of the app says what it will show and how to deploy
  // it (U6b; every project has the same nav). The shell's project names the
  // code to deploy when no project declares the app yet.
  const shellFocus = useShellLastPick((state) => state.focus);
  const undeployed = inShell && model.settled && roster.envs.length === 0 && !rosterReadFailed(roster);
  const deployProjectId = projectId ?? (shellFocus && !shellFocus.startsWith("app:") ? shellFocus : null);
  const body = useMemo(
    () =>
      listed ? (
        <ForgeConsoleContext.Provider value={value}>
          <div className="empty:hidden [&:not(:empty)]:mb-4">
            <ObsQuotaBanner />
          </div>
          {undeployed ? <AppSectionEmptyState page={page} projectId={deployProjectId} /> : <Outlet />}
        </ForgeConsoleContext.Provider>
      ) : (
        <div aria-busy="true" aria-label="Loading" data-testid="forge-console-loading" className="space-y-4">
          <SkeletonLoader variant="text" count={1} width="30%" />
          <SkeletonLoader variant="table-row" count={4} />
        </div>
      ),
    [listed, value, undeployed, page, deployProjectId]
  );

  // Inside the one shell the app's sections are rows of its nav: the console
  // is a page under a slim bar, with no column of its own (ConsoleFrame).
  if (inShell) {
    return (
      <LedConsoleProvider host={ledHost}>
        <ConsoleFrame
          project={forgeProject}
          page={page}
          env={env}
          dashboardTitle={dashboardTitle}
          scope={scope}
          platformWide={platformWide}
          onEnvChange={setEnv}
        >
          {body}
        </ConsoleFrame>
      </LedConsoleProvider>
    );
  }

  return (
    <LedConsoleProvider host={ledHost}>
    <ConsoleShell
      project={forgeProject}
      orgSwitcher={orgSwitcher}
      page={page}
      env={env}
      envs={navEnvs}
      alertsFiring={alertsFiring}
      starred={starred}
      dashboardTitle={dashboardTitle}
      optionalViews={optionalViews}
      obsOpen={obsOpen}
      onObsOpenChange={setObsOpen}
      scope={scope}
      platformWide={platformWide}
      onEnvChange={setEnv}
      user={user}
      collapsed={collapsed}
      onToggleCollapsed={toggleCollapsed}
      brand={brand}
      brandCollapsed={brandCollapsed}
    >
      {body}
    </ConsoleShell>
    </LedConsoleProvider>
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
