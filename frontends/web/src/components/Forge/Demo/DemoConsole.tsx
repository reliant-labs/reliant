// Copyright (c) 2025 Reliant Labs

/**
 * The forge demo console: `/forge/demo`, in both shells.
 *
 * The real console chrome (ConsoleShell) and the real Observability views
 * (ObservabilityPanel) on the in-memory fixture API — a made-up app called
 * shop, with an api, a worker and a browser frontend in dev, staging and prod.
 * The forge journey (Onboarding/journeys/forge.tsx) walks it; anyone may
 * browse it.
 *
 * ── NO DAEMON, NO CHAT, NO CONTROL PLANE ────────────────────────────────────
 *
 * On forge.reliantlabs.io it is public: signed out, no daemon, no Reliant
 * project. So everything it reads comes from createFixtureApi, through
 * ObservabilityApiProvider and its own QueryClient, and "Ask Reliant" is in
 * handoff mode (AskAgentModeProvider): one "Ask an agent" button on every
 * surface, opening a sheet to copy from rather than starting a chat. A test
 * (Demo/__tests__/DemoConsole.test.tsx) walks every view and asserts that no
 * transport, fetch, project list or chat start was touched.
 *
 * ── THE URL IS THE STATE ────────────────────────────────────────────────────
 *
 * The panel's state is the route's search (obsSearch.ts, plus `tour`), so
 * every view and every open trace or issue is a link, and the tour engine's
 * `advanceOn` predicates read it. The nav's rows stay on this route
 * (`linkTo`): Observability views switch `view`; the pages the demo does not
 * have (Releases, Environments, …) open its overview.
 */

import { lazy, Suspense, useCallback, useMemo, useState } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";

import PageHeader from "@/components/forge-ui/page_header";
import { getTourShell, type TourShell } from "@/components/Onboarding/tourShell";
import { ObservabilityBillingApiContext } from "@/hooks/observability-billing-queries";
import { ObservabilityApiProvider } from "@/services/controlPlane/observability/ObservabilityApiContext";

import { ConsoleShell } from "../Console/ConsoleShell";
import { ProjectMonogram } from "../Console/ConsoleProjectSwitcher";
import { ENV_ALL } from "../Console/envScope";
import { consolePath, isObsNavView, withSearch, type ConsolePage } from "../Console/paths";
import { isDemoShell } from "@/lib/shell";
import { AskAgentModeProvider } from "../Observability/AskReliant/askAgentMode";
import ObservabilityPanel, { type ObsPlace } from "../Observability/ObservabilityPanel";
import { OBS_VIEW_LABELS, OBS_VIEWS, viewOf, type ObsNavigate, type ObsSearch, type ObsView } from "../Observability/obsSearch";
import { FIXTURE_DECLARED } from "../Observability/__fixtures__/fixtureAlerting";
import { FIXTURE_ENVS, FIXTURE_PROJECT } from "../Observability/__fixtures__/fixtureApi";
import { ObsReadOnlyProvider, type ObsReadOnly } from "../Observability/obsReadOnly";
import { createDemoApi } from "./demoData/demoApi";
import { DemoBrowserProviders } from "./DemoBrowserProviders";
import { DemoBanner } from "./DemoBanner";
import { DemoWelcome } from "./DemoWelcome";
import { DEMO_SECTION_BUILT, FORGE_DEMO_PATH } from "./demoPath";
import { HowBuiltLink } from "./howBuilt/HowBuiltLink";

// The recorded forge run and its fixtures: a chunk of their own, loaded when the section opens.
const HowThisAppWasBuilt = lazy(() => import("./howBuilt/HowThisAppWasBuilt"));

/** The fixture environments with what each declares (rules, routes, observability policy), for Alerts. */
const ENVIRONMENTS = FIXTURE_ENVS.map((env) => ({ ...env, declared: FIXTURE_DECLARED[env.id] ?? null }));
const NAV_ENVS = FIXTURE_ENVS.map((env) => ({ name: env.name, preview: false }));
const SCOPE = { long: FIXTURE_ENVS.map((env) => ({ name: env.name, recorded: true })), previews: [] };
const PLACE: ObsPlace = { kind: "project", project: FIXTURE_PROJECT, environments: ENVIRONMENTS };

// Usage reads are the control plane's; the demo answers them as an older control plane would.
const legacyOnly = async () => {
  throw new ConnectError("Not in the demo", Code.Unimplemented);
};
const legacyBillingApi = {
  getObservabilityUsage: legacyOnly,
  listObservabilityUsage: legacyOnly,
  getObservabilityStorage: legacyOnly,
  getObservabilityLimits: legacyOnly,
  updateObservabilityLimits: legacyOnly,
};

const READ_ONLY: ObsReadOnly = { readOnly: true, reason: "Read-only demo" };

