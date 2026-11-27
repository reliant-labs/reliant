// Copyright (c) 2025 Reliant Labs

/**
 * The seam between the observability views and the RPCs they read.
 *
 * Every hook in hooks/observability-queries.ts calls through this context, so
 * the area can render against something other than control-plane: component
 * tests hand it an in-memory fake. The default is the real client, on the
 * nearest TransportScope's transport when there is one (the console over a
 * fixture pack) and on the app's otherwise; production never provides
 * anything else, and neither do the dev-only previews
 * (/forge-observability-preview, …), which run the real client over the shop
 * fixture pack's transport.
 */

import { createContext, useContext, type ReactNode } from "react";
import type { Transport } from "@connectrpc/connect";

import { transport as appTransport } from "@/lib/connect";
import { useTransport } from "@/lib/transportScope";

import { currentOrgId } from "../orgs";
import * as activity from "./activityApi";
import * as alerting from "./alertingApi";
import * as appDatabase from "./appDatabaseApi";
import * as rpc from "./api";
import * as frontendReplay from "./frontendReplayApi";
import * as graph from "./graphApi";
import * as dashboards from "./dashboardsApi";
import * as delivery from "./deliveryApi";
import * as issues from "./issuesApi";
import * as members from "./membersApi";
import * as metrics from "./metricsApi";
import * as rum from "./rumApi";

export interface ObservabilityApi {
  /** The caller's current organization — what every request is scoped by. Null while it is still being provisioned. */
  currentOrgId: () => Promise<string | null>;
  searchLogs: typeof rpc.searchLogs;
  listTraces: typeof rpc.listTraces;
  getTrace: typeof rpc.getTrace;
  queryPanel: typeof rpc.queryPanel;
  getServiceOverview: typeof rpc.getServiceOverview;
  getServiceGraph: typeof graph.getServiceGraph;
  getServiceGraphCatalog: typeof graph.getServiceGraphCatalog;
  getFlamegraph: typeof rpc.getFlamegraph;
  getServiceSignals: typeof rpc.getServiceSignals;
  getObservabilityUsage: typeof rpc.getObservabilityUsage;
  listAttributeValues: typeof rpc.listAttributeValues;
  // Metrics explorer: the catalog, one metric's detail, and its exemplars.
  listMetricCatalog: typeof metrics.listMetricCatalog;
  getMetric: typeof metrics.getMetric;
  listMetricExemplars: typeof metrics.listMetricExemplars;
  listWorkloads: typeof rpc.listWorkloads;
  getWorkload: typeof rpc.getWorkload;
  // Alerting (AlertingService): org-scoped, mutations need admin.
  listAlerts: typeof alerting.listAlerts;
  listAlertHistory: typeof alerting.listAlertHistory;
  listSilences: typeof alerting.listSilences;
  createSilence: typeof alerting.createSilence;
  expireSilence: typeof alerting.expireSilence;
  listReceivers: typeof alerting.listReceivers;
  createReceiver: typeof alerting.createReceiver;
  updateReceiver: typeof alerting.updateReceiver;
  deleteReceiver: typeof alerting.deleteReceiver;
  testReceiver: typeof alerting.testReceiver;
  listNotifications: typeof alerting.listNotifications;
  markNotificationsRead: typeof alerting.markNotificationsRead;
  // Delivery (DeliveryInsightsService, contract K5): the release ledger and DORA.
  listReleases: typeof delivery.listReleases;
  getDoraMetrics: typeof delivery.getDoraMetrics;
  // Release health (ObservabilityService.GetReleaseHealth): the server's verdict on one promotion.
  getReleaseHealth: typeof delivery.getReleaseHealth;
  // Issues (IssuesService): the error inbox, browser and server; triage needs obs:triage.
  listIssues: typeof issues.listIssues;
  getIssue: typeof issues.getIssue;
  listIssueEvents: typeof issues.listIssueEvents;
  updateIssueStatus: typeof issues.updateIssueStatus;
  // Discussion on an issue: comments, the assignee, chats started from it, the one timeline.
  listIssueTimeline: typeof issues.listIssueTimeline;
  addIssueComment: typeof issues.addIssueComment;
  updateIssueComment: typeof issues.updateIssueComment;
  deleteIssueComment: typeof issues.deleteIssueComment;
  assignIssue: typeof issues.assignIssue;
  linkIssueChat: typeof issues.linkIssueChat;
  // People: who an issue can be assigned to (token:read to list), and the reader.
  listOrgMembers: typeof members.listOrgMembers;
  getCurrentIssueUser: typeof members.getCurrentIssueUser;
  // Activity (ActivityService): who changed what — an issue's history, the project feed.
  listActivity: typeof activity.listActivity;
  // Sessions (RumService): real-user sessions, their timelines and masked replays.
  listSessions: typeof rum.listSessions;
  getSession: typeof rum.getSession;
  getSessionTimeline: typeof rum.getSessionTimeline;
  getSessionReplay: typeof rum.getSessionReplay;
  // Session replay per frontend (ObservabilityService.ListFrontendReplay): each frontend's live switch.
  listFrontendReplay: typeof frontendReplay.listFrontendReplay;
  // Native dashboards (DashboardService): reads need membership,
  // authoring the admin role.
  listDashboards: typeof dashboards.listDashboards;
  listDashboardTemplates: typeof dashboards.listDashboardTemplates;
  getDashboard: typeof dashboards.getDashboard;
  saveDashboard: typeof dashboards.saveDashboard;
  saveDashboardAsCopy: typeof dashboards.saveDashboardAsCopy;
  listDashboardVersions: typeof dashboards.listDashboardVersions;
  restoreDashboardVersion: typeof dashboards.restoreDashboardVersion;
  archiveDashboard: typeof dashboards.archiveDashboard;
  starDashboard: typeof dashboards.starDashboard;
  recordDashboardView: typeof dashboards.recordDashboardView;
  promoteDashboardToCode: typeof dashboards.promoteDashboardToCode;
  // Postgres panels (AppDatabaseService, C3): one environment's database, read live.
  queryDatabasePanel: typeof appDatabase.queryDatabasePanel;
  listDashboardDatabases: typeof appDatabase.listDashboardDatabases;
}

