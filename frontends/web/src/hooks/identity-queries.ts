// Copyright (c) 2025 Reliant Labs

/**
 * React Query hooks for an environment's Identity tab
 * (controlplane.v1.IdentityService). Every call goes through
 * IdentityApiContext, so tests render the tab against an in-memory fake.
 *
 * Keys start with (org, env): two environments never share an answer, and a
 * mutation re-reads exactly the environment it changed.
 */

import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { useIdentityApi, type IdentityApi } from "@/services/controlPlane/identity/IdentityApiContext";
import type { IdentityUserFilter } from "@/services/controlPlane/identity/api";
import { identityRetry } from "@/services/controlPlane/identity/errors";

export const identityKeys = {
  org: () => ["identity", "org"] as const,
  env: (orgId: string | null, envId: string) => ["identity", orgId ?? "", envId] as const,
  status: (orgId: string | null, envId: string) => [...identityKeys.env(orgId, envId), "status"] as const,
  users: (orgId: string | null, envId: string, filter: IdentityUserFilter) =>
    [...identityKeys.env(orgId, envId), "users", filter.query?.trim() ?? "", filter.state ?? ""] as const,
};

/** The caller's current org, which every identity request is scoped by. The server verifies membership. */
export function useIdentityOrg() {
  const api = useIdentityApi();
  const org = useQuery({
    queryKey: identityKeys.org(),
    queryFn: () => api.currentOrgId(),
    staleTime: 5 * 60_000,
    retry: identityRetry,
  });
  return { orgId: org.data ?? null, isLoading: org.isLoading, error: org.error };
}

// A converging instance moves on its own; a settled one does not.
const CONVERGING = new Set(["pending", "creating", "provisioning", "deleting"]);
const STATUS_POLL_MS = 10_000;

export function useIdentityStatus(orgId: string | null, envId: string) {
  const api = useIdentityApi();
  return useQuery({
    queryKey: identityKeys.status(orgId, envId),
    queryFn: () => api.getStatus(orgId as string, envId),
    enabled: !!orgId && !!envId,
    retry: identityRetry,
    refetchInterval: (query) => (query.state.data && CONVERGING.has(query.state.data.state) ? STATUS_POLL_MS : false),
  });
}

export function useIdentityUsers(orgId: string | null, envId: string, filter: IdentityUserFilter, enabled: boolean) {
  const api = useIdentityApi();
  return useInfiniteQuery({
    queryKey: identityKeys.users(orgId, envId, filter),
    queryFn: ({ pageParam }) => api.listUsers(orgId as string, envId, filter, pageParam),
    initialPageParam: "",
    getNextPageParam: (last) => last.nextPageToken || undefined,
    enabled: enabled && !!orgId && !!envId,
    retry: identityRetry,
  });
}

/**
 * Every identity write: it runs, then re-reads everything the tab shows for
 * this environment (the users list and the status), whatever the outcome — a
 * verb that failed half way may still have changed the instance.
 *
 * `secret`: the result is a credential (a password-set code). The mutation
 * cache drops it as soon as nothing observes it, rather than keeping it for
 * the default five minutes; the caller holds the only copy and clears it.
 */
export function useIdentityMutation<TArgs, TResult>(
  orgId: string | null,
  envId: string,
  run: (api: IdentityApi, orgId: string, envId: string, args: TArgs) => Promise<TResult>,
  opts: { secret?: boolean } = {}
) {
  const api = useIdentityApi();
  const queryClient = useQueryClient();
  return useMutation({
    ...(opts.secret ? { gcTime: 0 } : {}),
    mutationFn: (args: TArgs) => {
      if (!orgId) return Promise.reject(new Error("no organization"));
      return run(api, orgId, envId, args);
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: identityKeys.env(orgId, envId) }),
  });
}
