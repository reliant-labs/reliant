// Copyright (c) 2025 Reliant Labs

import { useInfiniteQuery, useQuery } from "@tanstack/react-query";

import { Code, ConnectError } from "@connectrpc/connect";

import { getMyPolicyUsage, getOrgPolicy, isUsageUnavailable, listPolicyHistory, orgPolicyKeys } from "@/services/controlPlane/orgPolicy";

const FINAL_CODES = new Set([Code.PermissionDenied, Code.Unauthenticated, Code.InvalidArgument]);

/**
 * The caller's own spend against the org's caps. An older control plane has
 * no such rpc: that is an answer ("not available yet"), never retried.
 */
export function useMyPolicyUsage(orgId: string, enabled = true) {
  return useQuery({
    queryKey: orgPolicyKeys.usage(orgId),
    queryFn: () => getMyPolicyUsage(orgId),
    enabled: !!orgId && enabled,
    staleTime: 60_000,
    // A refusal (not a member, not a person) is a verdict: asking again changes nothing.
    retry: (count, err) => !isUsageUnavailable(err) && !(err instanceof ConnectError && FINAL_CODES.has(err.code)) && count < 2,
  });
}

/** The org's policy document and its version. */
export function useOrgPolicy(orgId: string) {
  return useQuery({
    queryKey: orgPolicyKeys.policy(orgId),
    queryFn: () => getOrgPolicy(orgId),
    enabled: !!orgId,
    staleTime: 30_000,
  });
}

/** The policy's field-level edits, newest first. */
export function usePolicyHistory(orgId: string) {
  return useInfiniteQuery({
    queryKey: orgPolicyKeys.history(orgId),
    queryFn: ({ pageParam }) => listPolicyHistory(orgId, pageParam),
    initialPageParam: "",
    getNextPageParam: (last) => last.nextPageToken || undefined,
    enabled: !!orgId,
  });
}
