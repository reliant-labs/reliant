// Copyright (c) 2025 Reliant Labs

import type { QueryClient, QueryKey } from "@tanstack/react-query";

import { getRouteOrgScope, useRouteOrgScope, type RouteOrgScope } from "@/lib/routeOrg";
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
 * On an org page (`/o/<org>/…`) the org is the committed route's
 * (lib/routeOrg.ts), not the tab's store: a navigation into another org that
 * is still loading changes nothing here until it commits, and an org page
 * whose org is pending or denied is never ready, so it names no org and never
 * falls back to the previous one. Outside the org tree the tab's org applies,
 * as below.
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

function scopeOf(route: RouteOrgScope, userId: string | null, effectiveId: string | null, resolved: boolean, failure: Failure): OrgScope {
  if (route.kind === "member") return stableScope(route.org.id);
  if (route.kind !== "legacy") return UNRESOLVED;
  if (!hasControlPlane || userId === null) return NO_ORGS;
  if (effectiveId === null) {
    if (resolved) return NO_ORGS;
    return failure ? failedScope(failure) : UNRESOLVED;
  }
  return stableScope(effectiveId);
}

/**
 * For a hook that reads "the caller's org" through a query of its own (the
 * observability and identity reads, whose keys name no org): the committed
 * org page's org; null on an org page that names none (pending or denied);
 * undefined outside the org tree, where the hook reads it as before. So on an
 * org page that hook is the route's org at the same render as the keys and
 * the interceptor, never a cached answer for the org the tab was in before.
 */
export function useRouteOrgIdOverride(): string | null | undefined {
  const route = useRouteOrgScope((s) => s.scope);
  if (route.kind === "member") return route.org.id;
  return route.kind === "legacy" ? undefined : null;
}

/** The org scope right now, for code outside React. */
export function getOrgScope(): OrgScope {
  const { userId, effectiveId, resolved, failure } = useActiveOrgStore.getState();
  return scopeOf(getRouteOrgScope(), userId, effectiveId, resolved, failure);
}

/** The org scope, re-rendering when it changes. The same object for an unchanged scope. */
export function useOrgScope(): OrgScope {
  const userId = useActiveOrgStore((s) => s.userId);
  const effectiveId = useActiveOrgStore((s) => s.effectiveId);
  const resolved = useActiveOrgStore((s) => s.resolved);
  const failure = useActiveOrgStore((s) => s.failure);
  const route = useRouteOrgScope((s) => s.scope);
  return scopeOf(route, userId, effectiveId, resolved, failure);
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
    // Either store can make it ready: the tab's org, or the route's commit.
    const check = () => {
      const scope = getOrgScope();
      if (scope.ready) {
        stop();
        resolve(scope);
      } else if (scope.error) {
        stop();
        reject(scope.error);
      }
    };
    const unsubscribes = [useActiveOrgStore.subscribe(check), useRouteOrgScope.subscribe(check)];
    const stop = () => unsubscribes.forEach((unsubscribe) => unsubscribe());
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
    const orgId = orgOfRequest(part);
    if (orgId) return orgId;
  }
  return null;
}

/**
 * The org a generated key's request names: its `orgId`, or its scope's
 * (control-plane's observability reads carry the org in a QueryScope,
 * `{ scope: { orgId } }`).
 */
function orgOfRequest(part: unknown): string | null {
  if (typeof part !== "object" || part === null) return null;
  const { orgId, scope } = part as { orgId?: unknown; scope?: { orgId?: unknown } };
  if (typeof orgId === "string" && orgId) return orgId;
  const scoped = typeof scope === "object" && scope !== null ? scope.orgId : undefined;
  return typeof scoped === "string" && scoped ? scoped : null;
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
 * After the tab's org moved off `left` (a switch, or a reconcile away from a
 * saved choice): drop the answers cached under `queryKey` and re-read what is
 * on screen, in the new org.
 *
 * A query whose key names `left` is removed, never refetched. Its observers
 * are still attached to it when this runs: TanStack Query moves an observer
 * to its new key in an effect, and ActiveOrgBinding's effect runs before the
 * screen's. A refetch would send `left`'s request again (its key and request
 * name `left`), and its answer is one nothing will show. The screen's own
 * effect then reads the new org's key. Every other query is reset and
 * refetched: its key names no org, so the request goes out in the
 * transport's org, which is already the new one.
 */
export function resetAfterLeavingOrg(client: QueryClient, queryKey: QueryKey, left: string | null): Promise<void> {
  if (left) client.removeQueries({ queryKey, predicate: (query) => keyNamesOrg(query.queryKey, left) });
  return client.resetQueries({ queryKey });
}

/**
 * Whether a query key names `orgId`: as a segment of its own (the chat list's
 * key) or as the `orgId` of a generated key's request.
 */
export function keyNamesOrg(queryKey: readonly unknown[], orgId: string): boolean {
  return queryKey.some((part) => part === orgId || orgOfRequest(part) === orgId);
}
