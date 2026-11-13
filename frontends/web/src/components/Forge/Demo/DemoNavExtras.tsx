// Copyright (c) 2025 Reliant Labs

/**
 * The public demo's additions to the console (Console/consoleNavExtras), at
 * the "50" density (balanced/):
 *
 *  - the nav grouped by what a reader is after (Issues, Performance,
 *    Sessions, Product analytics…) with the rarer tools under "More tools",
 *    and Home and "How it was built" as rows of their own;
 *  - every page's technical name kept, with a plain subtitle under it;
 *  - Errors, Traces and Sessions lead with a sentence and their most
 *    important item (balanced/BalancedPages.tsx), and the console's own list
 *    is one click away; one issue, trace or session is the console's page
 *    with "What happened" on top;
 *  - charts draw at most two series, under a headline figure (chartDensity);
 *  - the environments in promotion order (dev, staging, prod), and on a phone
 *    the tour, Install Forge and the telemetry notice in the drawer.
 *
 * A guided tour walks the console as it is (its spotlights name its rows and
 * panels), so with `?tour=` the demo keeps the console's own nav.
 */

import { useMemo, type ReactNode } from "react";
import { useRouterState } from "@tanstack/react-router";
import { Hammer } from "lucide-react";

import { ConsoleNavExtrasProvider, type ConsoleNavExtras } from "../Console/consoleNavExtras";
import type { ConsolePage } from "../Console/paths";
import { ChartDensityProvider, type ChartDensity } from "../Observability/charts/chartDensity";
import { PageAgentStrip } from "./approachable/AgentCards";
import { balancedCrumbs, balancedNav, SUBTITLES } from "./balanced/balancedNav";
import {
  BalancedErrors,
  BalancedSessions,
  BalancedTraces,
  IssueWhatHappened,
  OverviewIntro,
  ReleasesIntro,
  SessionWhatHappened,
  TraceWhatHappened,
} from "./balanced/BalancedPages";
import { DemoDrawerActions } from "./DemoScreenActions";
import { DEMO_SECTION_BUILT, FORGE_DEMO_PATH, type DemoSection } from "./demoPath";

/** The build page's one name: nav row, breadcrumb, banner and tour step. */
export const HOW_BUILT_LABEL = "How it was built";
export const HOW_BUILT_HREF = `${FORGE_DEMO_PATH}?section=${DEMO_SECTION_BUILT}`;
export const DEMO_ENV_ORDER = ["dev", "staging", "prod"] as const;

/** Two series and a headline per chart: the "50" chart. */
const CHART_DENSITY: ChartDensity = { maxSeries: 2, headline: true };

/** The search keys that make a list the console's own (a filter was chosen, or "All filters"). */
const LIST_KEYS: Record<"errors" | "traces" | "sessions", readonly string[]> = {
  errors: ["q", "istatus", "platform", "isort", "assignee", "service", "user", "browser", "os", "build"],
  traces: ["q", "service", "errors", "minDuration", "lat", "f", "groupBy", "user", "rsession", "browser", "os", "fe", "be", "range"],
  sessions: ["q", "service", "errors", "user", "browser", "os", "fe", "cursor", "range"],
};

const filtered = (search: Record<string, unknown>, view: keyof typeof LIST_KEYS) =>
  LIST_KEYS[view].some((key) => search[key] !== undefined && search[key] !== "");

/** The list pages the demo draws itself: only with nothing open and no filter chosen. */
function balancedPage(page: ConsolePage, search: Record<string, unknown>): ReactNode | null {
  if (page.kind !== "obs-view") return null;
  if (page.view === "errors" && !search.issue && !filtered(search, "errors")) return <BalancedErrors search={search} />;
  if (page.view === "traces" && !search.trace && !filtered(search, "traces")) return <BalancedTraces search={search} />;
  if (page.view === "sessions" && !search.session && !filtered(search, "sessions")) return <BalancedSessions search={search} />;
  return null;
}

/** What goes under a console page's own header. */
function balancedIntro(key: string, search: Record<string, unknown>): ReactNode | null {
  switch (key) {
    case "errors":
      return search.issue ? (
        <IssueWhatHappened search={search} />
      ) : (
        <PageAgentStrip id="fix-errors" lead="An agent can read an issue like these, find the change that caused it, and open the fix for you to review." />
      );
    case "traces":
      return search.trace ? (
        <TraceWhatHappened search={search} />
      ) : (
        <PageAgentStrip id="slow-pages" lead="Hand an agent one slow request and it finds where the time goes." />
      );
    case "sessions":
      return search.session ? <SessionWhatHappened search={search} /> : null;
    case "alerts":
      return <PageAgentStrip id="watch-alerts" lead="When an alert fires at 2am, an agent can look first and have a fix waiting by morning." />;
    case "releases":
      return <ReleasesIntro />;
    case "overview":
      return <OverviewIntro />;
    default:
      return null;
  }
}

export function DemoNavExtras({ builtActive = false, section, children }: { builtActive?: boolean; section?: DemoSection; children: ReactNode }) {
  const search = useRouterState({ select: (s) => s.location.search }) as Record<string, unknown>;
  const current = section ?? (builtActive ? DEMO_SECTION_BUILT : undefined);
  const env = typeof search.env === "string" ? search.env : undefined;
  const value = useMemo<ConsoleNavExtras>(() => {
    const common = { envOrder: DEMO_ENV_ORDER, drawerAppendix: <DemoDrawerActions /> };
    if (search.tour) {
      return {
        ...common,
        lead: { label: HOW_BUILT_LABEL, href: HOW_BUILT_HREF, icon: <Hammer className="h-4 w-4" aria-hidden="true" />, active: current === DEMO_SECTION_BUILT },
      };
    }
    return {
      ...common,
      nav: (page, alertsFiring) => balancedNav(page, current, alertsFiring, env),
      crumbs: (page, own) => balancedCrumbs(page, own, current, env),
      // A section page (Home, How releases work…) is DemoConsole's own body: nothing added or replaced.
      subtitle: (key) => (current ? null : (SUBTITLES[key] ?? null)),
      intro: (key) => (current ? null : balancedIntro(key, search)),
      page: (page) => (current ? null : balancedPage(page, search)),
    };
  }, [current, search, env]);
  return (
    <ConsoleNavExtrasProvider value={value}>
      <ChartDensityProvider value={search.tour ? {} : CHART_DENSITY}>{children}</ChartDensityProvider>
    </ConsoleNavExtrasProvider>
  );
}
