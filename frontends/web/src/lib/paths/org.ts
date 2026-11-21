// Copyright (c) 2025 Reliant Labs

/**
 * THE ORG SEGMENT OF AN APP PATH: the one place it is added, and read back.
 *
 * The unified-shell design (control-plane docs/design/unified-shell-and-projects.md
 * §3) puts every org-scoped page under `/o/<org slug>/…`. The `/o/$org` routes
 * are always mounted (routes.tsx), and the runtime flag `web.org_routes` (D12,
 * lib/orgRoutesFlag.ts) decides which form the builders emit:
 *
 *   off  today's path, byte for byte (`/inbox`, `/forge/p/shop`);
 *   on   the org form: `/o/<org>` in front, and the two console paths whose
 *        org form is not their old path behind a prefix rewritten
 *        (orgFormOf): `/forge/p/<app>/…` → `/o/<org>/apps/<app>/…` and
 *        `/forge/observability/…` → `/o/<org>/observability/…`.
 *
 * The org is `ctx.org` when the caller names one, else the org of the route
 * on screen (lib/routeOrg.ts), else the tab's org (store/activeOrgStore) for a
 * link built on a page outside the org tree (Settings, until U8). With none
 * known (signed out, a machine credential, the membership list still loading)
 * a builder emits today's path, which redirects into the tab's org once it is
 * known (lib/orgRouting.ts, forwardIntoOrg).
 *
 * Not org-scoped, so never passed through here: sign-in and the OAuth pages,
 * invitations and claims (`/accept-invite` IS the org choice), and the
 * personal settings. Settings' org sections (D10, U8), machines (U8), the
 * mobile tree (D11, U15) and onboarding (R11) are not wrapped yet either:
 * each joins `withOrg` in the PR that mounts its `/o/<org>` route, so a link
 * never points at a route that does not exist.
 *
 * Code that MATCHES a pathname against a route reads it through orgFreePath
 * first, so one matcher serves both forms.
 */

import { orgLinksEnabled } from "../orgRoutesFlag";
import { currentRouteOrg, orgSegment } from "../routeOrg";
import { getActiveOrgId, orgSegmentOf } from "@/store/activeOrgStore";

/** Which org a path is built for. Optional everywhere: the default is the tab's own org. */
export interface PathContext {
  /**
   * The org's slug (`MyOrganization.slug`), for a link into a different org
   * than the current page's. `null`: today's org-less form whatever the flag
   * says, for a link that must not name the tab's org because the resource's
   * own org decides where it opens (a retired path naming a run or an
   * automation by id: the resolver's, U4).
   */
  org?: string | null;
}

const ORG_PREFIX = "/o/";
const CONSOLE_PREFIX = "/forge/p/";
const ORG_OBSERVABILITY = "/forge/observability";

/** `/o/<segment>`: the root of every path in that org, and its home. */
export function orgRootPath(segment: string): string {
  return `${ORG_PREFIX}${encodeURIComponent(segment)}`;
}

/**
 * The org-relative form of an org-less path: the path itself, except the two
 * console paths that change shape. `/` is the org's home (""), so the org's
 * root path needs no trailing slash.
 */
export function orgFormOf(path: string): string {
  if (path === "/") return "";
  if (path.startsWith(CONSOLE_PREFIX)) return `/apps/${path.slice(CONSOLE_PREFIX.length)}`;
  if (path === ORG_OBSERVABILITY || path.startsWith(`${ORG_OBSERVABILITY}/`)) return path.slice("/forge".length);
  return path;
}

/** `path` (org-less) in the org `segment` names, whatever the flag says. */
export function inOrg(segment: string, path: string): string {
  return `${orgRootPath(segment)}${orgFormOf(path)}`;
}

