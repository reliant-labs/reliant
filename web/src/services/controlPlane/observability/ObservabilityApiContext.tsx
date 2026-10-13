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
import * as rpc from "./api";

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
};

const ObservabilityApiContext = createContext<ObservabilityApi>(controlPlaneObservabilityApi);

export function ObservabilityApiProvider({ api, children }: { api: ObservabilityApi; children: ReactNode }) {
  return <ObservabilityApiContext.Provider value={api}>{children}</ObservabilityApiContext.Provider>;
}

export function useObservabilityApi(): ObservabilityApi {
  return useContext(ObservabilityApiContext);
}
