// Copyright (c) 2025 Reliant Labs

/**
 * The seam between the observability views and the RPCs they read.
 *
 * Every hook in hooks/observability-queries.ts calls through this context, so
 * the area can render against something other than control-plane: component
 * tests hand it an in-memory fake, and the dev-only preview route
 * (/forge-observability-preview) hands it fixtures, which is what makes the
 * screens lookable-at and Playwright-testable without a backend. The default
 * is the real client; production never provides anything else.
 */

import { createContext, useContext, type ReactNode } from "react";

import { currentOrgId } from "../orgs";
import * as activity from "./activityApi";
import * as alerting from "./alertingApi";
import * as rpc from "./api";
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
}

export const controlPlaneObservabilityApi: ObservabilityApi = {
  // The active org (the switcher's choice, else the person's default).
  currentOrgId,
  searchLogs: rpc.searchLogs,
  listTraces: rpc.listTraces,
  getTrace: rpc.getTrace,
  queryPanel: rpc.queryPanel,
  getServiceOverview: rpc.getServiceOverview,
  getServiceGraph: graph.getServiceGraph,
  getServiceGraphCatalog: graph.getServiceGraphCatalog,
  getFlamegraph: rpc.getFlamegraph,
  getServiceSignals: rpc.getServiceSignals,
  getObservabilityUsage: rpc.getObservabilityUsage,
  listAttributeValues: rpc.listAttributeValues,
  listMetricCatalog: metrics.listMetricCatalog,
  getMetric: metrics.getMetric,
  listMetricExemplars: metrics.listMetricExemplars,
  listWorkloads: rpc.listWorkloads,
  getWorkload: rpc.getWorkload,
  listAlerts: alerting.listAlerts,
  listAlertHistory: alerting.listAlertHistory,
  listSilences: alerting.listSilences,
  createSilence: alerting.createSilence,
  expireSilence: alerting.expireSilence,
  listReceivers: alerting.listReceivers,
  createReceiver: alerting.createReceiver,
  updateReceiver: alerting.updateReceiver,
  deleteReceiver: alerting.deleteReceiver,
  testReceiver: alerting.testReceiver,
  listNotifications: alerting.listNotifications,
  markNotificationsRead: alerting.markNotificationsRead,
  listReleases: delivery.listReleases,
  getDoraMetrics: delivery.getDoraMetrics,
  getReleaseHealth: delivery.getReleaseHealth,
  listIssues: issues.listIssues,
  getIssue: issues.getIssue,
  listIssueEvents: issues.listIssueEvents,
  updateIssueStatus: issues.updateIssueStatus,
  listIssueTimeline: issues.listIssueTimeline,
  addIssueComment: issues.addIssueComment,
  updateIssueComment: issues.updateIssueComment,
  deleteIssueComment: issues.deleteIssueComment,
  assignIssue: issues.assignIssue,
  linkIssueChat: issues.linkIssueChat,
  listOrgMembers: members.listOrgMembers,
  getCurrentIssueUser: members.getCurrentIssueUser,
  listActivity: activity.listActivity,
  listSessions: rum.listSessions,
  getSession: rum.getSession,
  getSessionTimeline: rum.getSessionTimeline,
  getSessionReplay: rum.getSessionReplay,
  listDashboards: dashboards.listDashboards,
  listDashboardTemplates: dashboards.listDashboardTemplates,
  getDashboard: dashboards.getDashboard,
  saveDashboard: dashboards.saveDashboard,
  saveDashboardAsCopy: dashboards.saveDashboardAsCopy,
  listDashboardVersions: dashboards.listDashboardVersions,
  restoreDashboardVersion: dashboards.restoreDashboardVersion,
  archiveDashboard: dashboards.archiveDashboard,
  starDashboard: dashboards.starDashboard,
  recordDashboardView: dashboards.recordDashboardView,
  promoteDashboardToCode: dashboards.promoteDashboardToCode,
};

const ObservabilityApiContext = createContext<ObservabilityApi>(controlPlaneObservabilityApi);

export function ObservabilityApiProvider({ api, children }: { api: ObservabilityApi; children: ReactNode }) {
  return <ObservabilityApiContext.Provider value={api}>{children}</ObservabilityApiContext.Provider>;
}

export function useObservabilityApi(): ObservabilityApi {
  return useContext(ObservabilityApiContext);
}
