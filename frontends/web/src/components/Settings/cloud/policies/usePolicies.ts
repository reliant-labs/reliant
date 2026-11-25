// Copyright (c) 2025 Reliant Labs

import { useCallback } from "react";
import { type InfiniteData } from "@tanstack/react-query";

import { Code, ConnectError } from "@connectrpc/connect";

import {
  isRpcNotDeployed,
  isUsageUnavailable,
  myPolicyUsageOf,
  policyDenialsOf,
  policyHistoryPageOf,
  policyHistoryRequest,
  policyRecordOf,
} from "@/services/controlPlane/orgPolicy";
import type { ListOrgLimitChangesResponse } from "@/gen/controlplane/services/billing/v1/billing_pb";
import type { GetOrgPolicyResponse } from "@/gen/controlplane/services/org_policy/v1/org_policy_pb";
import { useListOrgLimitChangesInfinite } from "@/hooks/controlplane/billing-service-hooks_gen";
import { useGetMyPolicyUsage, useGetOrgPolicy, useListOrgPolicyDenials } from "@/hooks/controlplane/org-policy-service-hooks_gen";

const FINAL_CODES = new Set([Code.PermissionDenied, Code.Unauthenticated, Code.InvalidArgument]);

/**
 * The caller's own spend against the org's caps. An older control plane has
 * no such rpc: that is an answer ("not available yet"), never retried.
 */
export function useMyPolicyUsage(orgId: string, enabled = true) {
  return useGetMyPolicyUsage(
    { orgId },
    {
      enabled: !!orgId && enabled,
      staleTime: 60_000,
      // A refusal (not a member, not a person) is a verdict: asking again changes nothing.
      retry: (count, err) => !isUsageUnavailable(err) && !(err instanceof ConnectError && FINAL_CODES.has(err.code)) && count < 2,
      select: myPolicyUsageOf,
    },
  );
}

/** The org's policy document and its version. */
export function useOrgPolicy(orgId: string) {
  const select = useCallback((res: GetOrgPolicyResponse) => policyRecordOf(orgId, res), [orgId]);
  return useGetOrgPolicy({ orgId }, { enabled: !!orgId, staleTime: 30_000, select });
}

// Module-level, so React Query re-maps only when the pages change.
const policyHistoryPages = (data: InfiniteData<ListOrgLimitChangesResponse, string>) => ({
  pages: data.pages.map(policyHistoryPageOf),
  pageParams: data.pageParams,
});

/** The policy's field-level edits, newest first. */
export function usePolicyHistory(orgId: string) {
  return useListOrgLimitChangesInfinite(policyHistoryRequest(orgId), {
    enabled: !!orgId,
    select: policyHistoryPages,
  });
}

/**
 * What the org's policies refused, and what audit-mode rules would have, over
 * the last `days` days. Admin/owner: pass enabled=false otherwise. Refusals
 * and an older control plane are final, never retried.
 */
export function useOrgPolicyDenials(orgId: string, days: number, enabled = true) {
  return useListOrgPolicyDenials(
    { orgId, days },
    {
      enabled: !!orgId && enabled,
      staleTime: 60_000,
      retry: (count, err) => !isRpcNotDeployed(err) && !(err instanceof ConnectError && FINAL_CODES.has(err.code)) && count < 2,
      select: policyDenialsOf,
    },
  );
}
