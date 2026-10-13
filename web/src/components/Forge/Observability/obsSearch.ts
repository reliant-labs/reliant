// Copyright (c) 2025 Reliant Labs

/**
 * The observability area's URL state.
 *
 * Everything a reader would send a colleague — which view, which window, the
 * query, the trace they opened — is in the URL, for the same reason the env
 * page's `tab` and `secret` are: "look at these errors" is a link, and a
 * refresh must not throw the reader back to the overview.
 */

import type { Severity } from "@/services/controlPlane/observability/types";
import { SEVERITIES } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";

export const OBS_VIEWS = ["overview", "logs", "traces", "dashboards", "profiles", "usage"] as const;
export type ObsView = (typeof OBS_VIEWS)[number];

export const OBS_VIEW_LABELS: Record<ObsView, string> = {
  overview: "Overview",
  logs: "Logs",
  traces: "Traces",
  dashboards: "Dashboards",
  profiles: "Profiles",
  usage: "Usage",
};

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
] as const satisfies readonly (keyof ObsSearch)[];

/**
 * What a cross-view link carries. Opening a trace from a log keeps the window
 * and service and drops the log query, which means nothing to the trace list.
 */
export type ObsNavigate = (next: ObsSearch, options?: { replace?: boolean }) => void;
