// Copyright (c) 2025 Reliant Labs

/**
 * THE CONSOLE'S URLS (research/console-v3/PROPOSAL.md §3.2), and the mapping
 * from every URL the console used to have onto them.
 *
 *   /forge/p/:project                         Overview (project home, with the workloads)
 *   /forge/p/:project/workloads/:env/:name    one workload in one environment
 *   /forge/p/:project/releases                Releases
 *   /forge/p/:project/releases/:version       one release (its DORA strip is on Releases, D9)
 *   /forge/p/:project/observability           Observability overview
 *   /forge/p/:project/observability/:view     errors · sessions · logs · traces · metrics · profiles · dashboards · alerts
 *   /forge/p/:project/observability/dashboards/:kind/:key
 *                                             one dashboard: a release one (`release/<id>`)
 *                                             or a workspace one (`workspace/<slug>`)
 *   /forge/p/:project/environments            Environments (Configure)
 *   /forge/p/:project/environments/:env       one environment, as a configured object
 *   /forge/p/:project/secrets                 Secrets
 *   /forge/p/:project/domains                 Domains
 *   /forge/p/:project/monitoring              Monitoring settings
 *   /forge/p/:project/settings                Project settings (Usage lives here)
 *   /forge/p/:project/activity                Activity
 *
 * `:project` is the FORGE project name (forge.yaml `name`), not a Reliant
 * project id. Everything the console shows — environments, releases,
 * telemetry, secrets — is filed by the control plane under that name, and it
 * is the only name the control plane can write into a link it sends
 * (a queued deploy's action URL). The Reliant project, which is what reaches
 * a daemon, is looked up from it (ConsoleLayout); a URL naming a forge
 * project no Reliant project here declares still opens, read from the control
 * plane alone.
 *
 * `?env=` is the ONE scope param (decision D1, envScope.ts). Every list page
 * honours it; a page with an environment in its path pins that one instead.
 *
 * Nothing outside this module spells a console path: pages, the nav and the
 * redirects all build them here, so a URL change is one edit.
 */

import { defaultStringifySearch } from "@tanstack/react-router";

import { isDashboardPageKind, type DashboardPageKind } from "../Observability/Dashboards/dashboardModel";
import { OBS_SEARCH_KEYS, type ObsSearch, type ObsView } from "../Observability/obsSearch";

export const FORGE_ROOT = "/forge";

/** The Observability sub-pages that are nav entries (decision D9). */
export const OBS_NAV_VIEWS = ["errors", "sessions", "logs", "traces", "metrics", "profiles", "dashboards", "alerts"] as const;
export type ObsNavView = (typeof OBS_NAV_VIEWS)[number];

export const OBS_NAV_LABELS: Record<ObsNavView, string> = {
  errors: "Errors",
  sessions: "Sessions",
  logs: "Logs",
  traces: "Traces",
  metrics: "Metrics",
  profiles: "Profiles",
  dashboards: "Dashboards",
  alerts: "Alerts",
};

export function isObsNavView(value: string | undefined): value is ObsNavView {
  return (OBS_NAV_VIEWS as readonly string[]).includes(value ?? "");
}

/** Every page of the console, as the nav and the breadcrumb name it. */
export type ConsolePage =
  | { kind: "overview" }
  /** One workload. Its environment is in the path: a workload is one per environment, and so are its actions. */
  | { kind: "workload"; env: string; name: string }
  | { kind: "releases" }
  | { kind: "release"; version: string }
  | { kind: "observability" }
  | { kind: "obs-view"; view: ObsNavView }
  /** One dashboard. Its kind is in the path: a release dashboard and a workspace one may share a key. */
  | { kind: "dashboard"; dashKind: DashboardPageKind; key: string }
  | { kind: "environments" }
  | { kind: "environment"; env: string }
  | { kind: "secrets" }
  | { kind: "domains" }
  | { kind: "monitoring" }
  | { kind: "settings" }
  | { kind: "activity" };

export function consoleBase(project: string): string {
  return `${FORGE_ROOT}/p/${encodeURIComponent(project)}`;
}

