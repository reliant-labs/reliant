// Copyright (c) 2025 Reliant Labs

/**
 * Who can an issue be assigned to, and who is the reader. The issue console's
 * only two questions about people.
 *
 * There is no roster rpc open to every org member: the one that exists,
 * AccessTokenService.ListOrgMemberGrants, needs token:read and names a member
 * by email. So `listOrgMembers` can be refused (PermissionDenied) for an
 * ordinary member, and the assignee control then offers only "me" and the
 * current assignee, which it can name without the roster.
 */

import { AccessTokenService } from "@/gen/controlplane/services/access_token/v1/access_token_pb";
import { UserService } from "@/gen/controlplane/services/user/v1/user_pb";

import { getControlPlaneClient } from "../client";
import type { IssueUser } from "./issuesTypes";

/** An org member as the assignee picker lists them. `name` is their email: the roster carries no display name. */
export interface OrgMember extends IssueUser {
  /** owner, admin or member: which of them may delete another person's comment. */
  role: string;
}

export async function listOrgMembers(): Promise<OrgMember[]> {
  const res = await getControlPlaneClient(AccessTokenService).listOrgMemberGrants({});
  return res.members.map((m) => ({ id: m.userId, name: m.email || m.userId, role: m.role }));
}

/** The signed-in person, as an issue would name them. Null when the server names nobody (a credential that acts for no person). */
export async function getCurrentIssueUser(): Promise<IssueUser | null> {
  const res = await getControlPlaneClient(UserService).getCurrentUser({});
  if (!res.user?.id) return null;
  return { id: res.user.id, name: res.user.name || res.user.email || res.user.id };
}