/**
 * The real client on `transport`: a TransportScope's (lib/transportScope.tsx),
 * or undefined for the app's. Each method hands it to its RPC function, which
 * passes it to getControlPlaneClient. A scope's transport skips the app's
 * observability read stage (obsInterceptors.ts): no read queue, no quota guard.
 */
function apiOn(transport: Transport | undefined): ObservabilityApi {
  return {
    // The active org (the switcher's choice, else the person's default): the app's, in a scope too.
    currentOrgId,
    searchLogs: (req, options) => rpc.searchLogs(req, options, transport),
    listTraces: (req, options) => rpc.listTraces(req, options, transport),
    getTrace: (req, options) => rpc.getTrace(req, options, transport),
    queryPanel: (req, options) => rpc.queryPanel(req, options, transport),
    getServiceOverview: (req, options) => rpc.getServiceOverview(req, options, transport),
    getServiceGraph: (req, options) => graph.getServiceGraph(req, options, transport),
    getServiceGraphCatalog: (req, options) => graph.getServiceGraphCatalog(req, options, transport),
    getFlamegraph: (req, options) => rpc.getFlamegraph(req, options, transport),
    getServiceSignals: (req, options) => rpc.getServiceSignals(req, options, transport),
    getObservabilityUsage: (orgId) => rpc.getObservabilityUsage(orgId, transport),
    listAttributeValues: (req, options) => rpc.listAttributeValues(req, options, transport),
    listMetricCatalog: (req, options) => metrics.listMetricCatalog(req, options, transport),
    getMetric: (req, options) => metrics.getMetric(req, options, transport),
    listMetricExemplars: (req, options) => metrics.listMetricExemplars(req, options, transport),
    listWorkloads: (req, options) => rpc.listWorkloads(req, options, transport),
    getWorkload: (req, options) => rpc.getWorkload(req, options, transport),
    listAlerts: (orgId, envId) => alerting.listAlerts(orgId, envId, transport),
    listAlertHistory: (orgId, opts) => alerting.listAlertHistory(orgId, opts, transport),
    listSilences: (orgId, includeExpired) => alerting.listSilences(orgId, includeExpired, transport),
    createSilence: (orgId, input) => alerting.createSilence(orgId, input, transport),
    expireSilence: (orgId, id) => alerting.expireSilence(orgId, id, transport),
    listReceivers: (orgId) => alerting.listReceivers(orgId, transport),
    createReceiver: (orgId, input) => alerting.createReceiver(orgId, input, transport),
    updateReceiver: (orgId, input) => alerting.updateReceiver(orgId, input, transport),
    deleteReceiver: (orgId, id) => alerting.deleteReceiver(orgId, id, transport),
    testReceiver: (orgId, id) => alerting.testReceiver(orgId, id, transport),
    listNotifications: (orgId, opts) => alerting.listNotifications(orgId, opts, transport),
    markNotificationsRead: (orgId, ids) => alerting.markNotificationsRead(orgId, ids, transport),
    listReleases: (req) => delivery.listReleases(req, transport),
    getDoraMetrics: (req) => delivery.getDoraMetrics(req, transport),
    getReleaseHealth: (req, options) => delivery.getReleaseHealth(req, options, transport),
    listIssues: (req, options) => issues.listIssues(req, options, transport),
    getIssue: (req, options) => issues.getIssue(req, options, transport),
    listIssueEvents: (req, options) => issues.listIssueEvents(req, options, transport),
    updateIssueStatus: (req) => issues.updateIssueStatus(req, transport),
    listIssueTimeline: (req) => issues.listIssueTimeline(req, transport),
    addIssueComment: (req) => issues.addIssueComment(req, transport),
    updateIssueComment: (req) => issues.updateIssueComment(req, transport),
    deleteIssueComment: (req) => issues.deleteIssueComment(req, transport),
    assignIssue: (req) => issues.assignIssue(req, transport),
    linkIssueChat: (req) => issues.linkIssueChat(req, transport),
    listOrgMembers: (orgId) => members.listOrgMembers(orgId, transport),
    getCurrentIssueUser: () => members.getCurrentIssueUser(transport),
    listActivity: (req) => activity.listActivity(req, transport),
    listSessions: (req, options) => rum.listSessions(req, options, transport),
    getSession: (req, options) => rum.getSession(req, options, transport),
    getSessionTimeline: (req, options) => rum.getSessionTimeline(req, options, transport),
    getSessionReplay: (req, options) => rum.getSessionReplay(req, options, transport),
    listFrontendReplay: (req, options) => frontendReplay.listFrontendReplay(req, options, transport),
    listDashboards: (req) => dashboards.listDashboards(req, transport),
    listDashboardTemplates: (orgId) => dashboards.listDashboardTemplates(orgId, transport),
    getDashboard: (req) => dashboards.getDashboard(req, transport),
    saveDashboard: (req) => dashboards.saveDashboard(req, transport),
    saveDashboardAsCopy: (req) => dashboards.saveDashboardAsCopy(req, transport),
    listDashboardVersions: (orgId, ref) => dashboards.listDashboardVersions(orgId, ref, transport),
    restoreDashboardVersion: (orgId, ref, version, baseVersion) => dashboards.restoreDashboardVersion(orgId, ref, version, baseVersion, transport),
    archiveDashboard: (orgId, ref) => dashboards.archiveDashboard(orgId, ref, transport),
    starDashboard: (orgId, ref, starred) => dashboards.starDashboard(orgId, ref, starred, transport),
    recordDashboardView: (orgId, ref) => dashboards.recordDashboardView(orgId, ref, transport),
    promoteDashboardToCode: (orgId, ref, version) => dashboards.promoteDashboardToCode(orgId, ref, version, transport),
    queryDatabasePanel: (req, options) => appDatabase.queryDatabasePanel(req, options, transport),
    listDashboardDatabases: (environmentId, options) => appDatabase.listDashboardDatabases(environmentId, options, transport),
  };
}

