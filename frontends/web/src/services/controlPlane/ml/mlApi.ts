// Copyright (c) 2026 Reliant Labs

/**
 * The ML tab's calls, as its pages make them (MlApiContext.tsx is the seam):
 *
 *   getMyOrgEntitlements   OrganizationService: whether the org has "ml", and whether someone asked
 *   requestOrgEntitlement  OrganizationService: ask Reliant for a feature (idempotent per org)
 *   getMachines            MlEarlyAccessService.GetMlMachines
 *   getStack               MlEarlyAccessService.GetMlStack
 *   listFindings           MlEarlyAccessService.ListMlFindings
 *   listRuns               MlEarlyAccessService.ListMlRuns
 *   listModels             MlEarlyAccessService.ListMlModels
 *   listInferenceServices  MlEarlyAccessService.ListMlInferenceServices
 *
 * Each is one call over the generated client (proto-vendor/controlplane),
 * mapped by wire.ts. An ML read resolves to an MlOutcome: the data, "not
 * entitled" (FEATURE_NOT_ENTITLED) or "no data yet" (Unimplemented, also this
 * client's answer with no control plane configured); anything else rejects.
 * The entitlement calls reject on every failure: the gate decides what each
 * one means.
 */

import { Code, ConnectError, type Client, type Transport } from "@connectrpc/connect";
import type { DescService } from "@bufbuild/protobuf";

import { MlEarlyAccessService } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { OrganizationService } from "@/gen/controlplane/services/organization/v1/organization_pb";
import { transport as appTransport } from "@/lib/connect";

import { getControlPlaneClient } from "../client";
import { CONTROL_PLANE_API_URL } from "../config";
import { outcomeOfError } from "./errors";
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
import { entitlementsOf, findingsOf, inferenceOf, machinesOf, modelsOf, requestResultOf, runsOf, stackOf } from "./wire";

/**
 * `transport` is the caller's TransportScope's (lib/transportScope.tsx). A
 * scope's own transport (a fixture pack's) needs no control plane; none, or
 * the app's, is today's client.
 */
function client<T extends DescService>(service: T, transport?: Transport): Client<T> {
  const scoped = transport !== undefined && transport !== appTransport;
  if (!scoped && !CONTROL_PLANE_API_URL) throw new ConnectError("no control plane is configured", Code.Unimplemented);
  return getControlPlaneClient(service, transport);
}

async function outcome<T>(read: () => Promise<T>): Promise<MlOutcome<T>> {
  try {
    return { kind: "ok", data: await read() };
  } catch (error) {
    return outcomeOfError<T>(error);
  }
}

export async function getMyOrgEntitlements(orgId: string, opts?: CallOptions, transport?: Transport): Promise<OrgEntitlement[]> {
  return entitlementsOf(await client(OrganizationService, transport).getMyOrgEntitlements({ orgId }, opts));
}

export async function requestOrgEntitlement(orgId: string, feature: string, note?: string, transport?: Transport): Promise<EntitlementRequestResult> {
  const res = await client(OrganizationService, transport).requestOrgEntitlement({ orgId, feature, note: note ?? "" });
  return requestResultOf(res, feature, Date.now());
}

export function getMachines(req: MlClusterRequest, opts?: CallOptions, transport?: Transport): Promise<MlOutcome<MlMachines>> {
  return outcome(async () => machinesOf(await client(MlEarlyAccessService, transport).getMlMachines(req, opts)));
}

export function getStack(req: MlClusterRequest, opts?: CallOptions, transport?: Transport): Promise<MlOutcome<MlStackComponent[]>> {
  return outcome(async () => stackOf(await client(MlEarlyAccessService, transport).getMlStack(req, opts)));
}

export function listFindings(req: MlClusterRequest, opts?: CallOptions, transport?: Transport): Promise<MlOutcome<MlFinding[]>> {
  return outcome(async () => findingsOf(await client(MlEarlyAccessService, transport).listMlFindings(req, opts)));
}

export function listRuns(req: MlProjectRequest, opts?: CallOptions, transport?: Transport): Promise<MlOutcome<MlRun[]>> {
  return outcome(async () => runsOf(await client(MlEarlyAccessService, transport).listMlRuns(req, opts)));
}

export function listModels(req: MlProjectRequest, opts?: CallOptions, transport?: Transport): Promise<MlOutcome<MlModel[]>> {
  return outcome(async () => modelsOf(await client(MlEarlyAccessService, transport).listMlModels(req, opts)));
}

export function listInferenceServices(req: MlProjectRequest, opts?: CallOptions, transport?: Transport): Promise<MlOutcome<MlInferenceService[]>> {
  return outcome(async () => inferenceOf(await client(MlEarlyAccessService, transport).listMlInferenceServices(req, opts)));
}
