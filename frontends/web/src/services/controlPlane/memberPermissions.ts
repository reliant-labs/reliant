/**
 * Cloud-only wrappers around the member-permission half of
 * `controlplane.v1.AccessTokenService`.
 *
 * WHAT THESE ARE. A per-(org, user, permission) grant decides what a PERSON may
 * do in an organization, and therefore the most a token acting as them may
 * carry. They replaced a single admin check, so "admin" is now a PRESET — a
 * name for holding the whole set — rather than a level that implies authority.
 *
 * A grant is either ORG-WIDE (`scopes`) or BOUND TO ONE ENVIRONMENT
 * (`environmentGrants`; control-plane docs/design/orgs-and-members.md §4). What
 * a member may do on an environment is the union of the two, so a prod-only
 * release manager holds `deploy:write` bound to prod and nothing wider.
 *
 * THE CLIP IS SERVER-SIDE AND THIS IS NOT IT. `grantable` exists so the UI can
 * disable what the caller could not hand out anyway, which is a courtesy, not a
 * control: the server refuses a permission the caller does not hold regardless
 * of what this module sends.
 */

import { Code, ConnectError } from "@connectrpc/connect";

import { AccessTokenService } from "@/gen/controlplane/services/access_token/v1/access_token_pb";
import { errorScopes, hasErrorReason } from "@/lib/errorReasons";
import { getControlPlaneClient } from "./client";

/** Permissions bound to one environment. */
export interface EnvironmentGrant {
  environmentId: string;
  scopes: string[];
}

/** One member and the org permissions they hold. */
export interface MemberPermissions {
  userId: string;
  email: string;
  /** Display only. No permission check consults a role any more. */
  role: string;
  /** Held org-wide: on every environment, and for everything that has none. */
  scopes: string[];
  /** Held on one environment only, one entry per environment with any. */
  environmentGrants: EnvironmentGrant[];
}

/** An environment a permission can be bound to: a column of the grid. */
export interface GrantEnvironment {
  id: string;
  name: string;
  /** Secret writes there need a step-up. Display only. */
  protected: boolean;
  /** What the CALLER may grant on this environment. */
  grantable: string[];
}

export interface MemberPermissionsList {
  members: MemberPermissions[];
  /** What the CALLER may grant org-wide — their own org-wide permission set. */
  grantable: string[];
  /** The org's live environments. Empty from a server older than §4. */
  environments: GrantEnvironment[];
  /** The permissions that may be bound to one environment; the rest are org-wide only. */
  environmentScopes: string[];
}

type WireMember = {
  userId: string;
  email: string;
  role: string;
  scopes: string[];
  environmentGrants?: { environmentId: string; scopes: string[] }[];
};

function fromWire(m: WireMember): MemberPermissions {
  return {
    userId: m.userId,
    email: m.email,
    role: m.role,
    scopes: [...m.scopes],
    environmentGrants: (m.environmentGrants ?? []).map((g) => ({
      environmentId: g.environmentId,
      scopes: [...g.scopes],
    })),
  };
}

export async function listMemberPermissions(orgId: string): Promise<MemberPermissionsList> {
  const res = await getControlPlaneClient(AccessTokenService).listOrgMemberGrants({ orgId });
  return {
    members: res.members.map(fromWire),
    grantable: [...res.grantable],
    environments: res.environments.map((e) => ({
      id: e.id,
      name: e.name,
      protected: e.protected,
      grantable: [...e.grantable],
    })),
    environmentScopes: [...res.environmentScopes],
  };
}

/**
 * updateMemberPermissions replaces one member's COMPLETE permission set: the
 * org-wide `scopes` and every environment-bound grant.
 *
 * Not a delta: a permission absent from `scopes`, or from an environment's
 * entry, is revoked, and an environment absent from `environmentGrants` holds
 * nothing afterwards. That is what makes the call idempotent, and it means two
 * admins editing the same member cannot merge into a combination neither of
 * them chose.
 *
 * `environment_grants` is ALWAYS sent, even empty. Leaving it unset is how an
 * older client says "I know nothing of environments, leave them alone", and a
 * client that renders environment grants must never claim that.
 */
export async function updateMemberPermissions(
  userId: string,
  scopes: string[],
  orgId: string,
  environmentGrants: EnvironmentGrant[],
): Promise<MemberPermissions> {
  // Pin the edited roster's org before the transport awaits authentication.
  // This same message is retained by the transport's authentication retry.
  if (!orgId) throw new Error("Choose an organization before editing permissions.");
  const res = await getControlPlaneClient(AccessTokenService).updateOrgMemberGrants({
    userId,
    scopes,
    orgId,
    environmentGrants: {
      // An environment with nothing bound is the same as one left out; the
      // server refuses neither, but an empty entry says nothing.
      environments: environmentGrants
        .filter((g) => g.scopes.length > 0)
        .map((g) => ({ environmentId: g.environmentId, scopes: g.scopes })),
    },
  });
  const member = res.member;
  if (!member) {
    return { userId, email: "", role: "", scopes: [...scopes], environmentGrants };
  }
  return fromWire(member);
}

/**
 * The words for a refused edit. The server's own message is written for a
 * person (it names the missing permission, the unbindable scope, or the
 * environment that is not in the org), so it is shown rather than replaced.
 * The one refusal reworded is the ceiling: "you can only grant what you hold"
 * is the rule the operator needs, not just the scope they lack.
 */
export function memberPermissionErrorMessage(err: unknown, email: string): string {
  const prefix = `Could not update ${email}'s permissions`;
  if (hasErrorReason(err, "scope_missing")) {
    const missing = errorScopes(err);
    if (missing.length === 1 && missing[0] === "token:write") {
      return `${prefix}: changing permissions needs token:write, which you don't hold.`;
    }
    const named = missing.length > 0 ? missing.join(", ") : "one of these permissions";
    return `${prefix}: you can only grant what you hold yourself, org-wide or on that environment, and you don't hold ${named}.`;
  }
  const ce = ConnectError.from(err);
  switch (ce.code) {
    case Code.PermissionDenied:
    case Code.InvalidArgument:
    case Code.FailedPrecondition:
    case Code.NotFound:
      return ce.rawMessage ? `${prefix}: ${ce.rawMessage}` : `${prefix}.`;
    default:
      return `${prefix}. Nothing was changed; try again.`;
  }
}
