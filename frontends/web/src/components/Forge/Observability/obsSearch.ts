// Copyright (c) 2025 Reliant Labs

/**
 * The observability area's URL state.
 *
 * Everything a reader would send a colleague — which view, which window, the
 * query, the trace they opened — is in the URL, for the same reason the env
 * page's `tab` and `secret` are: "look at these errors" is a link, and a
 * refresh must not throw the reader back to the overview.
 */

import type { HealthWindow } from "@/services/controlPlane/observability/deliveryTypes";
import { ISSUE_PLATFORMS, ISSUE_SORTS, ISSUE_STATUSES, type IssuePlatform, type IssueSort, type IssueStatus } from "@/services/controlPlane/observability/issuesTypes";
import type { Severity } from "@/services/controlPlane/observability/types";
import { SEVERITIES } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";

/**
 * The views. A view is one entry in this list (the route schema reads it), one
 * label and one branch in ObservabilityPanel. Delivery is not one: releases
 * and their DORA figures are the console's Releases page. Dashboards are not
 * one either: native dashboards are console pages of their own (Observability
 * › Dashboards). Usage is not one: the organization's allowance is the app's
 * Settings › Billing.
 */
export const OBS_VIEWS = ["overview", "graph", "rpc", "alerts", "errors", "sessions", "logs", "traces", "profiles"] as const;
export type ObsView = (typeof OBS_VIEWS)[number];

export const OBS_VIEW_LABELS: Record<ObsView, string> = {
  overview: "Overview",
  graph: "Service graph",
  rpc: "RPC explorer",
  alerts: "Alerts",
  errors: "Errors",
  sessions: "Sessions",
  logs: "Logs",
  traces: "Traces",
  profiles: "Profiles",
};

/** The views where grouping means something: the ones that aggregate. */
export const GROUPABLE_VIEWS: readonly ObsView[] = ["overview", "traces"];

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

/** `istatus=resolved,ignored` ⇄ ["resolved", "ignored"]. Empty: the open ones. Unknown names are dropped. */
export function issueStatusesOf(param: string | undefined): IssueStatus[] {
  if (!param) return [];
  const wanted = new Set(param.split(",").map((part) => part.trim()));
  return ISSUE_STATUSES.filter((status) => wanted.has(status));
}

export function issueStatusParam(statuses: readonly IssueStatus[]): string | undefined {
  return statuses.length ? ISSUE_STATUSES.filter((s) => statuses.includes(s)).join(",") : undefined;
}

export function issuePlatformOf(param: string | undefined): IssuePlatform | undefined {
  return (ISSUE_PLATFORMS as readonly string[]).includes(param ?? "") ? (param as IssuePlatform) : undefined;
}

export function issueSortOf(param: string | undefined): IssueSort {
  return (ISSUE_SORTS as readonly string[]).includes(param ?? "") ? (param as IssueSort) : "last";
}

/** The URL keys this area owns. */
export interface ObsSearch {
  view?: ObsView;
  range?: RangePreset;
  service?: string;
  /** Graph/explorer: exact full proto service and service/method identities. */
  rpcService?: string;
  rpcId?: string;
  declared?: boolean;
  internal?: boolean;
  severity?: string;
  /** Logs: the query. Traces: the root operation. Errors: title or culprit. Sessions: entry path contains. */
  q?: string;
  trace?: string;
  span?: string;
  profileType?: string;
  live?: boolean;
  /** Traces: only the ones with a failed span. Sessions: only the ones that raised a browser error. */
  errors?: boolean;
  /** Traces: only the ones slower than this ("500ms", "1s"). */
  minDuration?: string;
  /**
   * The project area's environment filter: env NAMES (unique within a
   * project, and readable in a shared link). Absent: every environment. An
   * env page pins its own environment and ignores this.
   */
  envs?: string[];
  /** The org place's app filter: forge app names. Absent: every app. */
  apps?: string[];
  /** Attribute filters, one token each (obsFilters.ts): `service.version:v1.8`. */
  f?: string[];
  /** Up to three keys every aggregating view splits its series by. */
  groupBy?: string[];
  /** Alerts: which part of the area is open. Absent: what is firing. */
  alerts?: AlertsTab;
  /** Alerts → Rules: the rule open in detail, `<env name>/<rule>`. */
  rule?: string;
  /**
   * A release comparison open over the current view: the promotion as
   * `<envId>~<promotedAtMs>~<promotionId>` (deliveryScope.ts compareParam).
   */
  compare?: string;
  /** The comparison's window on each side. Absent: the server's default, 15m. */
  cmpWindow?: HealthWindow;
  /** Errors: the issue open in detail. */
  issue?: string;
  /** Errors: the statuses listed, comma-separated (issueStatusesOf). Absent: the open ones. */
  istatus?: string;
  /** Errors: only browser or only server issues. Absent: both. */
  platform?: IssuePlatform;
  /** Errors: the order. Absent: most recently seen first. */
  isort?: IssueSort;
  /** Errors: `me` or a user id (assigneeFilterOf). Absent: anyone's. */
  assignee?: string;
  /** Sessions: the session open in detail (its rum.sessionId). */
  session?: string;
  /** Sessions: the moment to open the replay at, epoch ms (a link from an error or a trace). */
  t?: number;
  /** Sessions: only the ones with a recorded replay. */
  replay?: boolean;
  /** Sessions: only this user id's. */
  user?: string;
  /**
   * Sessions: the page tokens walked to reach the page shown, oldest first —
   * the last is the page's own. Absent: the first page. A trail, not one
   * token, so Previous is a link too.
   */
  cursor?: string[];
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
  "rpcService",
  "rpcId",
  "declared",
  "internal",
  "severity",
  "q",
  "trace",
  "span",
  "profileType",
  "live",
  "errors",
  "minDuration",
  "envs",
  "apps",
  "f",
  "groupBy",
  "alerts",
  "rule",
  "compare",
  "cmpWindow",
  "issue",
  "istatus",
  "platform",
  "isort",
  "assignee",
  "session",
  "t",
  "replay",
  "user",
  "cursor",
] as const satisfies readonly (keyof ObsSearch)[];

/**
 * What a cross-view link carries. Opening a trace from a log keeps the window
 * and service and drops the log query, which means nothing to the trace list.
 */
export type ObsNavigate = (next: ObsSearch, options?: { replace?: boolean }) => void;

/** Fails to compile if a URL key is added to ObsSearch and not to OBS_SEARCH_KEYS (leaving the tab would keep it). */
type AssertNever<_T extends never> = void;
export type _EveryObsKeyIsCleared = AssertNever<Exclude<keyof ObsSearch, (typeof OBS_SEARCH_KEYS)[number]>>;