/** The path of `page` in `project`. */
export function consolePath(project: string, page: ConsolePage): string {
  const base = consoleBase(project);
  switch (page.kind) {
    case "overview":
      return base;
    case "workload":
      return `${base}/workloads/${encodeURIComponent(page.env)}/${encodeURIComponent(page.name)}`;
    case "releases":
      return `${base}/releases`;
    case "release":
      return `${base}/releases/${encodeURIComponent(page.version)}`;
    case "observability":
      return `${base}/observability`;
    case "obs-view":
      return `${base}/observability/${page.view}`;
    case "dashboard":
      return `${base}/observability/dashboards/${page.dashKind}/${encodeURIComponent(page.key)}`;
    case "environments":
      return `${base}/environments`;
    case "environment":
      return `${base}/environments/${encodeURIComponent(page.env)}`;
    case "secrets":
      return `${base}/secrets`;
    case "domains":
      return `${base}/domains`;
    case "monitoring":
      return `${base}/monitoring`;
    case "settings":
      return `${base}/settings`;
    case "activity":
      return `${base}/activity`;
  }
}

function safeDecode(segment: string): string {
  try {
    return decodeURIComponent(segment);
  } catch {
    return segment;
  }
}

/**
 * Which console page `pathname` is, and in which project — or null for a path
 * outside the console. The inverse of consolePath, for the nav's active entry
 * and the breadcrumb.
 */
export function consolePageOf(pathname: string): { project: string; page: ConsolePage } | null {
  const parts = pathname.split("/").filter(Boolean);
  if (parts[0] !== "forge" || parts[1] !== "p" || !parts[2]) return null;
  const project = safeDecode(parts[2]);
  const [section, sub, dashKind, dashKey, ...rest] = parts.slice(3);
  const page = ((): ConsolePage | null => {
    switch (section) {
      case undefined:
        return { kind: "overview" };
      case "workloads":
        return sub && dashKind && !dashKey ? { kind: "workload", env: safeDecode(sub), name: safeDecode(dashKind) } : null;
      case "releases":
        return sub ? { kind: "release", version: safeDecode(sub) } : { kind: "releases" };
      case "observability":
        if (!sub) return { kind: "observability" };
        if (sub === "dashboards" && dashKind) {
          return isDashboardPageKind(dashKind) && dashKey && rest.length === 0 ? { kind: "dashboard", dashKind, key: safeDecode(dashKey) } : null;
        }
        return isObsNavView(sub) && !dashKind ? { kind: "obs-view", view: sub } : null;
      case "environments":
        return sub ? { kind: "environment", env: safeDecode(sub) } : { kind: "environments" };
      case "secrets":
      case "domains":
      case "monitoring":
      case "settings":
      case "activity":
        return sub ? null : { kind: section };
      default:
        return null;
    }
  })();
  return page ? { project, page } : null;
}

/** Whether a page has an environment in its path, so the top-bar scope does not apply to it. */
export function pinnedEnvOf(page: ConsolePage): string | null {
  return page.kind === "environment" || page.kind === "workload" ? page.env : null;
}

/**
 * `path` with `search` as its query string, in the router's own encoding
 * (arrays and objects as JSON), so a built href parses back to the search
 * object it came from. Undefined and empty values are dropped.
 */
export function withSearch(path: string, search: Record<string, unknown> = {}): string {
  const kept = Object.fromEntries(
    Object.entries(search).filter(([, value]) => value !== undefined && value !== null && value !== "")
  );
  return `${path}${defaultStringifySearch(kept)}`;
}

// ── The Observability panel's views, as console pages ──────────────────────

/**
 * Where an Observability view lives now. The panel still speaks in views
 * (obsSearch.ts) — its links say "open Logs", "open Traces" — and this turns
 * each into the page that hosts it: the nav entries under Observability and
 * Project settings for Usage (decision D9). Workloads and Delivery are no
 * views any more: the workloads are the Overview's own tables (with a page
 * per workload), and the DORA figures are the Releases page's strip.
 */
