// Copyright (c) 2025 Reliant Labs

/**
 * react-query hooks for custom domains.
 *
 * Its own file rather than a section of forge-queries.ts, because domains do
 * not share that module's premises. Everything there is keyed by a reliant
 * project id and most of it goes through the daemon; a domain is an ORG
 * resource read straight from the control plane, and there is no project in
 * its key because there is no project in the resource.
 *
 * ── THE POLL IS THE FEATURE ─────────────────────────────────────────────────
 *
 * forge-queries.ts deliberately has no refetchInterval on the topology: a
 * background poll there would discard merged verifications. The opposite is
 * true here. A domain converges on a timescale the user cannot act on — they
 * paste records at their registrar and then WAIT, through propagation, our
 * DNS check, and an ACME order — so a screen that did not move by itself
 * would train them to mash reload. The interval is live only while something
 * is actually converging (see domainIsConverging), so a page showing nothing
 * but live and conflicted domains is silent.
 */

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";

import {
  bindDomain,
  createDomain,
  deleteDomain,
  domainAvailabilityFromError,
  domainIsConverging,
  hasControlPlane,
  listDomains,
  unbindDomain,
  verifyDomain,
  type DomainAvailability,
  type ForgeDomain,
} from "@/services/forge/domains";

/** How often to re-read while a domain is still converging. */
const DOMAIN_POLL_MS = 15_000;

export const domainKeys = {
  all: ["forge", "domains"] as const,
  list: () => [...domainKeys.all, "list"] as const,
};

/**
 * Codes on which asking again cannot change the answer, so a retry only
 * delays the sentence that says what to fix.
 */
const NON_RETRYABLE: ReadonlySet<Code> = new Set([
  Code.PermissionDenied,
  Code.Unauthenticated,
  Code.Unimplemented,
  Code.Unavailable,
  Code.InvalidArgument,
  Code.NotFound,
]);

function domainRetry(failureCount: number, error: unknown): boolean {
  if (error instanceof ConnectError && NON_RETRYABLE.has(error.code)) return false;
  return failureCount < 1;
}

export interface DomainsState {
  availability: DomainAvailability;
  domains: ForgeDomain[];
  /** The server's own message, only when availability is `unreachable`. */
  detail: string;
}

/**
 * Every domain the organization holds.
 *
 * A failure is resolved into an `availability` the screen DESCRIBES rather
 * than an error it reports — the same shape useManagedSecrets uses, and for
 * the same reason: a user without a control plane, or without the org role
 * that may read domains, has not hit a fault.
 */
export function useForgeDomains({ enabled = true }: { enabled?: boolean } = {}) {
  return useQuery<DomainsState>({
    queryKey: domainKeys.list(),
    // An environment page asks only while a tab that shows domains is open.
    enabled,
    queryFn: async () => {
      if (!hasControlPlane()) {
        return { availability: "no-control-plane" as const, domains: [], detail: "" };
      }
      try {
        return { availability: "available" as const, domains: await listDomains(), detail: "" };
      } catch (err) {
        return {
          availability: domainAvailabilityFromError(err),
          domains: [],
          detail: err instanceof ConnectError ? err.rawMessage || err.message : "",
        };
      }
    },
    // Only while something can still move on its own. A list of live and
    // conflicted domains is a settled list, and polling it would be noise.
    refetchInterval: (query) =>
      (query.state.data?.domains ?? []).some((domain) => domainIsConverging(domain.state))
        ? DOMAIN_POLL_MS
        : false,
    staleTime: 5_000,
    retry: domainRetry,
  });
}

/** Re-read the list. Every mutation ends here rather than writing the cache. */
function useInvalidateDomains() {
  const queryClient = useQueryClient();
  return () => {
    void queryClient.invalidateQueries({ queryKey: domainKeys.all });
  };
}

/**
 * Add a domain and, in the same action, say what it should serve.
 *
 * ONE MUTATION FOR BOTH CALLS, because the user performed one action. The
 * control plane keeps them separate — a domain is bindable in any state, and
 * a binding outlives a deployment — but a tenant adding `hounders.club`
 * already knows it is for their web workload, and making them come back for a
 * second step would leave the common case half-finished on screen.
 *
 * The bind is best-effort AFTER the claim succeeds: if binding fails, the
 * domain still exists and is returned, because losing the claim over a
 * binding error would be the worse outcome — the claim is the part that
 * carries the ownership token.
 */
export function useAddDomain() {
  const invalidate = useInvalidateDomains();
  return useMutation<
    { domain: ForgeDomain; bindError: string | null },
    Error,
    { hostname: string; environmentId?: string; target?: string; redirectTo?: string }
  >({
    mutationFn: async ({ hostname, environmentId, target, redirectTo }) => {
      const domain = await createDomain(hostname);
      if (!environmentId || (!target && !redirectTo)) {
        return { domain, bindError: null };
      }
      try {
        await bindDomain({ domainId: domain.id, environmentId, target, redirectTo });
        return { domain, bindError: null };
      } catch (err) {
        return {
          domain,
          bindError: err instanceof Error ? err.message : "The binding could not be created.",
        };
      }
    },
    onSuccess: invalidate,
  });
}

/** Rebind: the same RPC as the first bind, which replaces any existing binding. */
export function useBindDomain() {
  const invalidate = useInvalidateDomains();
  return useMutation<
    void,
    Error,
    { domainId: string; environmentId: string; target?: string; redirectTo?: string }
  >({
    mutationFn: async (args) => {
      await bindDomain(args);
    },
    onSuccess: invalidate,
  });
}

/** Stop serving, keep the domain and its verification. */
export function useUnbindDomain() {
  const invalidate = useInvalidateDomains();
  return useMutation<void, Error, { domainId: string }>({
    mutationFn: ({ domainId }) => unbindDomain(domainId),
    onSuccess: invalidate,
  });
}

/** Remove the domain entirely. Frees the hostname for another org to claim. */
export function useRemoveDomain() {
  const invalidate = useInvalidateDomains();
  return useMutation<void, Error, { domainId: string }>({
    mutationFn: ({ domainId }) => deleteDomain(domainId),
    onSuccess: invalidate,
  });
}

/** Check DNS now. A nudge — the reconciler polls regardless. */
export function useVerifyDomain() {
  const invalidate = useInvalidateDomains();
  return useMutation<ForgeDomain | null, Error, { domainId: string }>({
    mutationFn: ({ domainId }) => verifyDomain(domainId),
    onSuccess: invalidate,
  });
}