/** Every nav row and crumb stays on the demo: a panel view by `view`, anything else the overview. */
function demoLink(target: ConsolePage): string {
  // The demo site mounts the real console (/forge/p/shop/…): every nav row goes to its page.
  if (isDemoShell()) return consolePath(FIXTURE_PROJECT, target);
  if (target.kind === "obs-view" && (OBS_VIEWS as readonly string[]).includes(target.view)) {
    return withSearch(FORGE_DEMO_PATH, { view: target.view });
  }
  return FORGE_DEMO_PATH;
}

/** The nav's open page for a panel view. */
function pageOf(view: ObsView): ConsolePage {
  return view !== "overview" && isObsNavView(view) ? { kind: "obs-view", view } : { kind: "overview" };
}

export interface DemoConsoleProps {
  /** Which shell hosts it; default the tour's (the hostname, or setTourShell at the forge shell's boot). */
  shell?: TourShell;
}

export function DemoConsole({ shell: shellProp }: DemoConsoleProps = {}) {
  const shell = shellProp ?? getTourShell();
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as ObsSearch & { tour?: string; section?: string };
  const [collapsed, setCollapsed] = useState(false);
  // Its own client and API: nothing here shares a cache entry with a real project's console.
  const [queryClient] = useState(() => new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false } } }));
  // Writes are refused in every shell, visibly (obsReadOnly) and underneath (readOnlyApi); on the
  // demo host the scenario's reads go over forge's mock transport (demoData/demoApi.ts).
  const api = useMemo(() => createDemoApi(), []);

  const view = viewOf(search.view);
  const env = search.envs?.length === 1 ? search.envs[0] : ENV_ALL;

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
  // "How this app was built" back to the console view it was opened from (`tour` rides along).
  const leaveBuilt = useCallback(() => {
    void navigate({ to: ".", search: ((prev: Record<string, unknown>) => ({ ...prev, section: undefined })) as never } as never);
  }, [navigate]);
  const openBuilt = useCallback(() => {
    void navigate({
      to: ".",
      search: ((prev: Record<string, unknown>) => ({ ...prev, section: DEMO_SECTION_BUILT, trace: undefined, span: undefined, issue: undefined })) as never,
    } as never);
  }, [navigate]);

  return (
    <AskAgentModeProvider mode="handoff" cli={shell === "forge" ? "forge" : "reliant forge"} demo>
      <ObsReadOnlyProvider value={READ_ONLY}>
        <QueryClientProvider client={queryClient}>
          <DemoBrowserProviders>
          <ObservabilityApiProvider api={api}>
            <ObservabilityBillingApiContext.Provider value={legacyBillingApi}>
              <ConsoleShell
                project={FIXTURE_PROJECT}
                page={pageOf(view)}
                env={env}
                envs={NAV_ENVS}
                alertsFiring={1}
                scope={SCOPE}
                onEnvChange={(next) => navigateObs({ envs: next === ENV_ALL ? undefined : [next] }, { replace: true })}
                linkTo={demoLink}
                collapsed={collapsed}
                onToggleCollapsed={() => setCollapsed((prev) => !prev)}
                brand={
                  <span className="flex items-center gap-2 px-1.5 text-sm font-semibold text-ink">
                    <ProjectMonogram name={FIXTURE_PROJECT} />
                    {FIXTURE_PROJECT}
                  </span>
                }
                brandCollapsed={<ProjectMonogram name={FIXTURE_PROJECT} />}
              >
                {!(isDemoShell() && search.section === DEMO_SECTION_BUILT) && <DemoBanner shell={shell} />}
                <div className="space-y-4" data-testid="forge-demo">
                  {search.section === DEMO_SECTION_BUILT ? (
                    <>
                      {isDemoShell() ? <DemoWelcome /> : <PageHeader density="compact" title="How this app was built" className="mb-0" />}
                      <Suspense fallback={<p className="text-sm text-ink-muted">Loading the recorded run…</p>}>
                        <HowThisAppWasBuilt onBack={isDemoShell() ? undefined : leaveBuilt} autoPlay={false} leadWithRecording={isDemoShell()} />
                      </Suspense>
                    </>
                  ) : (
                    <>
                      {/* One compact row; the top bar's scope control says which environments. */}
                      <PageHeader density="compact" title={OBS_VIEW_LABELS[view]} className="mb-0" />
                      <ObservabilityPanel place={PLACE} search={{ ...search, view }} navigate={navigateObs} />
                      {/* Last on the page, after the console's views. */}
                      <HowBuiltLink onOpen={openBuilt} />
                    </>
                  )}
                </div>
              </ConsoleShell>
            </ObservabilityBillingApiContext.Provider>
          </ObservabilityApiProvider>
          </DemoBrowserProviders>
        </QueryClientProvider>
      </ObsReadOnlyProvider>
    </AskAgentModeProvider>
  );
}

export default DemoConsole;