export function pageOfObsView(view: ObsView | undefined): ConsolePage {
  switch (view) {
    case undefined:
    case "overview":
      return { kind: "observability" };
    case "usage":
      return { kind: "settings" };
    default:
      return { kind: "obs-view", view };
  }
}

/** The panel view a console page hosts, when it hosts one. */
export function obsViewOfPage(page: ConsolePage): ObsView | null {
  switch (page.kind) {
    case "observability":
      return "overview";
    case "settings":
      return "usage";
    case "obs-view":
      // Metrics (the explorer, ../Observability/Metrics) and Dashboards
      // (decision DB) are console pages of their own, not panel views.
      return page.view === "metrics" || page.view === "dashboards" ? null : page.view;
    default:
      return null;
  }
}

/**
 * The `?env=` value for what the old `envs` filter said: one environment is
 * that environment; none (every environment) or several is All — the console
 * scope is one environment or all of them.
 */
export function envScopeOfEnvs(envs: readonly string[] | undefined): string {
  return envs && envs.length === 1 ? envs[0]! : "all";
}

/** The Observability keys of `search`, without the two the console moved out of the query (view, envs). */
export function carriedObsSearch(search: Record<string, unknown>): Record<string, unknown> {
  const kept: Record<string, unknown> = {};
  for (const key of OBS_SEARCH_KEYS) {
    if (key === "view" || key === "envs") continue;
    if (search[key] !== undefined) kept[key] = search[key];
  }
  return kept;
}

// ── The retired URLs ────────────────────────────────────────────────────────

/**
 * What a retired Observability URL carried: the current keys, plus the
 * retired `view=workloads` and the `workload` it had open.
 */
export type LegacyObsSearch = Omit<ObsSearch, "view"> & { view?: ObsView | "workloads"; workload?: string };

/** What a legacy `/forge/env/:env` URL carried. */
export interface LegacyEnvSearch extends LegacyObsSearch {
  tab?: string;
  secret?: string;
}

/**
 * The retired Workloads view → the Overview (where the workloads are now), or
 * the workload's own page when the URL names it and one environment.
 */
function legacyWorkloadsTarget(project: string, env: string, workload: string | undefined): string {
  if (workload && env !== "all") return withSearch(consolePath(project, { kind: "workload", env, name: workload }), { env });
  return withSearch(consolePath(project, { kind: "overview" }), { env, q: workload });
}

/**
 * `/forge/env/:env?tab=…` → its page now, with `?env=` set to that env so the
 * scope a notification or bookmark meant survives the move.
 *
 * The per-environment resource tabs become the project's resource pages
 * scoped to that environment (decision D1); everything else about the
 * environment — overview, the local dev stack, domain bindings, the retired
 * tab names — is the environment's own page under Configure.
 */
export function legacyEnvTarget(project: string, env: string, search: LegacyEnvSearch): string {
  switch (search.tab) {
    case "releases":
      return withSearch(consolePath(project, { kind: "releases" }), { env });
    case "activity":
      return withSearch(consolePath(project, { kind: "activity" }), { env });
    case "secrets":
      return withSearch(consolePath(project, { kind: "secrets" }), { env, secret: search.secret });
    case "observability":
      if (search.view === "workloads") return legacyWorkloadsTarget(project, env, search.workload);
      return withSearch(consolePath(project, pageOfObsView(search.view)), {
        env,
        ...carriedObsSearch(search as Record<string, unknown>),
      });
    case "domains":
    case "identity":
    case "running":
      return withSearch(consolePath(project, { kind: "environment", env }), { env, tab: search.tab });
    default:
      return withSearch(consolePath(project, { kind: "environment", env }), { env });
  }
}

/** `/forge/observability?view=…&envs=…` → the page hosting that view, scoped. */
export function legacyObservabilityTarget(project: string, search: LegacyObsSearch): string {
  if (search.view === "workloads") return legacyWorkloadsTarget(project, envScopeOfEnvs(search.envs), search.workload);
  return withSearch(consolePath(project, pageOfObsView(search.view)), {
    env: envScopeOfEnvs(search.envs),
    ...carriedObsSearch(search as Record<string, unknown>),
  });
}