/** The real client on the app's transport (lib/connect.ts): production's, outside every scope. */
export const controlPlaneObservabilityApi: ObservabilityApi = apiOn(undefined);

const scopedApis = new WeakMap<Transport, ObservabilityApi>();

/**
 * The real client on `transport`, memoized per transport so a context default
 * keeps its identity across renders. The app's transport is
 * controlPlaneObservabilityApi itself.
 */
export function observabilityApiOf(transport: Transport): ObservabilityApi {
  if (transport === appTransport) return controlPlaneObservabilityApi;
  let api = scopedApis.get(transport);
  if (!api) {
    api = apiOn(transport);
    scopedApis.set(transport, api);
  }
  return api;
}

const ObservabilityApiContext = createContext<ObservabilityApi | null>(null);

export function ObservabilityApiProvider({ api, children }: { api: ObservabilityApi; children: ReactNode }) {
  return <ObservabilityApiContext.Provider value={api}>{children}</ObservabilityApiContext.Provider>;
}

/**
 * The ObservabilityService reads the views make through its generated hooks
 * (hooks/controlplane/observability-service-hooks_gen.ts; the paged logs and
 * traces reads over its client, as scopedClient resolves it), which call the
 * QueryClient's transport: a TransportScope's, else the app's. The context does
 * not hand them out. They stay on ObservabilityApi only as the model-level
 * contract the shop fixture pack and test fakes implement; its wire handlers
 * (fixtures/shop/handlers/observability.ts) serve them to the hooks.
 */
