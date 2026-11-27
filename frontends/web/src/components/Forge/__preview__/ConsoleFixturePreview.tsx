// Copyright (c) 2025 Reliant Labs

/**
 * DEV-ONLY: the real forge console — the route tree the product mounts
 * (forgeConsoleRoutes), its layout and pages, in the app's shell with the
 * console's nav (PreviewShellFrame) — at one console URL, over
 * the shop fixture pack's app layer (src/fixtures/shop/app.ts) through
 * <FixturePreview>, signed in, with a named overlay for the state the story
 * does not have. Every hook, client and component runs as in the product;
 * only the Connect transport under them answers from the pack.
 *
 * The console's own navigation stays inside it (a memory history), so the
 * harness's URL names where it OPENS: the preview pages that mount this
 * (ForgePanePreview, ReleasesPreview, WorkloadsPreview) map their query
 * strings to a console path, and their pickers (case, scheme, dark) to
 * PreviewControls.
 */

import { useEffect, useMemo, type ReactNode } from "react";
import { createMemoryHistory, createRootRoute, createRoute, createRouter, Outlet, RouterProvider, useNavigate, useRouterState } from "@tanstack/react-router";

import { createForgeConsoleRoutes } from "@/forgeConsoleRoutes";
import { latency, logged } from "@/fixtures/overlay";
import { FixturePreview, type FixtureServeOptions } from "@/fixtures/preview";
import { SHOP_SERVE } from "@/fixtures/shop";
import { shopAppPack } from "@/fixtures/shop/app";
import { shopProject } from "@/fixtures/shop/workspace";
import type { FixturePack } from "@/fixtures/transport";
import { projectFromProto } from "@/hooks/project-queries";
import { useProjectStore } from "@/store/projectStore";

import { ConsoleLayout } from "../Console/ConsoleLayout";
import { DashboardPage, DashboardsPage } from "../Observability/Dashboards/DashboardsPages";
import { ActivityPage } from "../Console/ActivityPage";
import { ServicePage, ServicesPage } from "../Services/ServicesPages";
import { ProjectUsagePage } from "../Usage/ProjectUsagePage";
import { AnalyticsPage } from "../Analytics/AnalyticsPage";
import { EventsMetricsPage } from "../Analytics/EventsMetricsPage";
import { SiteInsightsPage } from "../Analytics/SiteInsightsPage";
import { ObservabilityViewPage } from "../Console/ConsolePages";
import { SecretsPage } from "../Console/SecretsPage";
import { DataPage } from "../Data/DataPage";
import { ObsOverviewPage } from "../Observability/ObsOverview/ObsOverviewPage";
import { AuthPage } from "../Users/AuthPage";
import { ExperimentPage, ExperimentsPage } from "../Experiments/ExperimentsPage";
import { ReleasePage, ReleasesPage } from "../Releases/ReleasesPages";
import { StoragePage } from "../Storage/StoragePage";
import { SbomsPage } from "../Security/SbomsPage";
import { VulnerabilitiesPage } from "../Security/VulnerabilitiesPage";
import { VulnerabilityPage } from "../Security/VulnerabilityPage";
import { TestsOverviewPage } from "../Tests/TestsOverviewPage";
import { TestsFlakyPage } from "../Tests/TestsFlakyPage";
import { TestsFlakyTestPage } from "../Tests/TestsFlakyTestPage";
import { TestsCoveragePage } from "../Tests/TestsPlaceholderPages";
import { TestsRunPage } from "../Tests/TestsRunPage";
import { TestsRunsPage } from "../Tests/TestsRunsPage";
import { OverviewPage, WorkloadPage } from "../Workloads/WorkloadPages";
import { ForgeLandingRoute, LegacyCommandRoute, LegacyDomainsRoute, LegacyEnvRoute, OrgObservabilityRoute } from "../Console/ForgeEntryRoutes";
import { OrgConsoleLayout } from "../Org";
import { ForgeDomainsPage } from "../Domains/ForgeDomainsPage";
import { ForgeEnvPage } from "../EnvPage/ForgeEnvPage";
import { ForgeLayout } from "../ForgeLayout";
import { EnvironmentsPage } from "../Overview/EnvironmentsPage";
import { PreviewShellFrame } from "./PreviewShellFrame";