/** The segment of the org a link goes to, or null for today's form. */
function linkOrgSegment(ctx?: PathContext): string | null {
  if (ctx?.org === null || !orgLinksEnabled()) return null;
  if (ctx?.org) return ctx.org;
  const routeOrg = currentRouteOrg();
  if (routeOrg) return orgSegment(routeOrg);
  return orgSegmentOf(getActiveOrgId());
}

/** `path` in the org `ctx` names (or the current one): the org form with the flag on, else `path` itself. */
export function withOrg(path: string, ctx?: PathContext): string {
  const segment = linkOrgSegment(ctx);
  return segment ? inOrg(segment, path) : path;
}

/**
 * The org segment of `pathname`, decoded, or null for a path outside the org
 * tree. `/o/<id>` gives the id: the layout redirects it to the slug form.
 */
export function orgOfPath(pathname: string): string | null {
  if (!pathname.startsWith(ORG_PREFIX)) return null;
  const raw = pathname.slice(ORG_PREFIX.length).split("/", 1)[0];
  if (!raw) return null;
  try {
    return decodeURIComponent(raw);
  } catch {
    return raw;
  }
}

/**
 * `pathname` in its org-less form: the path the same page has with the flag
 * off (the inverse of inOrg). A path outside the org tree is returned as it
 * is, so a matcher that reads every pathname through this serves both forms.
 */
export function orgFreePath(pathname: string): string {
  if (orgOfPath(pathname) === null) return pathname;
  const rest = pathname.slice(ORG_PREFIX.length);
  const slash = rest.indexOf("/");
  const tail = slash < 0 ? "" : rest.slice(slash);
  if (tail === "" || tail === "/") return "/";
  const app = /^\/apps\/([^/]+)(\/.*)?$/.exec(tail);
  if (app) return `${CONSOLE_PREFIX}${app[1]}${app[2] ?? ""}`;
  if (tail === "/observability" || tail.startsWith("/observability/")) return `/forge${tail}`;
  return tail;
}

/**
 * A built path (`built`, either form) in the org `pathname` is in: its org
 * form under `/o/<org>`, else its org-less form. For a redirect a route
 * computes while a navigation loads, when the route on screen (which the
 * builders read) may be another org's.
 */
export function inOrgOfPath(pathname: string, built: string): string {
  const path = orgFreePath(built);
  const segment = orgOfPath(pathname);
  return segment === null ? path : inOrg(segment, path);
}

/** A TanStack route id with the org layout's segment removed: `/o/$org/_app` → `/_app`. */
export function orgFreeRouteId(routeId: string): string {
  return routeId.startsWith("/o/$org/") ? routeId.slice("/o/$org".length) : routeId;
}

/**
 * The page the org switcher opens: `pathname`'s page in the org `segment`
 * names, minus what belongs to the old org (a project, an app, a run, a saved
 * workflow, an automation), whose list it lands on instead. Always the org
 * form: the switcher is only offered this target on a page under `/o/<org>`.
 */
export function orgSwitchPath(pathname: string, segment: string): string {
  const path = orgFreePath(pathname);
  if (path.startsWith("/project/")) return inOrg(segment, "/");
  if (path.startsWith(CONSOLE_PREFIX)) return inOrg(segment, "/apps");
  if (path.startsWith("/workflows/runs/")) return inOrg(segment, "/workflows/runs");
  // A built-in's page belongs to no org: it stays. A saved workflow is the old org's.
  if (path.startsWith("/workflows/library/") && !isBuiltinSegment(path.slice("/workflows/library/".length))) return inOrg(segment, "/workflows/library");
  if (path.startsWith("/workflows/automations/")) return inOrg(segment, "/workflows/library");
  if (path.startsWith("/workflow/") && path !== "/workflow/new") return inOrg(segment, "/workflows/library");
  return inOrg(segment, path);
}

/** Whether a percent-encoded path segment names a built-in workflow (`builtin://…`). */
function isBuiltinSegment(segment: string): boolean {
  try {
    return decodeURIComponent(segment).startsWith("builtin://");
  } catch {
    return false;
  }
}
