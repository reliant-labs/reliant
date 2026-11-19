// Copyright (c) 2025 Reliant Labs

/**
 * WHICH ORG A URL OPENS IN (control-plane
 * docs/design/unified-shell-and-projects.md §3.2–§3.5).
 *
 *   resolveOrgRoute   the `/o/$org` layout's beforeLoad. Resolves the URL's
 *                     segment against the person's memberships
 *                     (ListMyOrganizations, the switcher's own cache entry)
 *                     before any child renders: a slug is the org; an org id
 *                     redirects to the slug form (D6); anything else — not
 *                     theirs, or no such org — is the one "no access" page,
 *                     which never says which. It changes no state anyone
 *                     reads: a load can be superseded and never commit. The
 *                     answer takes effect when it commits (lib/routeOrg.ts,
 *                     the root's commit hooks): then a member org becomes the
 *                     route's scope, which query keys, explicit org fields and
 *                     the interceptor all read, and the tab's org.
 *   forwardIntoOrg    the root route's beforeLoad. With the flag
 *                     `web.org_routes` on, the org-less path of an org-scoped
 *                     page (lib/paths/legacy.ts) redirects into the tab's org,
 *                     keeping the rest of the path and the query.
 *   entryOrgSegment   that org (§3.3, D7): this tab's last, else this
 *                     browser's last, else the person's default
 *                     (`users.default_org_id`). Nothing is written to the
 *                     server: a server-side "last org" would couple tabs and
 *                     devices again.
 *
 * A deployment without orgs (no control plane: self-hosted) and a machine
 * credential (no membership list) have no org to put in a URL: an org URL
 * there opens the org-less page.
 */

import { redirect } from "@tanstack/react-router";

import { inOrg, orgFreePath, orgRootPath } from "@/lib/paths/org";
import { legacyOrgForward } from "@/lib/paths/legacy";
import { queryClient } from "@/lib/query-client";
import { hasControlPlane } from "@/services/controlPlane/config";
import { listMyOrganizations, orgKeys, resolveActiveOrg, type MyOrg, type MyOrgs } from "@/services/controlPlane/orgs";
import {
  bindActiveOrgUser,
  isMachineCredentialUser,
  learnOrgSegments,
  useActiveOrgStore,
} from "@/store/activeOrgStore";
import { useAuthStore } from "@/store/authStore";

import { orgLinksEnabled } from "./orgRoutesFlag";
import type { OrgRouteState } from "./routeOrg";

/** What the resolution reads, swappable in tests. */
export interface OrgRoutingDeps {
  /** The signed-in user's id once the session is restored, or null when nobody is. */
  signedInUser: () => Promise<string | null>;
  /** Whether this deployment has orgs at all (a control plane). */
  hasOrgs: () => boolean;
  /** The person's orgs. */
  listOrgs: () => Promise<MyOrgs>;
}

export const defaultOrgRoutingDeps: OrgRoutingDeps = {
  signedInUser: async () => {
    // A first load restores the session here, before any guard renders; the
    // guard's own call joins this one (authStore.initialize is shared).
    if (!useAuthStore.getState().initialized) await useAuthStore.getState().initialize();
    return useAuthStore.getState().user?.id ?? null;
  },
  hasOrgs: () => hasControlPlane,
  listOrgs: () => queryClient.fetchQuery({ queryKey: orgKeys.mine, queryFn: listMyOrganizations, staleTime: 60_000 }),
};

/** The part of a router location these read. */
export interface LocationLike {
  pathname: string;
  searchStr?: string;
  hash?: string;
  search?: unknown;
}

function suffixOf(location: LocationLike): string {
  return `${location.searchStr ?? ""}${location.hash ? `#${location.hash}` : ""}`;
}

/**
 * The org `segment` names among `orgs`: by id, else by slug, else by a slug
 * spelled in another case. Id first, as the control plane's resolver does
 * (control-plane internal/db/org_slugs.go), so a historical slug that happens
 * to equal another org's id names the same org on both sides.
 */