/** The app layer: the console's reads plus the reliant app's (projects, the signed-in user, forge's reports). */
const PACK = shopAppPack();

/**
 * How the console harness serves it: every call logged (so a screenshot can
 * be checked against what was asked), and a beat of latency on every answer,
 * so loading states are visible.
 */
export const CONSOLE_SERVE: FixtureServeOptions = { ...SHOP_SERVE, interceptors: [logged("console-preview"), latency(250)] };

function buildRouter(path: string) {
  const root = createRootRoute({ component: () => <Outlet /> });
  // The app's own layout id, so the shell's frame is on here by the app's rule (Layout/shell/shellFrame.tsx).
  const authenticated = createRoute({
    getParentRoute: () => root,
    id: "_authenticated",
    component: () => (
      <PreviewShellFrame>
        <Outlet />
      </PreviewShellFrame>
    ),
  });
  const forge = createForgeConsoleRoutes(() => authenticated, {
    layout: ForgeLayout,
    console: ConsoleLayout,
    overview: OverviewPage,
    workload: WorkloadPage,
    releases: ReleasesPage,
    release: ReleasePage,
    observabilityView: ObservabilityViewPage,
    obsOverview: ObsOverviewPage,
    dashboards: DashboardsPage,
    dashboard: DashboardPage,
    storage: StoragePage,
    data: DataPage,
    auth: AuthPage,
    vulnerabilities: VulnerabilitiesPage,
    vulnerability: VulnerabilityPage,
    sboms: SbomsPage,
    tests: TestsOverviewPage,
    testsCoverage: TestsCoveragePage,
    testsRuns: TestsRunsPage,
    testsRun: TestsRunPage,
    testsFlaky: TestsFlakyPage,
    testsFlakyTest: TestsFlakyTestPage,
    environments: EnvironmentsPage,
    environment: ForgeEnvPage,
    secrets: SecretsPage,
    domains: ForgeDomainsPage,
    activity: ActivityPage,
    services: ServicesPage,
    service: ServicePage,
    usage: ProjectUsagePage,
    events: EventsMetricsPage,
    analytics: AnalyticsPage,
    siteInsights: SiteInsightsPage,
    experiments: ExperimentsPage,
    experiment: ExperimentPage,
    orgLayout: OrgConsoleLayout,
    orgObservability: OrgObservabilityRoute,
    landing: ForgeLandingRoute,
    legacyEnv: LegacyEnvRoute,
    legacyDomains: LegacyDomainsRoute,
    legacyCommand: LegacyCommandRoute,
  });
  const home = createRoute({ getParentRoute: () => root, path: "/", component: () => <p className="p-6">Closed.</p> });
  return createRouter({
    routeTree: root.addChildren([authenticated.addChildren([forge]), home]),
    history: createMemoryHistory({ initialEntries: [path] }),
  });
}

export interface ConsoleFixturePreviewProps {
  /** The console URL it opens at (`/forge/p/shop/releases?env=prod`). */
  path: string;
  /** The named state over the pack (src/fixtures/shop/overlays.ts). */
  overlay?: FixturePack | readonly FixturePack[] | null;
  /** How the pack is served; CONSOLE_SERVE by default. */
  serve?: FixtureServeOptions;
  /** The harness's pickers (PreviewControls), over the console. */
  controls?: ReactNode;
  /** Context the console reads beside the transport (RepoMetaProvider: GitHub, not Connect). */
  wrap?: (console: ReactNode) => ReactNode;
}

