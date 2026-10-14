// Copyright (c) 2025 Reliant Labs

/**
 * The observability area's URL state.
 *
 * Everything a reader would send a colleague — which view, which window, the
 * query, the trace they opened — is in the URL, for the same reason the env
 * page's `tab` and `secret` are: "look at these errors" is a link, and a
 * refresh must not throw the reader back to the overview.
 */

import type { Panel, Severity } from "@/services/controlPlane/observability/types";
import { SEVERITIES } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";

import type { CompareWindow, DoraWindow } from "./deliveryScope";

/**
 * The views, in tab order. A view is one entry in this list (the route schema
 * reads it), one label and one branch in ObservabilityPanel.
 */
export const OBS_VIEWS = ["overview", "alerts", "delivery", "logs", "traces", "dashboards", "workloads", "profiles", "usage"] as const;
export type ObsView = (typeof OBS_VIEWS)[number];

export const OBS_VIEW_LABELS: Record<ObsView, string> = {
  overview: "Overview",
  alerts: "Alerts",
  delivery: "Delivery",
  logs: "Logs",
  traces: "Traces",
  dashboards: "Dashboards",
  workloads: "Workloads",
  profiles: "Profiles",
  usage: "Usage",
};

/** The views where grouping means something: the ones that aggregate. */
export const GROUPABLE_VIEWS: readonly ObsView[] = ["overview", "traces", "dashboards"];

export function viewOf(param: string | undefined): ObsView {
  return (OBS_VIEWS as readonly string[]).includes(param ?? "") ? (param as ObsView) : "overview";
}

/** `severity=warn,error` ⇄ ["warn", "error"]. Unknown names are dropped, not errors. */
export function severitiesOf(param: string | undefined): Severity[] {
  if (!param) return [];
  const wanted = new Set(param.split(",").map((part) => part.trim()));
  return SEVERITIES.filter((severity) => wanted.has(severity));
}

export function severityParam(severities: Severity[]): string | undefined {
  return severities.length ? severities.join(",") : undefined;
}

/** The URL keys this area owns. */
export interface ObsSearch {
  view?: ObsView;
  range?: RangePreset;
  service?: string;
  severity?: string;
  q?: string;
  trace?: string;
  span?: string;
  dashboard?: string;
  profileType?: string;
  live?: boolean;
  /** Traces: only the ones with a failed span. */
  errors?: boolean;
  /** Traces: only the ones slower than this ("500ms", "1s"). */
  minDuration?: string;
  /**
   * The project area's environment filter: env NAMES (unique within a
   * project, and readable in a shared link). Absent: every environment. An
   * env page pins its own environment and ignores this.
   */
  envs?: string[];
  /** Attribute filters, one token each (obsFilters.ts): `service.version:v1.8`. */
  f?: string[];
  /** Up to three keys every aggregating view splits its series by. */
  groupBy?: string[];
  /** Workloads: the workload open in detail, and optionally one container of it. */
  workload?: string;
  container?: string;
  /**
   * Dashboards: in-place panel edits, by panel id. A panel here REPLACES the
   * dashboard's own; `null` hides it; an id the dashboard does not have is a
   * panel the reader added. Nothing is saved anywhere but the URL — "Save as
   * code" turns the result into a file to commit.
   */
  edit?: Record<string, Panel | null>;
  /** Alerts: which part of the area is open. Absent: what is firing. */
  alerts?: AlertsTab;
  /** Alerts → Rules: the rule open in detail, `<env name>/<rule>`. */
  rule?: string;
  /**
   * A release comparison open over the current view: the promotion as
   * `<envId>~<promotedAtMs>~<promotionId>` (deliveryScope.ts compareParam).
   */
  compare?: string;
  /** The comparison's window on each side. Absent: 30m. */
  cmpWindow?: CompareWindow;
  /** Delivery: the DORA window. Absent: the last 30 days. */
  doraWindow?: DoraWindow;
  /** Delivery: N in "rolled back within N hours" (1–168). Absent: the server's 24. */
  failureHours?: number;
}

export const ALERTS_TABS = ["firing", "history", "silences", "rules", "receivers"] as const;
export type AlertsTab = (typeof ALERTS_TABS)[number];

export function alertsTabOf(param: string | undefined): AlertsTab {
  return (ALERTS_TABS as readonly string[]).includes(param ?? "") ? (param as AlertsTab) : "firing";
}

/** Every key above — what leaving the Observability tab clears, so its filters do not ride along to Secrets. */
export const OBS_SEARCH_KEYS = [
  "view",
  "range",
  "service",
  "severity",
  "q",
  "trace",
  "span",
  "dashboard",
  "profileType",
  "live",
  "errors",
  "minDuration",
  "envs",
  "f",
  "groupBy",
  "workload",
  "container",
  "edit",
  "alerts",
  "rule",
  "compare",
  "cmpWindow",
  "doraWindow",
  "failureHours",
] as const satisfies readonly (keyof ObsSearch)[];

/**
 * What a cross-view link carries. Opening a trace from a log keeps the window
 * and service and drops the log query, which means nothing to the trace list.
 */
export type ObsNavigate = (next: ObsSearch, options?: { replace?: boolean }) => void;

/** Fails to compile if a URL key is added to ObsSearch and not to OBS_SEARCH_KEYS (leaving the tab would keep it). */
type AssertNever<_T extends never> = void;
export type _EveryObsKeyIsCleared = AssertNever<Exclude<keyof ObsSearch, (typeof OBS_SEARCH_KEYS)[number]>>;