export function findOrg(orgs: readonly MyOrg[], segment: string): MyOrg | null {
  return (
    orgs.find((org) => org.id === segment) ??
    orgs.find((org) => org.slug !== "" && org.slug === segment) ??
    orgs.find((org) => org.slug !== "" && org.slug.toLowerCase() === segment.toLowerCase()) ??
    null
  );
}

/** `location` with its org segment replaced by `segment`. */
function inOtherSegment(location: LocationLike, segment: string): string {
  const rest = location.pathname.replace(/^\/o\/[^/]*/, "");
  return `${orgRootPath(segment)}${rest}${suffixOf(location)}`;
}

/**
 * The `/o/$org` layout's beforeLoad: see the module comment. Throws the
 * redirects. Writes nothing the page reads (bar the auth binding and each
 * org's URL segment): a preload, or a load a later navigation supersedes, must
 * leave the page on screen as it was.
 */
export async function resolveOrgRoute(
  segment: string,
  location: LocationLike,
  { deps = defaultOrgRoutingDeps }: { preload?: boolean; deps?: OrgRoutingDeps } = {},
): Promise<{ orgRoute: OrgRouteState }> {
  const userId = await deps.signedInUser();
  if (!userId) return { orgRoute: { kind: "signed-out" } };
  if (!deps.hasOrgs() || isMachineCredentialUser(userId)) {
    throw redirect({ href: `${orgFreePath(location.pathname)}${suffixOf(location)}`, replace: true });
  }
  bindActiveOrgUser(userId);
  let mine: MyOrgs;
  try {
    mine = await deps.listOrgs();
  } catch (error) {
    return { orgRoute: { kind: "error", error: error instanceof Error ? error : new Error(String(error)) } };
  }
  learnOrgSegments(mine.orgs);
  const org = findOrg(mine.orgs, segment);
  if (!org) {
    return {
      orgRoute: { kind: "not-member", orgs: mine.orgs.map((o) => ({ id: o.id, slug: o.slug, name: o.name })) },
    };
  }
  const canonical = org.slug || org.id;
  if (segment !== canonical) throw redirect({ href: inOtherSegment(location, canonical), replace: true });
  return { orgRoute: { kind: "member", org: { id: org.id, slug: org.slug } } };
}

/**
 * The segment of the org a bare `/` or another org-less path opens in (§3.3),
 * or null when there is none to name: nobody signed in, no orgs here, a
 * machine credential, a membership list that failed or is empty.
 */
export async function entryOrgSegment(deps: OrgRoutingDeps = defaultOrgRoutingDeps): Promise<string | null> {
  const userId = await deps.signedInUser();
  if (!userId || !deps.hasOrgs() || isMachineCredentialUser(userId)) return null;
  // Loads this tab's choice, seeded from this browser's last one.
  bindActiveOrgUser(userId);
  let mine: MyOrgs;
  try {
    mine = await deps.listOrgs();
  } catch {
    return null;
  }
  learnOrgSegments(mine.orgs);
  const id = resolveActiveOrg(mine, useActiveOrgStore.getState().chosenId);
  const org = mine.orgs.find((o) => o.id === id);
  return org ? org.slug || org.id : null;
}

/**
 * The root route's beforeLoad: with the flag on, an org-less path of an
 * org-scoped page redirects into the tab's org. Synchronous (and a no-op) for
 * every other path, so the flag off costs nothing.
 */
export function forwardIntoOrg(location: LocationLike, deps: OrgRoutingDeps = defaultOrgRoutingDeps): Promise<void> | void {
  if (!orgLinksEnabled()) return;
  const path = legacyOrgForward(location.pathname, (location.search ?? {}) as Record<string, unknown>);
  if (path === null) return;
  return entryOrgSegment(deps).then((segment) => {
    // No org to name: the page opens as it is (its guard signs the person in,
    // and the link forwards when they come back).
    if (!segment) return;
    throw redirect({ href: `${inOrg(segment, path)}${suffixOf(location)}`, replace: true });
  });
}
