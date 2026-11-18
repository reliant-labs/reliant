// Copyright (c) 2025 Reliant Labs

/**
 * THE ORG THE CURRENT ROUTE NAMES (control-plane
 * docs/design/unified-shell-and-projects.md §3.5).
 *
 * Every org-scoped page is mounted under the `/o/$org` layout route
 * (routes.tsx, orgRoutes.tsx). Its `beforeLoad` resolves the URL's org segment
 * against the person's memberships (lib/orgRouting.ts) and puts the answer in
 * the route's context as `orgRoute`, before any child renders. This module
 * reads that answer back from the router's rendered matches, for code that is
 * not a component: the request interceptor (api/orgInterceptor.ts) and the
 * path builders (lib/paths/org.ts).
 *
 * Dependency-free on purpose: it reads the router from the global registry
 * (routes.tsx registers it), so the transport and lib/paths can import it
 * without importing the route tree.
 */

/** The org layout's path, and so its route id, in every tree that mounts it. */
export const ORG_ROUTE_ID = "/o/$org";

/** An org a route resolved: the id requests name, and the slug its URLs carry. */
export interface RouteOrg {
  id: string;
  /** `MyOrganization.slug`; "" for an org that has none, whose URLs carry its id. */
  slug: string;
}

/** What the `/o/$org` layout's `beforeLoad` decided (lib/orgRouting.ts). */
export type OrgRouteState =
  /** The person is in it: the pages render, and requests name it. */
  | { kind: "member"; org: RouteOrg }
  /** Not one of theirs, or no such org: the one "no access" page, which never says which. */
  | { kind: "not-member"; orgs: readonly RouteOrgChoice[] }
  /** Nobody is signed in yet: the layout's guard sends them to sign in and back. */
  | { kind: "signed-out" }
  /** The membership list could not be read: an error with a Retry, never a guess. */
  | { kind: "error"; error: Error };

/** One of the person's orgs, as the "no access" page lists it. */
export interface RouteOrgChoice extends RouteOrg {
  name: string;
}

/** The segment an org's URLs carry: its slug, or its id when it has none. */
export function orgSegment(org: RouteOrg): string {
  return org.slug || org.id;
}

interface MatchLike {
  routeId: string;
  context?: unknown;
}

/** The org `matches` resolved, or null when none is under the org layout or it named no member org. */
export function routeOrgOfMatches(matches: readonly MatchLike[] | undefined): RouteOrg | null {
  for (const match of matches ?? []) {
    if (match.routeId !== ORG_ROUTE_ID) continue;
    const state = (match.context as { orgRoute?: OrgRouteState } | undefined)?.orgRoute;
    return state?.kind === "member" ? state.org : null;
  }
  return null;
}

/**
 * The org of the page on screen: the rendered matches', so a navigation still
 * loading never changes it early. Null outside the org tree, before the router
 * exists, and on the "no access" page.
 */
export function currentRouteOrg(): RouteOrg | null {
  const router = (globalThis as { __RELIANT_ROUTER?: { state?: { matches?: readonly MatchLike[] } } }).__RELIANT_ROUTER;
  return routeOrgOfMatches(router?.state?.matches);
}
