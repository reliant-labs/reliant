// Copyright (c) 2025 Reliant Labs

/**
 * The layout route for /forge/p/$project — every console page.
 *
 * It owns what is a property of the console rather than of one page:
 *
 * 1. WHICH RELIANT PROJECT the URL's forge project is (consoleContext.ts).
 *    The forge routes do not mount ModernApp, the only thing that loads the
 *    project list on a page load, so this loads it, finds the project that
 *    declares the forge project name (the one `?project=` names; else the
 *    only one; several and none named: ConsoleCheckoutPicker asks), and
 *    selects it — so the rest of the app agrees on a refresh. A name no
 *    project here declares is still a scope: the pages read the control plane
 *    by it, with no daemon.
 *
 * 2. THE ENVIRONMENT SCOPE (envScope.ts): resolved from `?env=`,
 *    the project's remembered scope, then prod; reflected back into the URL
 *    on every list page (replace, so it costs no history entry), remembered
 *    when the reader picks one.
 *
 * 3. THE CHROME. The console is a page inside the one shell
 *    (Layout/shell/AppShell.tsx): the shell's left nav draws this app's
 *    sections inline (the same model, useConsoleNavModel.ts), and the page
 *    sits under a slim bar with the breadcrumb and scope (ConsoleFrame). The
 *    app, the public demo (demoShellRoutes.tsx: the app's frame with the
 *    demo's nav, and a fixed session that turns it on) and the dev previews
 *    (Forge/__preview__/PreviewShellFrame.tsx) all draw it there, in the led
 *    console (ledConsole.tsx): pages that lead with one sentence and the item
 *    worth opening first.
 *
 * Escape and the way out belong to the parent `_forge` layout (ForgeLayout),
 * which also hosts the redirects from the retired URLs.
 */

import { useCallback, useEffect, useMemo } from "react";
import { Outlet, useLocation, useNavigate, useParams, useSearch } from "@tanstack/react-router";

import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import { DOCS } from "../GettingStarted";
import { useShellFrame } from "@/components/Layout/shell/shellFrame";

import { ObsQuotaBanner } from "../Observability/ObsStates";
import { PrimeMetricCatalog } from "../Observability/Metrics/PrimeMetricCatalog";
import { useShellLastPick } from "@/components/Layout/sidebar/shellProject";

import { AppSectionEmptyState } from "./AppSectionEmptyState";
import { ConsoleCheckoutPicker } from "./ConsoleCheckoutPicker";
import { ConsoleFrame } from "./ConsoleFrame";
import { LedConsoleProvider, type LedConsoleHost } from "./ledConsole";
import { ForgeConsoleContext, rosterReadFailed, type ForgeConsole } from "./consoleContext";
import { useConsoleNavModel } from "./useConsoleNavModel";
import { rememberEnv } from "./envScope";
import { consolePath, isTestsPage, pinnedEnvOf, withSearch, type ConsolePage } from "./paths";
import { severalModeOf, useSeveralApps } from "./severalProjects";
import { SeveralProjectSections } from "./severalProjectSections";
import { AppScopeProvider } from "@/fixtures/scope";

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
        : page.kind === "service"
          ? { kind: "services" }
        : page.kind === "release"
          ? { kind: "releases" }
          : page.kind === "dashboard"
            ? { kind: "obs-view", view: "dashboards" }
            : page;
  return consolePath(app, same);
}

/**
 * The console in its app's scope (fixtures/scope.tsx): the forge tour's
 * sample app reads its fixture pack through a TransportScope of its own, with
 * read-only controls; every real app is unwrapped. Everything below, the
 * console's own model included, reads inside it.
 */
export function ConsoleLayout() {
  const { project: forgeProject = "" } = useParams({ strict: false }) as { project?: string };
  return (
    <AppScopeProvider app={forgeProject}>
      <ConsoleLayoutInScope />
    </AppScopeProvider>
  );
}

function ConsoleLayoutInScope() {
  const navigate = useNavigate();
  const { pathname } = useLocation();
  const { project: forgeProject = "" } = useParams({ strict: false }) as { project?: string };
  const search = useSearch({ strict: false }) as { env?: string; project?: string };
  const { inShell } = useShellFrame();

  // ── 1–2. The Reliant project, the roster and the environment scope: the
  // same model the shell's nav draws this app's sections from. ──
  const model = useConsoleNavModel(forgeProject, pathname, search);
  const { orgId, page, env, listed, projectId, candidates, roster, dashboardTitle, scope, platformWide, productAnalytics } = model;
  const pinnedEnv = pinnedEnvOf(page);

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

  // Nothing deployed yet (the roster answered with no environment): inside
  // the shell every page of the app says what it will show and how to deploy
  // it (U6b; every project has the same nav). The shell's project names the
  // code to deploy when no project declares the app yet.
  const shellFocus = useShellLastPick((state) => state.focus);
  const undeployed = inShell && model.settled && roster.envs.length === 0 && !rosterReadFailed(roster);
  const deployProjectId = projectId ?? (shellFocus && !shellFocus.startsWith("app:") ? shellFocus : null);
  // Several projects in the shell (severalProjects.tsx): a list page shows every one of them, so
  // the focused app's own state (nothing deployed, two checkouts) never stands in for the page.
  const several = useSeveralApps();
  const severalMode = several ? severalModeOf(page) : "one";
  const across = !!several && severalMode !== "one";
  const body = useMemo(
    () =>
      candidates && !across ? (
        <ConsoleCheckoutPicker app={forgeProject} candidates={candidates} />
      ) : listed ? (
        <ForgeConsoleContext.Provider value={value}>
          <div className="empty:hidden [&:not(:empty)]:mb-4">
            <ObsQuotaBanner />
          </div>
          {/* The Overview is the landing: with nothing deployed it shows the chats and the way to deploy (WorkloadPages OverviewPage). */}
          {/* Tests need no deploy: a CI lane records runs before any release (TestsService RecordTestRun), so they keep their pages. */}
          {undeployed && !across && page.kind !== "overview" && !isTestsPage(page) ? (
            <AppSectionEmptyState page={page} projectId={deployProjectId} />
          ) : several && severalMode === "sections" ? (
            <SeveralProjectSections apps={several.apps}>
              <Outlet />
            </SeveralProjectSections>
          ) : (
            <Outlet />
          )}
        </ForgeConsoleContext.Provider>
      ) : (
        <div aria-busy="true" aria-label="Loading" data-testid="forge-console-loading" className="space-y-4">
          <SkeletonLoader variant="text" count={1} width="30%" />
          <SkeletonLoader variant="table-row" count={4} />
        </div>
      ),
    [candidates, forgeProject, listed, value, undeployed, page, deployProjectId, across, several, severalMode]
  );

  const primeMetrics = page.kind === "obs-view" && page.view === "metrics" && (
    <PrimeMetricCatalog orgId={orgId} forgeProject={forgeProject} env={env} roster={roster} />
  );

  // The app's sections are rows of the shell's nav: the console is a page
  // under a slim bar, with no column of its own (ConsoleFrame).
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
        {primeMetrics}
        {body}
      </ConsoleFrame>
    </LedConsoleProvider>
  );
}
