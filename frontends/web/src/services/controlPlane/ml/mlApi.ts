// Copyright (c) 2026 Reliant Labs

/**
 * The ML tab's calls, as its pages make them (MlApiContext.tsx is the seam):
 *
 *   getMyOrgEntitlements   OrganizationService: whether the org has "ml", and whether someone asked
 *   requestOrgEntitlement  OrganizationService: ask Reliant for a feature (idempotent per org)
 *   read(method, req)      any MlEarlyAccessService rpc, by its client method name
 *                          ("getMlRun", "queryMlSeries", ...), typed by the generated client
 *
 * Each is one call over the generated client (proto-vendor/controlplane). An
 * ML read resolves to an MlOutcome holding the generated response message:
 * the data, "not entitled" (FEATURE_NOT_ENTITLED) or "no data yet"
 * (Unimplemented, also this client's answer with no control plane
 * configured); anything else rejects. The entitlement calls reject on every
 * failure: the gate decides what each one means.
 */

import { Code, ConnectError, type Client, type Transport } from "@connectrpc/connect";
import type { DescService } from "@bufbuild/protobuf";

import { MlEarlyAccessService } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { OrganizationService } from "@/gen/controlplane/services/organization/v1/organization_pb";
import { transport as appTransport } from "@/lib/connect";

import { getControlPlaneClient } from "../client";
import { CONTROL_PLANE_API_URL } from "../config";
import { outcomeOfError } from "./errors";
import type { CallOptions, EntitlementRequestResult, MlOutcome, OrgEntitlement } from "./types";
import { entitlementsOf, requestResultOf } from "./wire";

/** The generated MlEarlyAccessService client. */
export type MlClient = Client<typeof MlEarlyAccessService>;
/** An rpc, by its client method name: "getMlRun", "listMlFindings", ... */
export type MlMethod = keyof MlClient;
/** What an rpc takes, without the org (the caller's gate supplies it). */
export type MlRequest<M extends MlMethod> = Omit<NonNullable<Parameters<MlClient[M]>[0]>, "orgId" | "$typeName">;
/** What an rpc answers: its generated response message. */
export type MlResponse<M extends MlMethod> = Awaited<ReturnType<MlClient[M]>>;

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

export async function getMyOrgEntitlements(orgId: string, opts?: CallOptions, transport?: Transport): Promise<OrgEntitlement[]> {
  return entitlementsOf(await client(OrganizationService, transport).getMyOrgEntitlements({ orgId }, opts));
}

export async function requestOrgEntitlement(orgId: string, feature: string, note?: string, transport?: Transport): Promise<EntitlementRequestResult> {
  const res = await client(OrganizationService, transport).requestOrgEntitlement({ orgId, feature, note: note ?? "" });
  return requestResultOf(res, feature, Date.now());
}

/** One MlEarlyAccessService rpc for `orgId`, as an outcome. */
export async function readMl<M extends MlMethod>(
  orgId: string,
  method: M,
  req: MlRequest<M>,
  opts?: CallOptions,
  transport?: Transport,
): Promise<MlOutcome<MlResponse<M>>> {
  try {
    const call = client(MlEarlyAccessService, transport)[method] as unknown as (r: unknown, o?: CallOptions) => Promise<MlResponse<M>>;
    return { kind: "ok", data: await call({ ...req, orgId }, opts) };
  } catch (error) {
    return outcomeOfError<MlResponse<M>>(error);
  }
}
