// Copyright (c) 2025 Reliant Labs

import type { ParsedLocation } from "@tanstack/react-router";
import { create } from "zustand";

import { chooseActiveOrg, learnOrgSegments, useActiveOrgStore } from "@/store/activeOrgStore";
import { currentSessionOwner } from "./auth/sessionOwner";
import { announceOrgArrival } from "./orgArrivalNotice";

/**
 * THE ORG THE PAGE ON SCREEN IS IN: the one authority for every org-scoped
 * read and write (control-plane docs/design/unified-shell-and-projects.md §3.5).
 *
 * Every org-scoped page is mounted under the `/o/$org` layout route
 * (routes.tsx, orgRoutes.tsx). Its `beforeLoad` resolves the URL's org segment
 * against the person's memberships (lib/orgRouting.ts) and puts the answer in
 * the route's context as `orgRoute`, before any child loads. Nothing acts on
 * that answer until the navigation COMMITS: the root route's `onEnter` /
 * `onStay` (routeOrgScopeHooks, run synchronously as the router swaps in the
 * new matches, before React renders them) read the committed matches and set
 * the route scope below. So while a navigation from org A to org B loads, or
 * if it is superseded and never commits, everything still names A, the org of
 * the page on screen; and the moment B's page is the one rendered, everything
 * names B.
 *
 * The scope is what the three consumers read, so they cannot disagree:
 *
 *   - query keys and explicit `org_id` fields (lib/orgScope.ts, getOrgScope /
 *     useOrgScope);
 *   - the request interceptor's implicit `org_id` (api/orgInterceptor.ts);
 *   - `currentOrgId()`, the observability and identity reads' required org
 *     (services/controlPlane/orgs.ts).
 *
 * Its four states keep a page outside the org tree apart from an org page
 * whose org is not (yet) known:
 *
 *   legacy   a page outside the org tree (Settings, the mobile tree, an
 *            org-less path with the flag off): the tab's org applies, as it
 *            did before U3 (store/activeOrgStore);
 *   pending  an org URL loaded fresh, before its first resolution commits:
 *            nothing is named; org-scoped reads wait and implicit requests
 *            are held;
 *   member   a committed org page the person is in: everything names it;
 *   denied   a committed org page that is not theirs, nobody signed in, or a
 *            membership list that failed: nothing is named, and implicit
 *            requests are refused rather than sent with the previous org.
 *
 * Entering a member org also updates the tab's org (the mirror code outside
 * the org tree reads): written here, at commit, and never by a load that might
 * not commit. When a URL moved the tab into another org that way, the person
 * is told once (lib/orgArrivalNotice.ts).
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

/** The org scope of the committed route (see the module comment). */
export type RouteOrgScope =
  | { kind: "legacy" }
  | { kind: "pending" }
  | { kind: "member"; org: RouteOrg }
  | { kind: "denied"; reason: Exclude<OrgRouteState["kind"], "member"> | "unresolved" };

/** The segment an org's URLs carry: its slug, or its id when it has none. */
export function orgSegment(org: RouteOrg): string {
  return org.slug || org.id;
}

interface MatchLike {
  routeId: string;
  context?: unknown;
}

/** Membership and the account epoch that resolved it. Cached matches may outlive that account. */
export interface OrgRouteContext {
  orgRoute: OrgRouteState;
  orgRouteEpoch: number;
}

/** The scope `matches` (a committed match list) put the page in. */
export function routeOrgScopeOfMatches(matches: readonly MatchLike[] | undefined): RouteOrgScope {
  const layout = (matches ?? []).find((match) => match.routeId === ORG_ROUTE_ID);
  if (!layout) return LEGACY;
  const context = layout.context as Partial<OrgRouteContext> | undefined;
  const state = context?.orgRoute;
  if (!state) return { kind: "denied", reason: "unresolved" };
  if (context?.orgRouteEpoch !== currentRouteOrgEpoch()) return PENDING;
  if (state.kind === "member") return { kind: "member", org: state.org };
  return { kind: "denied", reason: state.kind };
}

/** Whether a route's context (a `beforeLoad`'s `context`) is outside the org tree or in a member org. */
export function inMemberOrLegacyContext(context: unknown): boolean {
  const route = context as Partial<OrgRouteContext> | undefined;
  return route?.orgRoute === undefined || (route.orgRoute.kind === "member" && route.orgRouteEpoch === currentRouteOrgEpoch());
}

/** Reuse the account epoch that also invalidates queued settings writes and caches. */
export function currentRouteOrgEpoch(): number {
  return currentSessionOwner().epoch;
}

const LEGACY: RouteOrgScope = { kind: "legacy" };
const PENDING: RouteOrgScope = { kind: "pending" };

/** A page load that starts on an org URL is pending until its first commit; any other starts legacy. */
function initialScope(): RouteOrgScope {
  if (typeof window === "undefined") return LEGACY;
  return /^\/o\/[^/]+/.test(window.location.pathname) ? PENDING : LEGACY;
}

export const useRouteOrgScope = create<{ scope: RouteOrgScope }>(() => ({ scope: initialScope() }));

/** The committed route's org scope, for code outside React. */
export function getRouteOrgScope(): RouteOrgScope {
  return useRouteOrgScope.getState().scope;
}

function sameScope(a: RouteOrgScope, b: RouteOrgScope): boolean {
  if (a.kind !== b.kind) return false;
  if (a.kind === "member" && b.kind === "member") return a.org.id === b.org.id && a.org.slug === b.org.slug;
  if (a.kind === "denied" && b.kind === "denied") return a.reason === b.reason;
  return true;
}