export function ConsoleFixturePreview({ path, overlay, serve = CONSOLE_SERVE, controls, wrap = (console) => console }: ConsoleFixturePreviewProps) {
  useEffect(() => {
    // The project the console's pickers open on: the pack's shop project.
    useProjectStore.setState({ currentProject: projectFromProto(shopProject()) });
  }, []);
  // A new path is a new console: the router is built for one. (A new overlay
  // remounts it anyway: FixturePreview renders a new transport's subtree afresh.)
  const router = useMemo(() => buildRouter(path), [path]);
  return (
    <>
      <FixturePreview pack={PACK} overlay={overlay} serve={serve} signedIn>
        {wrap(<RouterProvider router={router} />)}
      </FixturePreview>
      {controls}
    </>
  );
}

const CONTROL = "rounded-md border border-border bg-surface px-2 py-1 text-sm text-ink";

const SCHEMES = ["professional-blue", "refined-neutral", "modern-teal", "slate", "purple-classic", "pure-black"] as const;

/**
 * The harness's pickers, floating over the console's corner: the case, the
 * colour scheme and dark mode, each a key of the harness's own URL
 * (`case`, `scheme`, `dark`), so a screenshot of any state is a link.
 */
export function PreviewControls({
  base,
  cases,
  caseId,
  defaultCase,
  scheme,
  dark,
  children,
}: {
  base: string;
  cases: readonly { id: string; label: string }[];
  caseId: string;
  defaultCase: string;
  scheme: string;
  dark: boolean;
  /** The harness's own pickers, before the case (a view, a page). */
  children?: ReactNode;
}) {
  const navigate = useNavigate();
  useEffect(() => {
    document.documentElement.classList.toggle("dark", dark);
    document.documentElement.setAttribute("data-color-scheme", scheme);
  }, [dark, scheme]);
  const go = (next: Record<string, unknown>) => void navigate({ to: base, search: (prev: Record<string, unknown>) => ({ ...prev, ...next }), replace: true });
  return (
    <div className="fixed bottom-3 right-3 z-50 flex flex-wrap items-center gap-2 rounded-lg border border-dashed border-border bg-surface/95 px-3 py-2 shadow" data-testid="preview-controls">
      {children}
      <select data-testid="preview-case" value={caseId} onChange={(e) => go({ case: e.target.value === defaultCase ? undefined : e.target.value })} className={CONTROL}>
        {cases.map((c) => (
          <option key={c.id} value={c.id}>
            {c.label}
          </option>
        ))}
      </select>
      <select data-testid="preview-scheme" value={scheme} onChange={(e) => go({ scheme: e.target.value })} className={CONTROL}>
        {SCHEMES.map((s) => (
          <option key={s} value={s}>
            {s}
          </option>
        ))}
      </select>
      <button type="button" data-testid="preview-dark" onClick={() => go({ dark: dark ? undefined : "1" })} className={CONTROL}>
        {dark ? "Dark" : "Light"}
      </button>
    </div>
  );
}

/** The harness's own query keys, which never reach the console. */
const HARNESS_KEYS = ["case", "scheme", "dark", "desktop"];

/**
 * `path` with this page's query string, minus the harness's own keys and
 * `omit`, as the console's URL: the console's search keys (`trace`, `range`,
 * `f`, …) pass through untouched, so a console link's query works here too.
 */
export function withPageSearch(path: string, omit: readonly string[] = [], search: string = window.location.search): string {
  const params = new URLSearchParams(search);
  for (const key of [...HARNESS_KEYS, ...omit]) params.delete(key);
  const query = params.toString();
  return query ? `${path}${path.includes("?") ? "&" : "?"}${query}` : path;
}

/** This page's raw query string, re-rendering on every change of it (the pickers navigate it). */
export function usePageSearch(): URLSearchParams {
  const searchStr = useRouterState({ select: (state) => state.location.searchStr });
  return useMemo(() => new URLSearchParams(searchStr), [searchStr]);
}

/** The router parses `dark=1` as JSON, so it arrives as a number. */
export const isDark = (value: unknown) => !!value && String(value) !== "0" && String(value) !== "false";
