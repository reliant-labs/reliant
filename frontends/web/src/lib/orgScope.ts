// Copyright (c) 2025 Reliant Labs

import { hasControlPlane } from "@/services/controlPlane/config";
import { useActiveOrgStore } from "@/store/activeOrgStore";

/**
 * The org the product's org-scoped lists are read in: projects, the chat list
 * (live, archived and search), the Inbox, workflows, triggers and runs.
 *
 * A list request that names no org is answered with the caller's rows in
 * EVERY org they belong to (the documented meaning of an unset org_id, kept
 * for the CLI and API). So a list must never go out while the tab's org is
 * still unknown, and its cache entry must say which org it holds:
 *
 *   - `ready` is false until the tab's org is known: a saved choice, or the
 *     membership list's answer (which may be "none": a person with no org yet,
 *     a machine credential). Lists pass it as `enabled`.
 *   - `orgId` is that org, or null to name none. Lists put it in their request
 *     (`orgRequest`), so the generated query key names it too, and the request
 *     sent is the one the key describes.
 *
 * With no control plane there are no orgs, and signed out there is nothing to
 * list (the lists wait for a session themselves): both are ready, naming none.
 * Who is signed in is the org store's `userId`, which the auth store binds
 * synchronously on every sign-in and sign-out. This module reads no auth
 * state itself, so pure modules that build list keys stay free of it.
 */
export interface OrgScope {
  ready: boolean;
  orgId: string | null;
  /**
   * The tab's org could not be resolved: the membership list failed with
   * nothing saved to act in. Not ready, and not loading either: lists show
   * this error, and their Retry runs `retry` (re-reads the membership list).
   * Never a fallback to naming no org, which is every org's rows.
   */
  error: Error | null;
  retry: () => void;
}

const noRetry = () => {};
const NO_ORGS: OrgScope = { ready: true, orgId: null, error: null, retry: noRetry };
const UNRESOLVED: OrgScope = { ready: false, orgId: null, error: null, retry: noRetry };

type Failure = ReturnType<typeof useActiveOrgStore.getState>["failure"];

function scopeOf(userId: string | null, effectiveId: string | null, resolved: boolean, failure: Failure): OrgScope {
  if (!hasControlPlane || userId === null) return NO_ORGS;
  if (effectiveId === null) {
    if (resolved) return NO_ORGS;
    return failure ? failedScope(failure) : UNRESOLVED;
  }
  return stableScope(effectiveId);
}

/** The org scope right now, for code outside React. */
export function getOrgScope(): OrgScope {
  const { userId, effectiveId, resolved, failure } = useActiveOrgStore.getState();
  return scopeOf(userId, effectiveId, resolved, failure);
}

/** The org scope, re-rendering when it changes. The same object for an unchanged scope. */
export function useOrgScope(): OrgScope {
  const userId = useActiveOrgStore((s) => s.userId);
  const effectiveId = useActiveOrgStore((s) => s.effectiveId);
  const resolved = useActiveOrgStore((s) => s.resolved);
  const failure = useActiveOrgStore((s) => s.failure);
  return scopeOf(userId, effectiveId, resolved, failure);
}

const scopes = new Map<string, OrgScope>();
function stableScope(orgId: string): OrgScope {
  let scope = scopes.get(orgId);
  if (!scope) {
    scope = { ready: true, orgId, error: null, retry: noRetry };
    scopes.set(orgId, scope);
  }
  return scope;
}

const failedScopes = new WeakMap<NonNullable<Failure>, OrgScope>();
function failedScope(failure: NonNullable<Failure>): OrgScope {
  let scope = failedScopes.get(failure);
  if (!scope) {
    scope = { ready: false, orgId: null, error: failure.error, retry: failure.retry };
    failedScopes.set(failure, scope);
  }
  return scope;
}

/**
 * Resolves with the org scope once it is ready, for an imperative read that
 * must not go out unscoped (fetchProjects). Rejects with the scope's error
 * when the org cannot be resolved (the caller reports it like a failed read),
 * rather than waiting forever. A caller on a hot path checks `getOrgScope()`
 * first and awaits this only when it is not ready, so the common case takes
 * no extra promise hop.
 */
