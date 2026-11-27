// Copyright (c) 2025 Reliant Labs

/**
 * React Query hooks for an environment's Identity tab
 * (controlplane.v1.IdentityService), over its generated hooks
 * (hooks/controlplane/identity-service-hooks_gen.ts). Those resolve their
 * client from the QueryClient they run on, so the tab reads a
 * TransportScope's transport inside one (a sample app) and the app's
 * anywhere else; tests answer them at the transport.
 *
 * Every request names (org, env), so two environments never share an answer,
 * and a write re-reads exactly the environment it changed
 * (useIdentityWriteOptions).
 */

import { useMutation, useQuery, useQueryClient, type InfiniteData, type QueryClient } from "@tanstack/react-query";
import { useRouteOrgIdOverride } from "@/lib/orgScope";

import { IdentityService, type ListIdentityUsersResponse } from "@/gen/controlplane/services/identity/v1/identity_pb";
import {
  identityServiceKeys,
  useGetIdentityStatus,
  useGetUser,
  useListUsersInfinite,
} from "@/hooks/controlplane/identity-service-hooks_gen";
import { connectClient } from "@/lib/connect";
import { scopedClient } from "@/lib/transportScope";
import { currentOrgId } from "@/services/controlPlane/orgs";
import {
  identityStateOf,
  listIdentityUsersRequest,
  toIdentityExportURL,
  toIdentityStatus,
  toIdentityUser,
  toIdentityUsersPage,
  type IdentityUserFilter,
  type IdentityUsersPage,
} from "@/services/controlPlane/identity/api";
import { identityRetry } from "@/services/controlPlane/identity/errors";

export const identityKeys = {
  /** The caller's org outside the org tree (currentOrgId), not an IdentityService read. */
  org: () => ["identity", "org"] as const,
};

/** The caller's current org, which every identity request is scoped by. The server verifies membership. */
export function useIdentityOrg() {
  // On an org page, the route's org (lib/orgScope.ts); the query only outside the org tree.
  const routeOrg = useRouteOrgIdOverride();
  const org = useQuery({
    queryKey: identityKeys.org(),
    // The active org (the switcher's choice, else the person's default): the app's, in a scope too.
    queryFn: () => currentOrgId(),
    staleTime: 5 * 60_000,
    retry: identityRetry,
    enabled: routeOrg === undefined,
  });
  if (routeOrg !== undefined) return { orgId: routeOrg, isLoading: routeOrg === null, error: null };
  return { orgId: org.data ?? null, isLoading: org.isLoading, error: org.error };
}

// A converging instance moves on its own; a settled one does not.
const CONVERGING = new Set(["pending", "creating", "provisioning", "deleting"]);
const STATUS_POLL_MS = 10_000;

export function useIdentityStatus(orgId: string | null, envId: string) {
  return useGetIdentityStatus(
    { orgId: orgId ?? "", environmentId: envId },
    {
      enabled: !!orgId && !!envId,
      retry: identityRetry,
      refetchInterval: (query) => (query.state.data && CONVERGING.has(identityStateOf(query.state.data.state)) ? STATUS_POLL_MS : false),
      select: toIdentityStatus,
    }
  );
}

export function useIdentityUser(orgId: string, envId: string, userId: string | null, enabled: boolean) {
  return useGetUser(
    { orgId, environmentId: envId, userId: userId ?? "" },
    {
      enabled: enabled && !!orgId && !!userId,
      retry: identityRetry,
      select: toIdentityUser,
    }
  );
}

/** Every loaded page in the console's model. Module-level, so `select` keeps one result per answer. */
function toUsersPages(data: InfiniteData<ListIdentityUsersResponse, string>): InfiniteData<IdentityUsersPage, string> {
  return { pages: data.pages.map(toIdentityUsersPage), pageParams: data.pageParams };
}

export function useIdentityUsers(orgId: string | null, envId: string, filter: IdentityUserFilter, enabled: boolean) {
  return useListUsersInfinite(listIdentityUsersRequest(orgId ?? "", envId, filter), {
    enabled: enabled && !!orgId && !!envId,
    retry: identityRetry,
    select: toUsersPages,
  });
}

/** Whether a query key is one of this environment's IdentityService reads (its request names the org and env). */
function inEnvironment(key: readonly unknown[], orgId: string, envId: string): boolean {
  return key.some(
    (part) =>
      typeof part === "object" &&
      part !== null &&
      (part as { orgId?: unknown }).orgId === orgId &&
      (part as { environmentId?: unknown }).environmentId === envId
  );
}

/** Re-read everything the tab shows for one environment: its status, users list and opened users. */
export function invalidateIdentityEnvironment(queryClient: QueryClient, orgId: string, envId: string) {
  return queryClient.invalidateQueries({
    queryKey: identityServiceKeys.all,
    predicate: (query) => inEnvironment(query.queryKey, orgId, envId),
  });
}

/**
 * The options every identity write takes: it re-reads everything the tab
 * shows for this environment whatever the outcome — a verb that failed half
 * way may still have changed the instance. So the generated success-only
 * refresh is off (`invalidate: false`) and the re-read runs on settle, as a
 * promise the mutation waits for: the caller's own callbacks (a toast, closing
 * a dialog) run once the tab shows the new state.
 *
 * `secret`: the result is a credential (a password-set code, a signed export
 * URL). The mutation cache drops it as soon as nothing observes it, rather
 * than keeping it for the default five minutes; the caller holds the only
 * copy and clears it.
 */
export function useIdentityWriteOptions(orgId: string, envId: string, opts: { secret?: boolean } = {}) {
  const queryClient = useQueryClient();
  return {
    ...(opts.secret ? { gcTime: 0 } : {}),
    invalidate: false as const,
    onSettled: () => invalidateIdentityEnvironment(queryClient, orgId, envId),
  };
}

// The generated hooks' client: the scope's transport on a scope's QueryClient, the app's elsewhere.
const identityClientOf = scopedClient(IdentityService, connectClient(IdentityService));

/**
 * A fresh signed URL for the export that already exists (GetExportURL), on
 * a click. Hand-written rather than the generated useGetExportURL: the rpc is
 * named like a read, but its answer is a bearer credential for every password
 * hash, so it must never enter the query cache. It runs as a write instead,
 * on the scope-aware client, with the same `secret` handling and re-read.
 */
export function useIdentityExportURL(orgId: string, envId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    gcTime: 0,
    mutationFn: async (_: void) =>
      toIdentityExportURL(await identityClientOf({ client: queryClient }).getExportURL({ orgId, environmentId: envId })),
    onSettled: () => invalidateIdentityEnvironment(queryClient, orgId, envId),
  });
}
