// Copyright (c) 2025 Reliant Labs

/**
 * THE ORG SEGMENT OF AN APP PATH: the one place it is added.
 *
 * The unified-shell design (control-plane docs/design/unified-shell-and-projects.md
 * §3) moves every org-scoped page under `/o/<org slug>/…`, behind the runtime
 * flag `web.org_routes` (D12): off, the builders emit today's paths; on, the
 * org form. U1 (this) routes every org-scoped builder through `withOrg`, which
 * is the identity, so no URL changes. U3 gives it a body:
 *
 *   - the org is `ctx.org` when the caller names one (the switcher: the same
 *     page in another org), else the org of the current route match;
 *   - with the flag on, the path gains `/o/<slug>` in front.
 *
 * Paths whose org form is not today's path behind a prefix (the console's
 * `/forge/p/<app>` → `/o/<org>/apps/<app>`) are rewritten by their own
 * builder beside its call to `withOrg`, so U3's edits stay inside lib/paths
 * and Console/paths.ts.
 *
 * Not org-scoped, so never passed through here: sign-in and the OAuth pages,
 * invitations and claims (`/accept-invite` IS the org choice), and the
 * personal settings. Settings' org sections (D10, U8), machines (U8) and the
 * mobile tree (D11, U15) are not wrapped yet either: each joins `withOrg` in
 * the PR that mounts its `/o/<org>` route, so a link never points at a route
 * that does not exist.
 */

/** Which org a path is built for. Optional everywhere: the default is the tab's own org. */
export interface PathContext {
  /** The org's slug (`MyOrganization.slug`), for a link into a different org than the current page's. */
  org?: string;
}

/** `path` in the org `ctx` names (or the current one). The identity until U3 mounts `/o/$org`. */
export function withOrg(path: string, ctx?: PathContext): string {
  void ctx;
  return path;
}
