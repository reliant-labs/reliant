/**
 * Cloud-only wrappers around the member-permission half of
 * `controlplane.v1.AccessTokenService`.
 *
 * WHAT THESE ARE. A per-(org, user, permission) grant decides what a PERSON may
 * do in an organization, and therefore the most a token acting as them may
 * carry. They replaced a single admin check, so "admin" is now a PRESET — a
 * name for holding the whole set — rather than a level that implies authority.
 *
 * THE CLIP IS SERVER-SIDE AND THIS IS NOT IT. `grantable` exists so the UI can
 * disable what the caller could not hand out anyway, which is a courtesy, not a
 * control: the server refuses a permission the caller does not hold regardless
 * of what this module sends.
 */

import { AccessTokenService } from "@/gen/controlplane/services/access_token/v1/access_token_pb";
import { getControlPlaneClient } from "./client";

/** One member and the org permissions they hold. */
export interface MemberPermissions {
  userId: string;
  email: string;
  /** Display only. No permission check consults a role any more. */
  role: string;
  scopes: string[];
}

export interface MemberPermissionsList {
  members: MemberPermissions[];
  /** What the CALLER may grant — their own permission set. */
  grantable: string[];
}

export async function listMemberPermissions(): Promise<MemberPermissionsList> {
  const res = await getControlPlaneClient(AccessTokenService).listOrgMemberGrants({});
  return {
    members: res.members.map((m) => ({
      userId: m.userId,
      email: m.email,
      role: m.role,
      scopes: [...m.scopes],
    })),
    grantable: [...res.grantable],
  };
}

/**
 * updateMemberPermissions replaces one member's COMPLETE permission set.
 *
 * Not a delta: a permission absent from `scopes` is revoked. That is what makes
 * the call idempotent, and it means two admins editing the same member cannot
 * merge into a combination neither of them chose.
 */
export async function updateMemberPermissions(
  userId: string,
  scopes: string[]
): Promise<MemberPermissions> {
  const res = await getControlPlaneClient(AccessTokenService).updateOrgMemberGrants({
    userId,
    scopes,
  });
  const member = res.member;
  return {
    userId: member?.userId ?? userId,
    email: member?.email ?? "",
    role: member?.role ?? "",
    scopes: [...(member?.scopes ?? [])],
  };
}
