// Copyright (c) 2025 Reliant Labs

/**
 * THE ORG-LESS PATHS THAT FORWARD INTO AN ORG, in one table (control-plane
 * docs/design/unified-shell-and-projects.md §3.4, §12).
 *
 * With the flag `web.org_routes` on, an org-scoped page's org-less path — an
 * old bookmark, a link built before the org list answered, `/` itself —
 * redirects into the tab's org (§3.3), keeping the rest of the path and the
 * query (lib/orgRouting.ts, forwardIntoOrg). With the flag off nothing here
 * runs.
 *
 * What does NOT forward, and stays where it is, each for a reason:
 *
 *   - links that name a resource by id or by app name: `/project/<id>`,
 *     `/workflows/runs/<id>`, `/workflows/automations/<id>`, `/forge/p/<app>`,
 *     `/forge?project=`, `/forge/env/<env>?forgeProject=`, and the org
 *     Observability with an app in its query. The resource's org decides
 *     where they go, not the tab's (§3.4, D8): the resolver (U4) answers that.
 *     Until it ships they open as they always have.
 *   - pages that are not org-scoped yet: Settings and machines (U8), the
 *     mobile tree (U15), onboarding (R11); and the never-scoped ones (sign-in,
 *     OAuth, claims, invitations).
 *   - the retired paths (`/workflow`, `/runs`, `/automations`, …): they
 *     redirect to a current path first, which forwards from there.
 */

/** Exactly these paths forward as they are. */
const FORWARDED = new Set([
  "/",
  "/inbox",
  "/apps",
  "/sites",
  "/usage",
  "/workflows",
  "/workflows/library",
  "/workflows/runs",
  "/workflows/automations",
  "/workflow/new",
]);

/** The query keys that name an app: a link carrying one is the resolver's (U4). */
const APP_KEYS = ["project", "forgeProject"];

/**
 * The org-less path `pathname` forwards to in the tab's org, or null when it
 * stays where it is. The caller puts it in the org (lib/paths/org.ts, inOrg)
 * and keeps the query and hash.
 */
export function legacyOrgForward(pathname: string, search: Record<string, unknown> = {}): string | null {
  const path = pathname.replace(/\/+$/, "") || "/";
  if (FORWARDED.has(path)) return path;
  // One workflow's page (a built-in's ref, or a saved workflow's name) and the builder.
  if (/^\/workflows\/library\/[^/]+$/.test(path)) return path;
  if (/^\/workflow\/[^/]+$/.test(path)) return path;
  // The org's Observability, unless the link names an app.
  if (path === "/forge/observability" || /^\/forge\/observability\/[^/]+$/.test(path)) {
    return APP_KEYS.some((key) => search[key] !== undefined && search[key] !== "") ? null : path;
  }
  return null;
}