/**
 * The committed matches put the page in `scope`: record it, and, for a member
 * org, make it the tab's org (the mirror). Only the router calls this, through
 * routeOrgScopeHooks; tests call it to stand in for a commit.
 */
export function commitRouteOrgScope(scope: RouteOrgScope): void {
  if (!sameScope(getRouteOrgScope(), scope)) useRouteOrgScope.setState({ scope });
  if (scope.kind !== "member") return;
  learnOrgSegments([scope.org]);
  if (useActiveOrgStore.getState().chosenId !== scope.org.id) chooseActiveOrg(scope.org.id);
}

interface RouterLike {
  state?: { matches?: readonly MatchLike[]; pendingMatches?: readonly MatchLike[]; location?: { pathname: string } };
  invalidate?: () => Promise<unknown>;
}

/** Drop the previous account's scope synchronously; do not reroute until auth is installed. */
export function clearRouteOrgForAccountTransition(): void {
  if (getRouteOrgScope().kind !== "legacy") commitRouteOrgScope(PENDING);
}

/** Re-resolve even an unchanged URL using the new account's membership cache. */
export function resolveRouteOrgAfterAccountTransition(): void {
  const router = (globalThis as { __RELIANT_ROUTER?: RouterLike }).__RELIANT_ROUTER;
  const pendingOrg = router?.state?.pendingMatches?.some((match) => match.routeId === ORG_ROUTE_ID);
  if (getRouteOrgScope().kind !== "legacy" || pendingOrg) void router?.invalidate?.();
}

/**
 * The scope of the router's committed matches (the global registry's router,
 * routes.tsx), and the notice when entering it moved the tab's org
 * (lib/orgArrivalNotice.ts).
 */
function syncFromRouter(): void {
  const router = (globalThis as { __RELIANT_ROUTER?: RouterLike }).__RELIANT_ROUTER;
  if (!router?.state) return;
  const scope = routeOrgScopeOfMatches(router.state.matches);
  const chosenBefore = useActiveOrgStore.getState().chosenId;
  commitRouteOrgScope(scope);
  if (scope.kind === "member") announceOrgArrival(scope.org.id, chosenBefore, router.state.location?.pathname ?? "");
}

/**
 * The root route's commit hooks: every route tree that mounts `/o/$org` spreads
 * these into its `createRootRoute` options. The router runs them synchronously
 * right after it swaps in a navigation's matches (the root match enters on the
 * first commit and stays on every one after), so the scope moves exactly when
 * the page on screen does: never during a load, never for a load that does not
 * commit.
 */
export const routeOrgScopeHooks = {
  onEnter: syncFromRouter,
  onStay: syncFromRouter,
};

/** The org of the page on screen, or null outside the org tree and on an org page that names no member org. */
export function currentRouteOrg(): RouteOrg | null {
  const scope = getRouteOrgScope();
  return scope.kind === "member" ? scope.org : null;
}

/** Resolves with the scope once it is not pending (the first commit of a page load that starts on an org URL). */
export function whenRouteOrgSettled(): Promise<RouteOrgScope> {
  const now = getRouteOrgScope();
  if (now.kind !== "pending") return Promise.resolve(now);
  return new Promise((resolve) => {
    const unsubscribe = useRouteOrgScope.subscribe(({ scope }) => {
      if (scope.kind === "pending") return;
      unsubscribe();
      resolve(scope);
    });
  });
}

const memberOnlyBeforeLoads = new WeakSet<object>();

/**
 * What a memberOnly `beforeLoad` reads of the router's options. `params` is
 * there only when the route names them: createRoute infers the route's own
 * params from its `beforeLoad` too, so a `params: unknown` here would make
 * every route's params `unknown`.
 */
export type MemberOnlyOptions<TParams = never> = {
  context?: unknown;
  location: ParsedLocation;
} & ([TParams] extends [never] ? unknown : { params: TParams });

/**
 * A route's `beforeLoad` that runs only outside the org tree or in a member
 * org. A redirect under `/o/$org` (a retired path's, a bare area's) must never
 * run for an org that is not the person's: the layout renders the one "no
 * access" page there, and a child's redirect would carry the URL away from it.
 * The router still runs a child's `beforeLoad` when its layout resolved to no
 * access, so every one under the org tree goes through this
 * (`orgTreeBeforeLoads.guard.test.ts` holds the production trees to it).
 */
// The options are typed here, not inferred through createRoute: TypeScript 7
// infers a `beforeLoad` passed through a generic wrapper with the route's type
// parameters still unresolved (`never`). A route whose redirect reads params
// names ALL of its own path's params (`memberOnly<{ kind: string; key: string }>`
// for `…/$kind/$key`): createRoute infers the route's params from its
// `beforeLoad`, so a subset would drop the rest from the route's type
// (routeOrg.memberOnly.test.ts pins both). Skipping returns no context.
export function memberOnly<TParams = never>(
  beforeLoad: (options: MemberOnlyOptions<NoInfer<TParams>>) => void,
): (options: MemberOnlyOptions<NoInfer<TParams>>) => void {
  const wrapped = (options: MemberOnlyOptions<NoInfer<TParams>>) =>
    inMemberOrLegacyContext(options.context) ? beforeLoad(options) : undefined;
  memberOnlyBeforeLoads.add(wrapped);
  return wrapped;
}

/** Whether `beforeLoad` was made by memberOnly. */
export function isMemberOnly(beforeLoad: unknown): boolean {
  return typeof beforeLoad === "function" && memberOnlyBeforeLoads.has(beforeLoad);
}
