// Copyright (c) 2025 Reliant Labs

/**
 * The caller's organizations (controlplane.v1.OrganizationService, contract in
 * control-plane docs/orgs.md). A person can belong to several orgs; every
 * org-scoped control-plane request acts in exactly one of them, named by its
 * `org_id` field (api/orgInterceptor.ts fills it from the active org).
 *
 * Human sessions only: a machine credential has no orgs to choose between, so
 * these refuse it and the web (which then has no active org) names none.
 */

import { OrganizationService } from "@/gen/controlplane/services/organization/v1/organization_pb";
import { queryClient } from "@/lib/query-client";
import { switchTabOrg } from "@/lib/orgSwitch";
import { whenRouteOrgSettled } from "@/lib/routeOrg";
import { learnOrgSegments, useActiveOrgStore } from "@/store/activeOrgStore";
import { getControlPlaneClient } from "./client";

export const orgKeys = {
  mine: ["orgs", "mine"] as const,
};

export interface MyOrg {
  id: string;
  name: string;
  slug: string;
  /** The caller's role in it: "owner", "admin" or "member". */
  role: string;
  isOwner: boolean;
  /** The org requests that name none act in (the server-stored preference). */
  isDefault: boolean;
}

export interface MyOrgs {
  orgs: MyOrg[];
  /** "" when the person belongs to none yet (a signup still provisioning). */
  defaultOrgId: string;
}

/** ListMyOrganizations: every org the caller belongs to, oldest first. */
export async function listMyOrganizations(): Promise<MyOrgs> {
  const res = await getControlPlaneClient(OrganizationService).listMyOrganizations({});
  return {
    orgs: res.organizations.map((o) => ({
      id: o.id,
      name: o.name,
      slug: o.slug,
      role: o.role,
      isOwner: o.isOwner,
      isDefault: o.isDefault,
    })),
    defaultOrgId: res.defaultOrgId,
  };
}

/**
 * SetDefaultOrganization: the org requests that name none act in, on every
 * device and client. PermissionDenied for an org the caller is not in.
 */
export async function setDefaultOrganization(orgId: string): Promise<void> {
  await getControlPlaneClient(OrganizationService).setDefaultOrganization({ orgId });
}

/**
 * The org the web acts in, given the orgs the person belongs to and what they
 * chose before: the choice if they still belong to it, else the server's
 * default. There is no third rule: never "the first org in the list". `null`
 * only when the person belongs to none (a signup still provisioning).
 */
export function resolveActiveOrg(orgs: MyOrgs, chosenId: string | null): string | null {
  if (chosenId && orgs.orgs.some((o) => o.id === chosenId)) return chosenId;
  if (orgs.defaultOrgId && orgs.orgs.some((o) => o.id === orgs.defaultOrgId)) return orgs.defaultOrgId;
  return orgs.orgs.find((o) => o.isDefault)?.id ?? null;
}

/**
 * The org every identity and observability request is scoped by (they take a
 * REQUIRED org_id, so they cannot leave it to the interceptor).
 *
 * On an org page it is the committed route's org (lib/routeOrg.ts), the one
 * authority query keys and the interceptor read too: null on an org page that
 * names no org of theirs, and a page load that starts on an org URL waits for
 * its first commit. Outside the org tree it is the tab's org, resolved against
 * the live membership list the switcher reads (sharing its cache entry, so it
 * costs no extra request).
 */
export async function currentOrgId(): Promise<string | null> {
  const route = await whenRouteOrgSettled();
  if (route.kind === "member") return route.org.id;
  if (route.kind !== "legacy") return null;
  const mine = await queryClient.fetchQuery({ queryKey: orgKeys.mine, queryFn: listMyOrganizations, staleTime: 60_000 });
  return resolveActiveOrg(mine, useActiveOrgStore.getState().chosenId);
}

/**
 * Act in an org the person has just joined (a claimed site's org, one they
 * created). The membership list is read afresh FIRST: the binding forgets a
 * choice that is not in the list it holds, so choosing before the refetch
 * would be undone. View-level only, like the switcher: the server-side
 * default is untouched. On an org page that is a navigation into it
 * (lib/orgSwitch.ts), by its slug once the fresh list names it.
 */
export async function switchToJoinedOrg(orgId: string): Promise<void> {
  await queryClient.invalidateQueries({ queryKey: orgKeys.mine });
  const orgs = queryClient.getQueryData<MyOrgs>(orgKeys.mine)?.orgs ?? [];
  learnOrgSegments(orgs);
  const org = orgs.find((o) => o.id === orgId);
  switchTabOrg({ id: orgId, slug: org?.slug ?? "" });
}
