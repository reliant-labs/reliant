// Copyright (c) 2025 Reliant Labs

/**
 * WHAT A PROJECT URL OPENS: the loader of the project routes (routes.tsx,
 * `/project/$projectId` and `/projects/$projectId[/settings|/workspaces]`,
 * both forms), per control-plane docs/design/unified-shell-and-projects.md
 * §3.4 (D8), beside lib/chatRouteResolve.ts, whose rules it follows:
 *
 *   the project, in the URL's org   the page, as it is
 *   the project, in another org     the resource's org wins: redirect (replace)
 *                                   to the same URL in the project's org, with a
 *                                   notice, when the person is a member there;
 *                                   otherwise not found (the page's own "No such
 *                                   project in this organization"). A project is
 *                                   never shown under an org it is not in.
 *   no such project, or not theirs  not found, in place
 *   a read failed                   the page, as it is: it reads the same org
 *                                   list, and shows that failure with a Retry
 *
 * The org-less form opens as it is with the flag `web.org_routes` off; on, it
 * redirects into the project's org, keeping the query and the hash, with a
 * notice when that moves the tab's org (lib/orgArrivalNotice.ts).
 *
 * The project's org is found without a resolver rpc (`ResolveResourceOrg`,
 * U4, is not built): the URL's org list first (the tab's, for the org-less
 * form), which the page then reads from the same cache entry; else any org's
 * list the cache holds; else GetProject, which is not org-scoped (the server
 * answers a project's owner, or a viewer it is shared with, in any org). A
 * project from before orgs is in its owner's default org.
 *
 * Writes nothing a page reads: a load can be superseded and never commit.
 */

import { redirect } from "@tanstack/react-router";

import { isProjectInOrgList, lookupProjectOrg } from "@/hooks/project-queries";
import { noteResourceOrgRedirect } from "@/lib/orgArrivalNotice";
import { orgLinksEnabled } from "@/lib/orgRoutesFlag";
import { defaultOrgRoutingDeps, type LocationLike, type OrgRoutingDeps } from "@/lib/orgRouting";
import { inOrg, orgFreePath } from "@/lib/paths/org";
import type { OrgRouteState } from "@/lib/routeOrg";
import { resolveActiveOrg, type MyOrg, type MyOrgs } from "@/services/controlPlane/orgs";
import { bindActiveOrgUser, isMachineCredentialUser, useActiveOrgStore } from "@/store/activeOrgStore";

/** What a project URL resolved to: the project routes' loader data. */
export type ProjectRouteData =
  /** The project is in the org the page is in (or, org-less, opens where it is). */
  | { kind: "project"; projectId: string }
  /** No such project, or not one the person may read here. */
  | { kind: "missing"; projectId: string }
  /** Nobody signed in, the org layout shows its own page, the flag is off, or a read failed: the page decides. */
  | { kind: "unresolved" };

export interface ProjectRouteDeps extends OrgRoutingDeps {
  /** Whether the project is in the org's project list. Throws when the list cannot be read. */
  inOrgList: (projectId: string, orgId: string) => Promise<boolean>;
  /** The project's `org_id` ("" before orgs), or null when there is none for this person. Throws on any other failure. */
  projectOrg: (projectId: string) => Promise<string | null>;
}

export const defaultProjectRouteDeps: ProjectRouteDeps = {
  ...defaultOrgRoutingDeps,
  inOrgList: isProjectInOrgList,
  projectOrg: lookupProjectOrg,
};

/** The notice for a project URL redirected into the project's org. */
export function movedProjectNotice(orgName: string): string {
  return `This project is in ${orgName}, so it opened there.`;
}

function suffixOf(location: LocationLike): string {
  return `${location.searchStr ?? ""}${location.hash ? `#${location.hash}` : ""}`;
}

/** Redirect (replace) to `location`'s page in `org`, leaving the notice for the page it lands on. */
function redirectIntoOrg(org: MyOrg, location: LocationLike, { always }: { always: boolean }): never {
  const href = `${inOrg(org.slug || org.id, orgFreePath(location.pathname))}${suffixOf(location)}`;
  noteResourceOrgRedirect(org.id, href, movedProjectNotice(org.name || org.slug || org.id), { always });
  throw redirect({ href, replace: true });
}

/**
 * The org the project is in, by id: `listed` when it is in that org's list,
 * else what the project says (its owner's default before orgs). Null: no such
 * project for this person. Throws when a read fails.
 */
async function projectOrgId(projectId: string, listed: string | null, mine: MyOrgs, deps: ProjectRouteDeps): Promise<string | null> {
  if (listed && (await deps.inOrgList(projectId, listed))) return listed;
  const orgId = await deps.projectOrg(projectId);
  if (orgId === null) return null;
  return orgId || mine.defaultOrgId;
}

/**
 * The project routes' loader: see the module comment. `orgRoute` is the org
 * layout's answer (its context), absent for the org-less form. Throws the
 * redirects.
 */
export async function resolveProjectRoute(
  projectId: string,
  location: LocationLike,
  { orgRoute, deps = defaultProjectRouteDeps }: { orgRoute?: OrgRouteState; deps?: ProjectRouteDeps } = {},
): Promise<ProjectRouteData> {
  // Under an org the person is not in (or not signed in to), the layout renders its own page.
  if (orgRoute && orgRoute.kind !== "member") return { kind: "unresolved" };
  const routeOrg = orgRoute?.kind === "member" ? orgRoute.org : null;
  // The org-less form moves only once links carry the org.
  if (!routeOrg && (!orgLinksEnabled() || !deps.hasOrgs())) return { kind: "unresolved" };

  const userId = await deps.signedInUser();
  if (!userId || isMachineCredentialUser(userId)) return { kind: "unresolved" };

  let mine: MyOrgs;
  let orgId: string | null;
  try {
    mine = await deps.listOrgs();
    let listed = routeOrg?.id ?? null;
    if (!routeOrg) {
      // The org-less form: most likely in the tab's org, whose list its page reads anyway.
      bindActiveOrgUser(userId);
      listed = resolveActiveOrg(mine, useActiveOrgStore.getState().chosenId);
    }
    orgId = await projectOrgId(projectId, listed, mine, deps);
  } catch {
    // The page reads the same lists and says what failed.
    return { kind: "unresolved" };
  }
  if (orgId === null) return { kind: "missing", projectId };

  const org = mine.orgs.find((o) => o.id === orgId) ?? null;
  if (routeOrg) {
    // An unstamped project of a person with no default org yet: the route's org is all there is.
    if (!orgId || orgId === routeOrg.id) return { kind: "project", projectId };
    // Readable, but in an org the person is not in: not under this one either.
    if (!org) return { kind: "missing", projectId };
    redirectIntoOrg(org, location, { always: true });
  }
  // The org-less form: into the project's org, said once the page commits if that moved the tab.
  if (!org) return { kind: "project", projectId };
  redirectIntoOrg(org, location, { always: false });
}
