// Copyright (c) 2025 Reliant Labs

/**
 * Which console page a URL is, for the demo's page tours (DemoTips).
 *
 * The demo serves the console two ways, and both are read here:
 *  - the console's own pages at their real paths, `/forge/p/<project>/…`
 *    (Console/paths.ts), which the demo site mounts on sample data;
 *  - the demo console, `/forge/demo?view=<obs view>&trace=…` (DemoConsole),
 *    which the other shells keep.
 *
 * A page with no tour is null: nothing shows, and an open card closes.
 */

import { consolePageOf } from "../../Console/paths";
import { DEMO_SECTION_BUILT, FORGE_DEMO_PATH } from "../demoPath";

export const DEMO_TIP_KEYS = [
  "overview",
  "releases",
  "release",
  "environments",
  "environment",
  "secrets",
  "domains",
  "activity",
  "users",
  "dashboards",
  "errors",
  "traces",
  "trace",
  "logs",
  "profiles",
  "alerts",
  "sessions",
  "metrics",
  "rpc",
  "graph",
  "workload",
] as const;

export type DemoTipKey = (typeof DEMO_TIP_KEYS)[number];

const OBS_VIEW_TIPS: Record<string, DemoTipKey> = {
  overview: "overview",
  traces: "traces",
  logs: "logs",
  profiles: "profiles",
  errors: "errors",
  dashboards: "dashboards",
  alerts: "alerts",
  sessions: "sessions",
  metrics: "metrics",
  rpc: "rpc",
  graph: "graph",
};

function obsTip(view: string | undefined, search: Record<string, unknown>): DemoTipKey | null {
  const key = OBS_VIEW_TIPS[view ?? "overview"] ?? null;
  // An open trace is a page of its own: the waterfall.
  if (key === "traces" && typeof search.trace === "string" && search.trace) return "trace";
  return key;
}

export function demoTipKey(pathname: string, search: Record<string, unknown>): DemoTipKey | null {
  if (pathname === FORGE_DEMO_PATH || pathname.startsWith(`${FORGE_DEMO_PATH}/`)) {
    if (search.section === DEMO_SECTION_BUILT) return null;
    return obsTip(typeof search.view === "string" ? search.view : undefined, search);
  }
  const page = consolePageOf(pathname)?.page;
  if (!page) return null;
  switch (page.kind) {
    case "overview":
      return "overview";
    case "workload":
      return "workload";
    case "releases":
      return "releases";
    case "release":
      return "release";
    case "environments":
      return "environments";
    case "environment":
      return "environment";
    case "secrets":
      return "secrets";
    case "domains":
      return "domains";
    case "activity":
      return "activity";
    case "users":
      return "users";
    case "dashboard":
      return "dashboards";
    case "obs-view":
      return obsTip(page.view, search);
    default:
      return null;
  }
}

/** Search keys that open one item inside a list page: a trace, a span, an issue, a session, an alert rule… */
const DETAIL_KEYS = ["trace", "span", "issue", "session", "rsession", "rule", "rpcId", "compare", "user"] as const;

/** The item a URL has open inside its page, if any ("" for none). */
export function demoDetailOf(search: Record<string, unknown>): string {
  for (const key of DETAIL_KEYS) {
    const value = search[key];
    if (typeof value === "string" && value) return `${key}=${value}`;
  }
  return "";
}

/**
 * Whether a URL is a top-level section page, the only kind whose tip may open
 * by itself: not one release, workload, environment or dashboard, and not a
 * list page with one of its items open. Filters, ranges and tabs do not make
 * a sub-page; they are not visits either (demoPageVisit).
 */
export function isDemoSectionPage(pathname: string, search: Record<string, unknown>): boolean {
  if (demoDetailOf(search)) return false;
  if (pathname === FORGE_DEMO_PATH || pathname.startsWith(`${FORGE_DEMO_PATH}/`)) return search.section !== DEMO_SECTION_BUILT;
  const page = consolePageOf(pathname)?.page;
  if (!page) return false;
  return !["workload", "release", "environment", "dashboard"].includes(page.kind);
}
