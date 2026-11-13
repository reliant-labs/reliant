// Copyright (c) 2025 Reliant Labs

import { useInfiniteQuery, useQuery } from "@tanstack/react-query";

import { getOrgPolicy, listPolicyHistory, orgPolicyKeys } from "@/services/controlPlane/orgPolicy";

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
