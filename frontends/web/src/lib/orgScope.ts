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
}

const NO_ORGS: OrgScope = { ready: true, orgId: null };
const UNRESOLVED: OrgScope = { ready: false, orgId: null };

function scopeOf(userId: string | null, effectiveId: string | null, resolved: boolean): OrgScope {
  if (!hasControlPlane || userId === null) return NO_ORGS;
  if (effectiveId === null) return resolved ? NO_ORGS : UNRESOLVED;
  return { ready: true, orgId: effectiveId };
}

/** The org scope right now, for code outside React. */
export function getOrgScope(): OrgScope {
  const { userId, effectiveId, resolved } = useActiveOrgStore.getState();
  return scopeOf(userId, effectiveId, resolved);
}

/** The org scope, re-rendering when it changes. */
export function useOrgScope(): OrgScope {
  const userId = useActiveOrgStore((s) => s.userId);
  const effectiveId = useActiveOrgStore((s) => s.effectiveId);
  const resolved = useActiveOrgStore((s) => s.resolved);
  const scope = scopeOf(userId, effectiveId, resolved);
  // The same object for an unchanged scope, so it can sit in a dependency list.
  return scope.orgId === null ? scope : stableScope(scope.orgId);
}

const scopes = new Map<string, OrgScope>();
function stableScope(orgId: string): OrgScope {
  let scope = scopes.get(orgId);
  if (!scope) {
    scope = { ready: true, orgId };
    scopes.set(orgId, scope);
  }
  return scope;
}

/**
 * Resolves with the org scope once it is ready, for an imperative read that
 * must not go out unscoped (fetchProjects). Never rejects; it waits as long as
 * the tab's org is unknown. A caller on a hot path checks `getOrgScope()`
 * first and awaits this only when it is not ready, so the common case takes
 * no extra promise hop.
 */
export function whenOrgScopeReady(): Promise<OrgScope> {
  const now = getOrgScope();
  if (now.ready) return Promise.resolve(now);
  return new Promise((resolve) => {
    const unsubscribe = useActiveOrgStore.subscribe(() => {
      const scope = getOrgScope();
      if (!scope.ready) return;
      unsubscribe();
      resolve(scope);
    });
  });
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
