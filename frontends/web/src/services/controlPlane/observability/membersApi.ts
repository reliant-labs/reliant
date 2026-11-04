// Copyright (c) 2025 Reliant Labs

/** The issue org's assignable people and the reader's role in that org, never their default org. */

import { IssuesService } from "@/gen/controlplane/services/issues/v1/issues_pb";
import { OrganizationService } from "@/gen/controlplane/services/organization/v1/organization_pb";
import { UserService } from "@/gen/controlplane/services/user/v1/user_pb";

import { getControlPlaneClient } from "../client";
import type { IssueUser } from "./issuesTypes";

export interface IssueRoster {
  users: IssueUser[];
  /** Only the CALLER's role, selected by the explicit org. Unknown/refused fails closed. */
  callerRole: string | null;
}

export async function listOrgMembers(orgId: string): Promise<IssueRoster> {
  if (!orgId) throw new Error("An explicit issue organization is required");
  // Membership lookup is a hint for admin controls, not a prerequisite for the roster.
  // It rejects machine credentials; that must never hide otherwise assignable people.
  const membership = getControlPlaneClient(OrganizationService).listMyOrganizations({})
    .then((res) => res.organizations.find((org) => org.id === orgId)?.role ?? null, () => null);
  const users = new Map<string, IssueUser>();
  let pageToken = "";
  do {
    const page = await getControlPlaneClient(IssuesService).listAssignableUsers({ orgId, pageSize: 100, pageToken });
    for (const user of page.users) users.set(user.id, { id: user.id, name: user.name || user.id });
    pageToken = page.nextPageToken;
  } while (pageToken);
  return { users: [...users.values()], callerRole: await membership };
}

/** The signed-in person, as an issue would name them. Null when the server names nobody (a credential that acts for no person). */
export async function getCurrentIssueUser(): Promise<IssueUser | null> {
  const res = await getControlPlaneClient(UserService).getCurrentUser({});
  if (!res.user?.id) return null;
  return { id: res.user.id, name: res.user.name || res.user.email || res.user.id };
}
