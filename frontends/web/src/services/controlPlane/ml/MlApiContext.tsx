// Copyright (c) 2026 Reliant Labs

/**
 * The seam between the ML tab (components/Forge/Ml) and the calls it makes.
 *
 * The same shape as TestApiContext: component tests hand it an in-memory
 * api, and production never provides anything but the real client
 * (mlApi.ts), which calls the control plane with the user's session. The
 * default is that client on the nearest TransportScope's transport (the demo
 * shell's fixture pack), else on the app's.
 */

import { createContext, useContext, type ReactNode } from "react";
import type { Transport } from "@connectrpc/connect";

import { transport as appTransport } from "@/lib/connect";
import { useTransport } from "@/lib/transportScope";

import * as ml from "./mlApi";
import type {
  CallOptions,
  EntitlementRequestResult,
  MlClusterRequest,
  MlFinding,
  MlInferenceService,
  MlMachines,
  MlModel,
  MlOutcome,
  MlProjectRequest,
  MlRun,
  MlStackComponent,
  OrgEntitlement,
} from "./types";

export interface MlApi {
  getMyOrgEntitlements(orgId: string, opts?: CallOptions): Promise<OrgEntitlement[]>;
  requestOrgEntitlement(orgId: string, feature: string, note?: string): Promise<EntitlementRequestResult>;
  getMachines(req: MlClusterRequest, opts?: CallOptions): Promise<MlOutcome<MlMachines>>;
  getStack(req: MlClusterRequest, opts?: CallOptions): Promise<MlOutcome<MlStackComponent[]>>;
  listFindings(req: MlClusterRequest, opts?: CallOptions): Promise<MlOutcome<MlFinding[]>>;
  listRuns(req: MlProjectRequest, opts?: CallOptions): Promise<MlOutcome<MlRun[]>>;
  listModels(req: MlProjectRequest, opts?: CallOptions): Promise<MlOutcome<MlModel[]>>;
  listInferenceServices(req: MlProjectRequest, opts?: CallOptions): Promise<MlOutcome<MlInferenceService[]>>;
}

/** The real client on `transport`: a TransportScope's, or undefined for the app's. */
function apiOn(transport: Transport | undefined): MlApi {
  return {
    getMyOrgEntitlements: (orgId, opts) => ml.getMyOrgEntitlements(orgId, opts, transport),
    requestOrgEntitlement: (orgId, feature, note) => ml.requestOrgEntitlement(orgId, feature, note, transport),
    getMachines: (req, opts) => ml.getMachines(req, opts, transport),
    getStack: (req, opts) => ml.getStack(req, opts, transport),
    listFindings: (req, opts) => ml.listFindings(req, opts, transport),
    listRuns: (req, opts) => ml.listRuns(req, opts, transport),
    listModels: (req, opts) => ml.listModels(req, opts, transport),
    listInferenceServices: (req, opts) => ml.listInferenceServices(req, opts, transport),
  };
}

/** The real client on the app's transport (lib/connect.ts): production's, outside every scope. */
export const controlPlaneMlApi: MlApi = apiOn(undefined);

const scopedApis = new WeakMap<Transport, MlApi>();

/** The real client on `transport`, memoized per transport so a context default keeps its identity. */
export function mlApiOf(transport: Transport): MlApi {
  if (transport === appTransport) return controlPlaneMlApi;
  let api = scopedApis.get(transport);
  if (!api) {
    api = apiOn(transport);
    scopedApis.set(transport, api);
  }
  return api;
}

const MlApiContext = createContext<MlApi | null>(null);

export function MlApiProvider({ api, children }: { api: MlApi; children: ReactNode }) {
  return <MlApiContext.Provider value={api}>{children}</MlApiContext.Provider>;
}

/** The provided api, else the real client on the nearest TransportScope's transport, else on the app's. */
export function useMlApi(): MlApi {
  const provided = useContext(MlApiContext);
  const transport = useTransport(appTransport);
  return provided ?? mlApiOf(transport);
}
