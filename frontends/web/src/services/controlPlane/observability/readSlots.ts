// Copyright (c) 2025 Reliant Labs

/**
 * Which observability RPCs are ClickHouse reads. A leaf module, importing
 * nothing, so both the read queue (obsInterceptors.ts) and the transport's deadlines
 * (api/transport.ts) read the one list without an import cycle.
 */

/** The services whose reads spend the org's ClickHouse query quota. */
export const READ_SLOT_SERVICES: ReadonlySet<string> = new Set([
  "controlplane.v1.ObservabilityService",
  "controlplane.v1.IssuesService",
  "controlplane.v1.RumService",
]);

/**
 * The RPCs that take a server read slot (obsread.Open on the console surface).
 * Mutations and the Postgres-only issue reads do not: they
 * must never wait behind a queue of ClickHouse reads. obsInterceptors.test.ts fails
 * when a method of these services is in neither this set nor NO_READ_SLOT_RPCS,
 * so a new RPC must be classified.
 */
export const READ_SLOT_RPCS: ReadonlySet<string> = new Set([
  // ObservabilityService
  "searchLogs",
  "listTraces",
  "getTrace",
  "listMetrics",
  "getMetric",
  "listMetricExemplars",
  "getServiceGraph",
  "getServiceGraphCatalog",
  "getServiceOverview",
  "getFlamegraph",
  "queryPanel",
  "listAttributeValues",
  "getServiceSignals",
  "listWorkloads",
  "getWorkload",
  "getDashboards",
  "getReleaseHealth",
  // A ClickHouse read over the env's user events (control-plane
  // rpc_get_user_activity.go opens a read slot); the web does not call it yet.
  "getUserActivity",
  // Both open a read slot (control-plane rpc_export_pgo_profile.go,
  // rpc_get_managed_database_metrics.go); the web does not call them yet.
  "exportPGOProfile",
  "getManagedDatabaseMetrics",
  // IssuesService
  "listIssues",
  "getIssue",
  "listIssueEvents",
  // RumService
  "listSessions",
  "getSession",
  "getSessionTimeline",
  "getSessionReplay",
]);

/** The methods of the services above that take no read slot. */
export const NO_READ_SLOT_RPCS: ReadonlySet<string> = new Set([
  // Postgres, Kubernetes (runtime configs and the release ledger) and the
  // hub's StaticSites; no ClickHouse query.
  "listFrontendReplay",
  "updateIssueStatus",
  "addIssueComment",
  "updateIssueComment",
  "deleteIssueComment",
  "assignIssue",
  "linkIssueChat",
  "listAssignableUsers",
  "listIssueTimeline",
  // Postgres only: control-plane admits these without opening a read slot.
  "declareMaintenanceWindow",
  "listMaintenanceWindows",
]);

/** Whether `<serviceTypeName>/<MethodName>` (the wire names) is a ClickHouse read. */
export function isReadSlotProcedure(serviceTypeName: string, methodName: string): boolean {
  return READ_SLOT_SERVICES.has(serviceTypeName) && READ_SLOT_RPCS.has(methodName.charAt(0).toLowerCase() + methodName.slice(1));
}
