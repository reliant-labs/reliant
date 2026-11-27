// Copyright (c) 2025 Reliant Labs

/**
 * The forge demo's own pages: `/forge/demo` on the demo shell
 * (demoShellRoutes.tsx), the demo bundle on demo.reliantlabs.io. The app's
 * `/forge/demo` leaves for it (demoRoute.tsx, createForgeDemoHandoffRoutes):
 * the demo has one home.
 *
 * A page of the app's own shell (demoShellRoutes.tsx mounts it under the
 * console's `_authenticated` layout, inside AppShellFrame): the console's page
 * bar (ConsoleFrame: the breadcrumb and the environment scope) over the
 * demo's sections (Home, How releases work, Put an agent on it, How this
 * shop was built), the Data and Storage browsers (`?browser=`), and the
 * Observability views (ObservabilityPanel) by `?view=`, all on a made-up app
 * called shop. The console's own pages (/forge/p/shop/…) are the rest of the
 * demo; the shell's nav (DemoNav, demoNavConfig.ts) opens them.
 *
 * ── NO DAEMON, NO CHAT, NO CONTROL PLANE ────────────────────────────────────
 *
 * Everything it reads is the shop fixture pack (src/fixtures/shop) on the
 * demo's transport, with the demo's providers (DemoConsoleProviders, its
 * layout's, as the console's pages'): the Observability views on the console's REAL client,
 * the Data, Storage and Identity APIs, and "Ask an agent" in handoff mode.
 * Writes are refused visibly (the shell root's ObsReadOnlyProvider) and
 * underneath (the demo transport's read-only guard). A
 * test (Demo/__tests__/DemoConsole.test.tsx) walks every view and asserts
 * that no transport, fetch, project list or chat start was touched.
 *
 * ── THE URL IS THE STATE ────────────────────────────────────────────────────
 *
 * The page's state is the route's search (obsSearch.ts, plus `section`,
 * `browser` and `tour`), so every view and every open trace or issue is a link.
 */

import { lazy, Suspense, useCallback } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";

import PageHeader from "@/components/forge-ui/page_header";
import type { RosterEnv } from "@/services/forge/roster";

import { ConsoleFrame } from "../Console/ConsoleFrame";
import { DEFAULT_ENV, ENV_ALL, scopeOptions } from "../Console/envScope";
import { isObsNavView, withSearch, type ConsolePage } from "../Console/paths";
import ObservabilityPanel, { type ObsPlace } from "../Observability/ObservabilityPanel";
import { OBS_VIEW_LABELS, viewOf, type ObsNavigate, type ObsSearch, type ObsView } from "../Observability/obsSearch";
import { FIXTURE_DECLARED } from "../Observability/__fixtures__/fixtureAlerting";
import { FIXTURE_ENVS, FIXTURE_PROJECT } from "../Observability/__fixtures__/fixtureApi";
import { DataBrowser } from "../Data/DataBrowser";
import type { DataSearch } from "../Data/dataUrl-model";
import { StorageBrowser } from "../Storage/StorageBrowser";
import type { StorageSearch } from "../Storage/storageUrl-model";
import { DEMO_SECTION_AGENTS, DEMO_SECTION_BUILT, DEMO_SECTION_HOME, DEMO_SECTION_RELEASES, FORGE_DEMO_PATH } from "./demoPath";
import { AgentsPage } from "./sections/AgentsPage";
import { DemoHome } from "./sections/DemoHome";
import { ReleasesExplained } from "./sections/ReleasesExplained";

// The recorded forge run and its fixtures: a chunk of their own, loaded when the section opens.
const HowThisAppWasBuilt = lazy(() => import("./howBuilt/HowThisAppWasBuilt"));

/** The fixture environments with what each declares (rules, routes, observability policy), for Alerts. */
const ENVIRONMENTS = FIXTURE_ENVS.map((env) => ({ ...env, declared: FIXTURE_DECLARED[env.id] ?? null }));
/** The scope control's choices, in the app's order (prod first): the sample's environments are all recorded and deployed. */
const SCOPE = scopeOptions(FIXTURE_ENVS.map((env): RosterEnv => ({ name: env.name, source: "backend", lifecycle: "deployed", live: null, forge: null })));
const PLACE: ObsPlace = { kind: "project", project: FIXTURE_PROJECT, environments: ENVIRONMENTS };