export function whenOrgScopeReady(): Promise<OrgScope> {
  const now = getOrgScope();
  if (now.ready) return Promise.resolve(now);
  if (now.error) return Promise.reject(now.error);
  return new Promise((resolve, reject) => {
    const unsubscribe = useActiveOrgStore.subscribe(() => {
      const scope = getOrgScope();
      if (scope.ready) {
        unsubscribe();
        resolve(scope);
      } else if (scope.error) {
        unsubscribe();
        reject(scope.error);
      }
    });
  });
}

/** Whether a list may run its query: the org is known, or its failure is to be shown. */
export function orgQueryEnabled(org: OrgScope): boolean {
  return org.ready || org.error !== null;
}

/**
 * Query options for a list in `org`: unchanged while the org is known. While
 * it failed to resolve, the query runs a fetch that asks for the membership
 * list again and fails with the org's error, so the list shows an error state
 * whose Retry (a refetch) retries the org, and never sends a request naming no
 * org. Once the org resolves, the request (and so the key) names it and the
 * real fetch runs.
 */
export function withOrgFailure<O extends { queryFn?: unknown }>(options: O, org: OrgScope): O {
  if (!org.error) return options;
  const { error, retry } = org;
  return {
    ...options,
    queryFn: () => {
      retry();
      throw error;
    },
  };
}

/**
 * `req` naming `orgId` (or no org when it is null). The request then carries
 * its org explicitly, so the generated key names it and the transport's
 * orgInterceptor leaves it alone.
 */
export function orgRequest<T extends object>(req: T, orgId: string | null): T & { orgId?: string } {
  return orgId ? { ...req, orgId } : req;
}

/**
 * The org a generated query key's request names (`orgRequest`), or null when
 * it names none.
 */
export function requestOrgOfKey(queryKey: readonly unknown[]): string | null {
  for (const part of queryKey) {
    if (typeof part === "object" && part !== null) {
      const orgId = (part as { orgId?: unknown }).orgId;
      if (typeof orgId === "string" && orgId) return orgId;
    }
  }
  return null;
}

/**
 * `placeholderData` for an org-scoped list that keeps the previous answer on
 * screen while a new one loads (a search term that changed): only when that
 * answer was read in the SAME org. `keepPreviousData` alone would show org A's
 * rows under org B for the renders between the switch and B's answer, since
 * ActiveOrgBinding's reset runs in an effect, after the render with B's key.
 *
 * For GENERATED keys only, whose request names the org (`orgRequest`): the
 * previous request must name `orgId`, or, with `orgId` null, name no org.
 * That last case is provable only because such a key carries its org nowhere
 * else. A key that carries its org as a plain segment (observability's) uses
 * `keepPreviousInOrgSegment`.
 */
export function keepPreviousInOrg(orgId: string | null) {
  return <T>(previous: T | undefined, previousQuery?: { queryKey: readonly unknown[] }): T | undefined =>
    previousQuery && requestOrgOfKey(previousQuery.queryKey) === orgId ? previous : undefined;
}

/**
 * `keepPreviousInOrg` for keys that carry their org as a plain string segment
 * (observability's `["observability", orgId, …]`, the sessions list). A
 * segment cannot prove a key is unscoped, so with `orgId` null (no scope yet,
 * an empty selection) nothing is kept: the previous answer belongs to some org
 * this read no longer names.
 */
export function keepPreviousInOrgSegment(orgId: string | null) {
  return <T>(previous: T | undefined, previousQuery?: { queryKey: readonly unknown[] }): T | undefined =>
    orgId && previousQuery && previousQuery.queryKey.includes(orgId) ? previous : undefined;
}

/**
 * Whether a query key names `orgId`: as a segment of its own (the chat list's
 * key) or as the `orgId` of a generated key's request.
 */
export function keyNamesOrg(queryKey: readonly unknown[], orgId: string): boolean {
  return queryKey.some(
    (part) =>
      part === orgId ||
      (typeof part === "object" && part !== null && (part as { orgId?: unknown }).orgId === orgId),
  );
}