export type GeneratedObservabilityReads =
  | "searchLogs"
  | "listTraces"
  | "queryPanel"
  | "getServiceGraph"
  | "getServiceGraphCatalog"
  | "getServiceOverview"
  | "getTrace"
  | "getFlamegraph"
  | "getServiceSignals"
  | "listAttributeValues"
  | "listMetricCatalog"
  | "getMetric"
  | "listMetricExemplars"
  | "listWorkloads"
  | "getWorkload"
  | "getReleaseHealth"
  | "listFrontendReplay";

/**
 * AlertingService: every read and write runs on its generated hooks
 * (hooks/observability-queries.ts, hooks/controlplane/alerting-service-hooks_gen.ts),
 * so the context hands none of it out. It stays on ObservabilityApi as the
 * model-level contract the shop fixture pack and test fakes implement; its wire
 * handlers (fixtures/shop/handlers/alerting.ts) serve them to the hooks.
 */
export type GeneratedAlertingCalls =
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

/**
 * IssuesService's calls, which the views make through its generated hooks
 * (hooks/controlplane/issues-service-hooks_gen.ts) and imperative writes; like
 * GeneratedObservabilityReads, on ObservabilityApi only as the model-level
 * contract the shop pack's and the fakes' handlers (fixtures/shop/handlers/issues.ts) serve.
 */
export type GeneratedIssuesCalls =
  | "listIssues"
  | "getIssue"
  | "listIssueEvents"
  | "listIssueTimeline"
  | "updateIssueStatus"
  | "addIssueComment"
  | "updateIssueComment"
  | "deleteIssueComment"
  | "assignIssue"
  | "linkIssueChat";

/** What a view may call through the context: everything but the calls on generated hooks. */
export type ObservabilityContextApi = Omit<ObservabilityApi, GeneratedObservabilityReads | GeneratedAlertingCalls | GeneratedIssuesCalls>;

/** The provided api, else the real client on the nearest TransportScope's transport, else on the app's. */
export function useObservabilityApi(): ObservabilityContextApi {
  const provided = useContext(ObservabilityApiContext);
  const transport = useTransport(appTransport);
  return provided ?? observabilityApiOf(transport);
}
