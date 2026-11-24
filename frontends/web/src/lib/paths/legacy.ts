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
 * Only pages the route tree serves in both forms forward (the table is its
 * inventory, routes.tsx createOrgScopedRoutes). Both hosts mount that one
 * tree (U6), so both forward.
 *
 * An app's console by name, `/forge/p/<app>/…` (and `/apps/<app>/…`, its org
 * form missing the org), forwards too, to `/o/<org>/apps/<app>/…` (owner,
 * U18: every console URL names the org). On the org-less page the console
 * already read the tab's org (the route's scope is "legacy", so requests
 * carry the tab's org), so the forward changes what the URL says, not what
 * the page shows. Which org holds an app of that name is the resolver's
 * (ResolveAppOrgs, U4, design D8); until it ships, the tab's org is the
 * answer, as it already was.
 *
 * What does NOT forward, and stays where it is, each for a reason:
 *
 *   - a chat's (`/c/<id>`, and a thread of it): the chat names its own org
 *     (`Chat.org_id`), and its route's loader redirects there
 *     (lib/chatRouteResolve.ts), not into the tab's.
 *   - a project's (`/project/<id>`, and its page
 *     `/projects/<id>[/settings|/workspaces]`): the project names its own org
 *     (`Project.org_id`), and its routes' loader redirects there
 *     (lib/projectRouteResolve.ts), not into the tab's.
 *   - links that name a resource by id or by app name in a way nothing here
 *     can resolve yet: `/workflows/runs/<id>`, `/workflows/automations/<id>`,
 *     `/forge?project=`, `/forge/env/<env>?forgeProject=`, and the org
 *     Observability with an app in its query. So do saved workflows, by name:
 *     `/workflows/library/<name>` and the builder's `/workflow/<name>` (a
 *     built-in opens there by its bare name too, so the builder's path cannot
 *     tell them apart). The resource's org decides where they go, not the
 *     tab's (§3.4, D8): the resolver (U4) answers that. Until it ships they
 *     open as they always have. A built-in's page (`builtin://…`) and the
 *     library and New workflow belong to no org, and forward.
 *   - pages that are not org-scoped yet: Settings and machines (U8), the
 *     mobile tree (U15), onboarding (R11); and the never-scoped ones (sign-in,
 *     OAuth, claims, invitations).
 *   - the retired paths (`/workflow`, `/runs`, `/automations`, …): they
 *     redirect to a current path first, which forwards from there.
 *
 * Below the forwarding table is the other half of the old URLs: the pages
 * that are gone (RETIRED_PAGES), each a redirect to the page that replaced it.
 */

import { homePath, projectsPath } from "./index";

/** Exactly these paths forward as they are. */
const FORWARDED = new Set([
  "/",
  "/inbox",
  "/apps",
  "/sites",
  "/usage",
  "/connectors",
  "/projects",
  "/workflows",
  "/workflows/library",
  "/workflows/runs",
  "/workflows/automations",
  "/workflow/new",
  // The new-chat view (R3): its draft context is its search, kept by the forward.
  "/new",
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
  // A built-in workflow's page: org-neutral. A saved workflow's is not (see above).
  const ref = /^\/workflows\/library\/([^/]+)$/.exec(path)?.[1];
  if (ref !== undefined && isBuiltinRef(ref)) return path;
  // An app's console by name (see the module comment): `/forge/p/<app>/…`,
  // or `/apps/<app>/…` without its org.
  if (/^\/(forge\/p|apps)\/[^/]+(\/|$)/.test(path)) return path;
  // The org's Observability, unless the link names an app.
  if (path === "/forge/observability" || /^\/forge\/observability\/[^/]+$/.test(path)) {
    return APP_KEYS.some((key) => search[key] !== undefined && search[key] !== "") ? null : path;
  }
  return null;
}

// ── Retired pages ───────────────────────────────────────────────────────────

/**
 * A page that is gone, and the page its links open now. Old links, bookmarks
 * and older Electron builds name these paths, so each stays a route that only
 * redirects (legacyRedirectRoutes.tsx): never a not-found. The request's own
 * query rides along (a claim's `?claimed=`), under the target's.
 */
export interface RetiredPage {
  /** The retired path, org-less (`/apps`). */
  from: string;
  /** The page it opens now, from lib/paths (any org is replaced by the request's own). */
  to: () => string;
  /** Search keys the target adds (the Projects filter). */
  search?: Readonly<Record<string, string>>;
  /**
   * `org-scoped`: an org-scoped page's path, retired in both forms
   * (`/apps` and `/o/<org>/apps`). `root`: a path that only ever existed
   * org-less.
   */
  scope: "org-scoped" | "root";
}

/**
 * Every retired page (U6, one shell). The other retired URLs keep their
 * redirects beside the area that replaced them, where the mapping needs the
 * area's own knowledge: the Workflows area's (`/workflow`, `/runs`,
 * `/automations`, workflowsAreaRoutes.tsx), Settings' section aliases
 * (routeSchemas.ts SETTINGS_SECTION_ALIASES) and the console's
 * (forgeConsoleRoutes.tsx: `/forge`, `/forge/env/<env>`, `/forge/topology`, …).
 * __tests__/legacyUrls.production.test.ts holds every one of them to resolving.
 */
export const RETIRED_PAGES: readonly RetiredPage[] = [
  // Apps and Sites are filters of Projects: one list of what the person has shipped.
  { from: "/apps", to: () => projectsPath(), search: { show: "apps" }, scope: "org-scoped" },
  { from: "/sites", to: () => projectsPath(), search: { show: "sites" }, scope: "org-scoped" },
  // The bare project path: every project's chats.
  { from: "/project", to: () => homePath(), scope: "root" },
  // The forge shell's dev preview: there is one shell, and its home is Projects.
  { from: "/forge-shell-preview", to: () => projectsPath(), scope: "root" },
];

/** Whether a URL segment (percent-encoded) names a built-in workflow (`builtin://…`). */
function isBuiltinRef(segment: string): boolean {
  try {
    return decodeURIComponent(segment).startsWith("builtin://");
  } catch {
    return false;
  }
}
