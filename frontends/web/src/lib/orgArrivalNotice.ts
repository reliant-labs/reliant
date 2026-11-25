// Copyright (c) 2025 Reliant Labs

import { create } from "zustand";

/**
 * THE ONE NOTICE FOR A URL THAT MOVED THE TAB'S ORG (control-plane
 * docs/design/unified-shell-and-projects.md §3.4, D8: "deep links into
 * another org switch the org with a notice").
 *
 * A committed org page makes its org the tab's (lib/routeOrg.ts). When that
 * moves the tab from one org to another because of a URL — a link, a
 * bookmark, an org-less link redirected into its resource's org, Back into
 * another org's page — the person is told, once, on the page it landed on:
 *
 *   a resource's own notice   a chat or project URL that a loader redirected
 *                             into the resource's org says so
 *                             (lib/chatRouteResolve.ts, lib/projectRouteResolve.ts):
 *                             "This chat is in Side, so it opened there."
 *   otherwise                 "Switched to Side to open this link."
 *
 * Exactly one per arrival: a resource's notice replaces the generic one, and
 * nothing is said when the tab was already in that org, or when the person
 * switched org themselves (the switcher, lib/orgSwitch.ts, expectOwnOrgSwitch).
 *
 * The router commits before React renders the page (and on a page load,
 * before the Toaster subscribes), so a commit only queues the notice here;
 * OrgArrivalNotice (components/Orgs) shows it as a toast once mounted.
 */

/** An org as the notice names it, learned from the membership list (lib/orgRouting.ts). */
interface KnownOrgs {
  names: Record<string, string>;
  defaultOrgId: string;
}

/** A loader's notice for the redirect it threw, for the page that commits there. */
interface ResourceNotice {
  orgId: string;
  /** The page it redirected to, without its org segment (pageOf). */
  page: string;
  message: string;
  /** Say it even when the tab was already in the org: the URL itself named another one. */
  always: boolean;
}

let known: KnownOrgs = { names: {}, defaultOrgId: "" };
let resource: ResourceNotice | null = null;
let ownSwitch: string | null = null;
let sequence = 0;

/** The notice waiting to be shown; `id` tells two equal messages apart. */
export const useOrgArrivalNotice = create<{ notice: { id: number; message: string } | null }>(() => ({ notice: null }));

/** Remember the person's orgs' names and default (the membership list's answer). */
export function rememberOrgsForNotice(orgs: readonly { id: string; slug: string; name: string }[], defaultOrgId: string): void {
  const names: Record<string, string> = {};
  for (const org of orgs) names[org.id] = org.name || org.slug || org.id;
  known = { names, defaultOrgId };
}

/** The name a notice gives `orgId`. */
export function orgNameForNotice(orgId: string): string {
  return known.names[orgId] ?? orgId;
}

/**
 * The person chose `orgId` themselves (the switcher): the commit that enters
 * it says nothing.
 */
export function expectOwnOrgSwitch(orgId: string): void {
  ownSwitch = orgId;
}

/** A pathname without its org segment and query: `/o/side/c/x?view=1` → `/c/x`. */
function pageOf(pathname: string): string {
  return pathname.split(/[?#]/, 1)[0]!.replace(/^\/o\/[^/]+/, "").replace(/\/+$/, "") || "/";
}

/**
 * A loader redirected a resource's URL into its org, to `href`: the page that
 * commits there, in `orgId`, says `message` instead of the generic notice.
 * `always`: even when the tab was in that org already (the URL named another
 * org). Keyed by the page, so a load that never commits (a preload, one a
 * later navigation superseded) cannot lend its notice to another page.
 */
export function noteResourceOrgRedirect(orgId: string, href: string, message: string, { always }: { always: boolean }): void {
  resource = { orgId, page: pageOf(href), message, always };
}

/** The org the tab was in, as the membership list resolves the tab's choice (services/controlPlane/orgs resolveActiveOrg). */
function tabOrgOf(chosenId: string | null): string | null {
  if (chosenId && known.names[chosenId] !== undefined) return chosenId;
  return known.defaultOrgId || chosenId;
}

/**
 * A member org's page committed (the root's commit hook, lib/routeOrg.ts):
 * queue its notice, if it earns one. `chosenBefore` is the tab's chosen org
 * before the commit wrote it; `pathname` the committed page's.
 */
export function announceOrgArrival(orgId: string, chosenBefore: string | null, pathname: string): void {
  const own = ownSwitch === orgId;
  ownSwitch = null;
  const note = resource !== null && resource.orgId === orgId && resource.page === pageOf(pathname) ? resource : null;
  if (note) resource = null;
  if (own) return;
  const before = tabOrgOf(chosenBefore);
  const moved = before !== null && before !== orgId;
  let message: string | null = null;
  if (note && (note.always || moved)) message = note.message;
  else if (moved) message = `Switched to ${orgNameForNotice(orgId)} to open this link.`;
  if (message) useOrgArrivalNotice.setState({ notice: { id: ++sequence, message } });
}

/** The presenter showed notice `id`: drop it (a newer one stays). */
export function clearOrgArrivalNotice(id: number): void {
  if (useOrgArrivalNotice.getState().notice?.id === id) useOrgArrivalNotice.setState({ notice: null });
}

/** Forget everything (tests). */
export function resetOrgArrivalNoticeForTests(): void {
  known = { names: {}, defaultOrgId: "" };
  resource = null;
  ownSwitch = null;
  useOrgArrivalNotice.setState({ notice: null });
}
