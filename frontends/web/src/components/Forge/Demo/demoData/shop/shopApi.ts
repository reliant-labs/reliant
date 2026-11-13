// Copyright (c) 2025 Reliant Labs

/**
 * The demo's ObservabilityApi: shop's story (shopStory.ts) answered for every
 * read the console's observability views make, one module per area. The demo
 * console (demoApi.ts) and the wire scenario (demoScenario.ts) both read it,
 * so the two can never disagree.
 *
 * Writes never reach it: the read-only layer (Observability/readOnlyApi.ts)
 * and the transport (demoTransport.ts) refuse them first. A module's write
 * methods therefore only refuse (`demoWriteRefused`).
 */

import { Code, ConnectError } from "@connectrpc/connect";

import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";

import { DEMO_ORG, demoNow } from "../shopStory";
import { shopAlerting } from "./alerting";
import { shopDashboards } from "./dashboards";
import { shopDelivery } from "./delivery";
import { shopGraph } from "./graph";
import { shopIssues } from "./issues";
import { shopMetrics } from "./metrics";
import { shopPanels } from "./panels";
import { shopSessions } from "./sessions";
import { shopTelemetry } from "./telemetry";
import { shopWorkloads } from "./workloads";

/** The keys each module answers: together, every ObservabilityApi read, each exactly once. */
export type TelemetryKeys = "searchLogs" | "listTraces" | "getTrace" | "getServiceOverview" | "getFlamegraph" | "getServiceSignals";
export type GraphKeys = "getServiceGraph" | "getServiceGraphCatalog";
export type WorkloadKeys = "listWorkloads" | "getWorkload";
export type PanelKeys = "queryPanel" | "listAttributeValues" | "getObservabilityUsage";
export type DeliveryKeys = "listReleases" | "getDoraMetrics";
export type IssueKeys =
  | "listIssues"
  | "getIssue"
  | "listIssueEvents"
  | "updateIssueStatus"
  | "listIssueTimeline"
  | "addIssueComment"
  | "updateIssueComment"
  | "deleteIssueComment"
  | "assignIssue"
  | "linkIssueChat"
  | "listOrgMembers"
  | "getCurrentIssueUser"
  | "getReleaseHealth"
  | "listActivity";
export type AlertingKeys =
  | "listAlerts"
  | "listAlertHistory"
  | "listSilences"
  | "createSilence"
  | "expireSilence"
  | "listReceivers"
  | "createReceiver"
  | "updateReceiver"
  | "deleteReceiver"
  | "testReceiver"
  | "listNotifications"
  | "markNotificationsRead";
export type SessionKeys = "listSessions" | "getSession" | "getSessionTimeline" | "getSessionReplay" | "listFrontendReplay";
export type DashboardKeys =
  | "listDashboards"
  | "listDashboardTemplates"
  | "getDashboard"
  | "saveDashboard"
  | "saveDashboardAsCopy"
  | "listDashboardVersions"
  | "restoreDashboardVersion"
  | "archiveDashboard"
  | "starDashboard"
  | "recordDashboardView"
  | "promoteDashboardToCode"
  | "queryDatabasePanel"
  | "listDashboardDatabases";
export type MetricKeys = "listMetricCatalog" | "getMetric" | "listMetricExemplars";

/** What a write method answers: the demo changes nothing. */
export const demoWriteRefused = async (): Promise<never> => {
  throw new ConnectError("This is a demo. Changes are turned off.", Code.PermissionDenied);
};

/** shop's observability, as of `now` (the page load). */
export function createShopApi(now: number = demoNow()): ObservabilityApi {
  return {
    currentOrgId: async () => DEMO_ORG,
    ...shopTelemetry(now),
    ...shopGraph(now),
    ...shopWorkloads(now),
    ...shopPanels(now),
    ...shopDelivery(now),
    ...shopIssues(now),
    ...shopAlerting(now),
    ...shopSessions(now),
    ...shopDashboards(now),
    ...shopMetrics(now),
  };
}
