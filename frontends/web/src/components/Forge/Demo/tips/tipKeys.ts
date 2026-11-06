// Copyright (c) 2025 Reliant Labs

/**
 * Which console view a URL is, for the demo's one-time tips (DemoTips).
 *
 * The demo serves the console two ways, and both are read here:
 *  - the demo console, `/forge/demo?view=<obs view>&trace=…` (DemoConsole);
 *  - the console's own pages at their real paths, `/forge/p/<project>/…`
 *    (Console/paths.ts), which the demo bundle mounts on sample data.
 *
 * A view with no tip is null: no tip shows, and the last one closes.
 */

import { consolePageOf } from "../../Console/paths";
import { DEMO_SECTION_BUILT, FORGE_DEMO_PATH } from "../demoPath";

export const DEMO_TIP_KEYS = [
  "overview",
  "traces",
  "trace",
  "logs",
  "profiles",
  "errors",
  "releases",
  "environments",
  "secrets",
  "domains",
  "dashboards",
  "alerts",
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
};

function obsTip(view: string | undefined, search: Record<string, unknown>): DemoTipKey | null {
  const key = OBS_VIEW_TIPS[view ?? "overview"] ?? null;
  // An open trace is a view of its own: the waterfall.
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
    case "releases":
    case "release":
      return "releases";
    case "environments":
    case "environment":
      return "environments";
    case "secrets":
      return "secrets";
    case "domains":
      return "domains";
    case "dashboard":
      return "dashboards";
    case "obs-view":
      return obsTip(page.view, search);
    default:
      return null;
  }
}
