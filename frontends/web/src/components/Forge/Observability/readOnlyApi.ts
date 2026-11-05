// Copyright (c) 2025 Reliant Labs

/**
 * An ObservabilityApi that refuses every write: the demo console's
 * (Forge/Demo), in every shell that mounts it, and later the live demo's
 * (defence in depth; there the server is the authority).
 *
 * Every method of the API is classified exactly once, as a read, a write, or
 * a bookkeeping write. The test asserts the three lists cover the API's keys
 * with no overlap, so a method added to ObservabilityApi without a decision
 * fails it: a new write cannot slip into the demo by default.
 *
 *   READ_METHODS     passed through.
 *   WRITE_METHODS    throw PermissionDenied with the demo's reason; the
 *                    controls that call them are disabled first
 *                    (obsReadOnly.tsx), so the throw is the backstop.
 *   QUIET_WRITES     bookkeeping a page does on its own (a dashboard view, a
 *                    notification marked read): answered as a no-op rather than
 *                    an error the visitor never asked for.
 */

import { Code, ConnectError } from "@connectrpc/connect";

import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";

type Method = keyof ObservabilityApi;

export const DEMO_READ_ONLY_REASON = "This is a demo. Changes are turned off.";

export const READ_METHODS = [
  "currentOrgId",
  "searchLogs",
  "listTraces",
  "getTrace",
  "queryPanel",
  "getServiceOverview",
  "getServiceGraph",
  "getServiceGraphCatalog",
  "getFlamegraph",
  "getServiceSignals",
  "getObservabilityUsage",
  "listAttributeValues",
  "listMetricCatalog",
  "getMetric",
  "listMetricExemplars",
  "listWorkloads",
  "getWorkload",
  "listAlerts",
  "listAlertHistory",
  "listSilences",
  "listReceivers",
  "listNotifications",
  "listReleases",
  "getDoraMetrics",
  "getReleaseHealth",
  "listIssues",
  "getIssue",
  "listIssueEvents",
  "listIssueTimeline",
  "listOrgMembers",
  "getCurrentIssueUser",
  "listActivity",
  "listSessions",
  "getSession",
  "getSessionTimeline",
  "getSessionReplay",
  "listDashboards",
  "listDashboardTemplates",
  "getDashboard",
  "listDashboardVersions",
  "queryDatabasePanel",
  "listDashboardDatabases",
] as const satisfies readonly Method[];

export const WRITE_METHODS = [
  "createSilence",
  "expireSilence",
  "createReceiver",
  "updateReceiver",
  "deleteReceiver",
  "testReceiver",
  "updateIssueStatus",
  "addIssueComment",
  "updateIssueComment",
  "deleteIssueComment",
  "assignIssue",
  "linkIssueChat",
  "saveDashboard",
  "saveDashboardAsCopy",
  "restoreDashboardVersion",
  "archiveDashboard",
  "starDashboard",
  "promoteDashboardToCode",
] as const satisfies readonly Method[];

export const QUIET_WRITES = {
  recordDashboardView: async () => undefined,
  markNotificationsRead: async () => 0,
} satisfies Partial<ObservabilityApi>;

/** `api` with every write refused (or, for bookkeeping, answered as a no-op). */
export function withReadOnly(api: ObservabilityApi, reason: string = DEMO_READ_ONLY_REASON): ObservabilityApi {
  const refuse = async () => {
    throw new ConnectError(reason, Code.PermissionDenied);
  };
  const out = { ...api, ...QUIET_WRITES } as Record<Method, unknown>;
  for (const method of WRITE_METHODS) out[method] = refuse;
  return out as unknown as ObservabilityApi;
}
