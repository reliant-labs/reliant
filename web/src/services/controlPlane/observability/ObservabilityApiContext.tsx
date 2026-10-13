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

import { getWalletOverview } from "../reliantAI";
import * as alerting from "./alertingApi";
import * as rpc from "./api";
import type { ObsScope, ReleaseMarker, TimeWindow } from "./types";

export interface ObservabilityApi {
  /** The caller's current organization — what every request is scoped by. Null while it is still being provisioned. */
  currentOrgId: () => Promise<string | null>;
  searchLogs: typeof rpc.searchLogs;
  listTraces: typeof rpc.listTraces;
  getTrace: typeof rpc.getTrace;
  queryPanel: typeof rpc.queryPanel;
  getServiceOverview: typeof rpc.getServiceOverview;
  getFlamegraph: typeof rpc.getFlamegraph;
  getDashboards: typeof rpc.getDashboards;
  getObservabilityUsage: typeof rpc.getObservabilityUsage;
  listAttributeValues: typeof rpc.listAttributeValues;
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
  /**
   * Release promotions in the scope, for chart markers (contract K5). Absent
   * until DeliveryInsightsService is wired: dashboards that declare release
   * annotations then say the markers are not available yet.
   */
  listReleaseMarkers?: (req: ObsScope & { window: TimeWindow }) => Promise<ReleaseMarker[]>;
}

export const controlPlaneObservabilityApi: ObservabilityApi = {
  // The org the billing surfaces resolve for the caller (the wallet overview).
  currentOrgId: async () => (await getWalletOverview())?.organization?.id ?? null,
  searchLogs: rpc.searchLogs,
  listTraces: rpc.listTraces,
  getTrace: rpc.getTrace,
  queryPanel: rpc.queryPanel,
  getServiceOverview: rpc.getServiceOverview,
  getFlamegraph: rpc.getFlamegraph,
  getDashboards: rpc.getDashboards,
  getObservabilityUsage: rpc.getObservabilityUsage,
  listAttributeValues: rpc.listAttributeValues,
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
};

const ObservabilityApiContext = createContext<ObservabilityApi>(controlPlaneObservabilityApi);

export function ObservabilityApiProvider({ api, children }: { api: ObservabilityApi; children: ReactNode }) {
  return <ObservabilityApiContext.Provider value={api}>{children}</ObservabilityApiContext.Provider>;
}

export function useObservabilityApi(): ObservabilityApi {
  return useContext(ObservabilityApiContext);
}
