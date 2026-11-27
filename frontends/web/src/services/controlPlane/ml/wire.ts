// Copyright (c) 2026 Reliant Labs

/**
 * OrganizationService's entitlement messages → the gate's view-models
 * (types.ts). Pure, so it is tested without a transport. A zero timestamp is
 * "unset", never 1970.
 */

import { timestampMs, type Timestamp } from "@bufbuild/protobuf/wkt";

import type {
  GetMyOrgEntitlementsResponse,
  OrgFeatureEntitlement,
  RequestOrgEntitlementResponse,
} from "@/gen/controlplane/services/organization/v1/organization_pb";

import type { EntitlementRequestResult, OrgEntitlement } from "./types";

function ms(ts: Timestamp | undefined): number | null {
  if (!ts) return null;
  const value = timestampMs(ts);
  return value > 0 ? value : null;
}

export function entitlementOf(e: OrgFeatureEntitlement): OrgEntitlement {
  return { feature: e.feature, enabled: e.enabled, requestedAtMs: ms(e.requestedAt), grantedAtMs: ms(e.grantedAt) };
}

export function entitlementsOf(res: GetMyOrgEntitlementsResponse): OrgEntitlement[] {
  return res.features.map(entitlementOf);
}

/** RequestOrgEntitlement's answer; a server that sent no entitlement back answers for `feature`, requested now. */
export function requestResultOf(res: RequestOrgEntitlementResponse, feature: string, nowMs: number): EntitlementRequestResult {
  const entitlement = res.entitlement
    ? entitlementOf(res.entitlement)
    : { feature, enabled: res.alreadyEnabled, requestedAtMs: res.alreadyEnabled ? null : nowMs, grantedAtMs: null };
  return { entitlement, alreadyRequested: res.alreadyRequested, alreadyEnabled: res.alreadyEnabled };
}
