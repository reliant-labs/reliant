// Copyright (c) 2025 Reliant Labs

import { loadConfig } from "./config_gen";

/**
 * The runtime flag `web.org_routes` (control-plane
 * docs/design/unified-shell-and-projects.md, D12): the web config's
 * ORG_ROUTES, rendered per environment into config.js, so turning it on or off
 * is a config change, never a rebuild.
 *
 *   on   the path builders (lib/paths) emit the org form, `/o/<org slug>/…`,
 *        and the org-less path of an org-scoped page redirects into the tab's
 *        org (lib/orgRouting.ts, forwardIntoOrg), a chat's or a project's into
 *        the resource's own (lib/chatRouteResolve.ts, lib/projectRouteResolve.ts).
 *   off  (the default) the builders emit today's paths and nothing redirects.
 *
 * Either way the `/o/<org>` routes resolve, so an org URL never 404s: turning
 * the flag off is the rollback, and every link printed in either form keeps
 * working.
 *
 * Read at call time, never at module scope: tests set the document per case.
 */
export function isOrgRoutesEnabled(): boolean {
  try {
    return loadConfig().ORG_ROUTES === "true";
  } catch {
    // A document that does not validate stops the app at boot (configReady);
    // a path builder asked first answers with today's paths.
    return false;
  }
}

/**
 * Whether this page load's links take the org form: the flag. Both hosts
 * mount the one route tree (routes.tsx), with every org page, so the forge
 * host's links follow it too (U6).
 */
export function orgLinksEnabled(): boolean {
  return isOrgRoutesEnabled();
}