/** The console page a panel view stands for: the breadcrumb's page. */
function pageOf(view: ObsView): ConsolePage {
  return view !== "overview" && isObsNavView(view) ? { kind: "obs-view", view } : { kind: "overview" };
}

export function DemoConsole() {
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as ObsSearch & DataSearch & StorageSearch & { tour?: string; section?: string; browser?: "data" | "storage" };

  const view = viewOf(search.view);
  // The demo opens on prod, as its console pages do (Console/envScope DEFAULT_ENV); `envs: ["all"]` is All.
  const fallbackEnv = search.browser ? "staging" : DEFAULT_ENV;
  const env = search.envs?.length === 1 ? search.envs[0] : fallbackEnv;
  const browserEnv = FIXTURE_ENVS.find((candidate) => candidate.name === env);
  const browserHref = (next: DataSearch | StorageSearch) => withSearch(FORGE_DEMO_PATH, { browser: search.browser, envs: [env], ...next });
  const navigateBrowser = (next: DataSearch | StorageSearch, options?: { replace?: boolean }) => void navigate({
    to: ".", search: { browser: search.browser, envs: [env], ...next }, replace: options?.replace,
  } as never);

  // The panel's navigation, onto this route's search (`tour` rides along: the route retains it).
  const navigateObs = useCallback<ObsNavigate>(
    (next, options) => {
      void navigate({
        to: ".",
        search: ((prev: Record<string, unknown>) => ({ ...prev, ...next })) as never,
        replace: options?.replace,
      } as never);
    },
    [navigate]
  );

  return (
    // The breadcrumb names a section page (DemoConsoleHost's hostPage), else the view's console page.
    <ConsoleFrame
      project={FIXTURE_PROJECT}
      page={search.browser ? { kind: search.browser } : pageOf(view)}
      env={env}
      scope={SCOPE}
      onEnvChange={(next) => navigateObs({ envs: next === fallbackEnv ? undefined : [next] }, { replace: true })}
    >
      {/* The demo says "sample, read-only" once, in its action row (DemoScreenActions). */}
      <div className="space-y-4" data-testid="forge-demo">
        {search.section === DEMO_SECTION_HOME ? (
          <DemoHome />
        ) : search.section === DEMO_SECTION_RELEASES ? (
          <ReleasesExplained />
        ) : search.section === DEMO_SECTION_AGENTS ? (
          <AgentsPage />
        ) : search.section === DEMO_SECTION_BUILT ? (
          <>
            <PageHeader density="compact" title="How this shop was built" className="mb-0" />
            <Suspense fallback={<p className="text-sm text-ink-muted">Loading the recorded run…</p>}>
              <HowThisAppWasBuilt autoPlay={false} leadWithRecording />
            </Suspense>
          </>
        ) : search.browser ? (
          <>
            <PageHeader density="compact" title={search.browser === "data" ? "Data" : "Storage"} className="mb-0" />
            {!browserEnv ? <p role="alert">Choose a sample environment: dev, staging or prod.</p> : search.browser === "data" ? (
              <DataBrowser env={browserEnv} search={search} onNavigate={navigateBrowser} href={browserHref} />
            ) : (
              <StorageBrowser env={browserEnv} search={search} onNavigate={navigateBrowser} href={browserHref} />
            )}
          </>
        ) : (
          <>
            {/* One compact row; the top bar's scope control says which environments. */}
            <PageHeader density="compact" title={OBS_VIEW_LABELS[view]} className="mb-0" />
            <ObservabilityPanel place={PLACE} search={{ ...search, view, envs: env === ENV_ALL ? undefined : [env] }} navigate={navigateObs} />
          </>
        )}
      </div>
    </ConsoleFrame>
  );
}

export default DemoConsole;
