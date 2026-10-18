// Copyright (c) 2025 Reliant Labs

/**
 * control-plane `controlplane.v1.IdentityService`, mapped into the console's
 * model. The only file that knows its wire shape.
 *
 * WHAT IT IS. An environment's managed identity is a Zitadel instance the
 * control plane runs for it. admin-server relays every call to the identity
 * reconciler, which alone maps (org, environment) to an instance — no instance
 * id, key or secret ever reaches the browser, in either direction.
 *
 * WHO MAY DO WHAT. Every RPC carries the caller's current org, which the
 * server verifies against membership first. Any member may read the status
 * and the users; every verb that changes something needs the org admin role.
 * GetIdentityStatus says which of the two the caller is (`callerCanManage`),
 * so the tab can hide what a member would be refused — the server checks
 * again on every call regardless.
 *
 * DECLARED ROLES are converged from the environment's KCL and are read-only
 * here: the invite, role and delete verbs refuse to touch them.
 */

import type { Timestamp } from "@bufbuild/protobuf/wkt";

import {
  IdentityDeliveryState as WireDeliveryState,
  IdentityState as WireState,
  IdentityUserState as WireUserState,
  type IdentityUser as WireUser,
} from "@/gen/controlplane/controlplane/v1/identity_pb";
import { IdentityService } from "@/gen/controlplane/services/identity/v1/identity_pb";

import { getControlPlaneClient } from "../client";
import { getWalletOverview } from "../reliantAI";

function client() {
  return getControlPlaneClient(IdentityService);
}

function msOf(ts: Timestamp | undefined): number | null {
  if (!ts) return null;
  return Number(ts.seconds) * 1000 + Math.round(ts.nanos / 1e6);
}

// ── Model ────────────────────────────────────────────────────────────────────

export type IdentityState =
  | "pending"
  | "creating"
  | "provisioning"
  | "ready"
  | "degraded"
  | "failing"
  | "withdrawn"
  | "grace"
  | "deleting"
  | "deleted"
  | "unknown";

export type IdentityDeliveryState = "delivered" | "pending" | "failing" | "unbound" | "withdrawn" | "disabled" | "unknown";

export type IdentityUserState = "invited" | "active" | "locked" | "disabled" | "unknown";

export interface IdentityApp {
  name: string;
  /** Not a secret. */
  clientId: string;
  /** When the app's broker token expires and renewal is due; null for an app without a broker. */
  brokerExpiresAtMs: number | null;
}

export interface IdentityDelivery {
  kind: string;
  cluster: string;
  namespace: string;
  name: string;
  state: IdentityDeliveryState;
  lastError: string;
  lastVerifiedAtMs: number | null;
}

export interface IdentityStatus {
  /** False when the environment declares no identity, or its declaration was refused (`refused`). */
  hasInstance: boolean;
  state: IdentityState;
  issuer: string;
  domain: string;
  public: boolean;
  adopted: boolean;
  apps: IdentityApp[];
  deliveries: IdentityDelivery[];
  /** The environment that owns the instance; this one's own id unless it shares another's. */
  ownerEnvironmentId: string;
  sharerEnvironmentIds: string[];
  lastReconciledAtMs: number | null;
  nextAttemptAtMs: number | null;
  lastError: string;
  failures: number;
  graceUntilMs: number | null;
  exportUrlAvailable: boolean;
  refused: string;
  /** Whether the CALLER may use the mutating verbs here. A courtesy; the server checks again. */
  callerCanManage: boolean;
  /** Whether this deployment can export identities at all. */
  exportEnabled: boolean;
}

export interface IdentityRole {
  key: string;
  displayName: string;
  group: string;
  /** Converged from KCL (`members_from = declared`): read-only in the console. */
  declared: boolean;
}

export interface IdentityUser {
  userId: string;
  email: string;
  displayName: string;
  state: IdentityUserState;
  /** Declared and console-managed alike. */
  roles: string[];
  createdAtMs: number | null;
}

export interface IdentityUsersPage {
  users: IdentityUser[];
  nextPageToken: string;
  roles: IdentityRole[];
}

export interface IdentityUserFilter {
  /** Case-insensitive substring of the email address. */
  query?: string;
  state?: Exclude<IdentityUserState, "unknown">;
}

export interface IdentityExportURL {
  url: string;
  expiresAtMs: number | null;
}

// ── Enums ────────────────────────────────────────────────────────────────────

const STATE: Record<number, IdentityState> = {
  [WireState.PENDING]: "pending",
  [WireState.CREATING]: "creating",
  [WireState.PROVISIONING]: "provisioning",
  [WireState.READY]: "ready",
  [WireState.DEGRADED]: "degraded",
  [WireState.FAILING]: "failing",
  [WireState.WITHDRAWN]: "withdrawn",
  [WireState.GRACE]: "grace",
  [WireState.DELETING]: "deleting",
  [WireState.DELETED]: "deleted",
};

const DELIVERY_STATE: Record<number, IdentityDeliveryState> = {
  [WireDeliveryState.DELIVERED]: "delivered",
  [WireDeliveryState.PENDING]: "pending",
  [WireDeliveryState.FAILING]: "failing",
  [WireDeliveryState.UNBOUND]: "unbound",
  [WireDeliveryState.WITHDRAWN]: "withdrawn",
  [WireDeliveryState.DISABLED]: "disabled",
};

const USER_STATE: Record<number, IdentityUserState> = {
  [WireUserState.INVITED]: "invited",
  [WireUserState.ACTIVE]: "active",
  [WireUserState.LOCKED]: "locked",
  [WireUserState.DISABLED]: "disabled",
};

const USER_STATE_TO_WIRE: Record<Exclude<IdentityUserState, "unknown">, WireUserState> = {
  invited: WireUserState.INVITED,
  active: WireUserState.ACTIVE,
  locked: WireUserState.LOCKED,
  disabled: WireUserState.DISABLED,
};

function mapUser(u: WireUser): IdentityUser {
  return {
    userId: u.userId,
    email: u.email,
    displayName: u.displayName,
    state: USER_STATE[u.state] ?? "unknown",
    roles: [...u.roles],
    createdAtMs: msOf(u.createdAt),
  };
}

// ── RPCs ─────────────────────────────────────────────────────────────────────

/** The org every identity request is scoped by: the caller's current one, as the billing surfaces resolve it. */
export async function currentOrgId(): Promise<string | null> {
  return (await getWalletOverview())?.organization?.id ?? null;
}

export async function getIdentityStatus(orgId: string, environmentId: string): Promise<IdentityStatus> {
  const r = await client().getIdentityStatus({ orgId, environmentId });
  return {
    hasInstance: r.hasInstance,
    state: STATE[r.state] ?? "unknown",
    issuer: r.issuer,
    domain: r.domain,
    public: r.public,
    adopted: r.adopted,
    apps: r.apps.map((a) => ({ name: a.name, clientId: a.clientId, brokerExpiresAtMs: msOf(a.brokerExpiresAt) })),
    deliveries: r.deliveries.map((d) => ({
      kind: d.kind,
      cluster: d.cluster,
      namespace: d.namespace,
      name: d.name,
      state: DELIVERY_STATE[d.state] ?? "unknown",
      lastError: d.lastError,
      lastVerifiedAtMs: msOf(d.lastVerifiedAt),
    })),
    ownerEnvironmentId: r.ownerEnvironmentId,
    sharerEnvironmentIds: [...r.sharerEnvironmentIds],
    lastReconciledAtMs: msOf(r.lastReconciledAt),
    nextAttemptAtMs: msOf(r.nextAttemptAt),
    lastError: r.lastError,
    failures: r.failures,
    graceUntilMs: msOf(r.graceUntil),
    exportUrlAvailable: r.exportUrlAvailable,
    refused: r.refused,
    callerCanManage: r.callerCanManage,
    exportEnabled: r.exportEnabled,
  };
}

export const USERS_PAGE_SIZE = 50;

export async function listIdentityUsers(
  orgId: string,
  environmentId: string,
  filter: IdentityUserFilter,
  pageToken: string
): Promise<IdentityUsersPage> {
  const query = filter.query?.trim();
  const r = await client().listUsers({
    orgId,
    environmentId,
    query: query ? query : undefined,
    state: filter.state ? USER_STATE_TO_WIRE[filter.state] : undefined,
    pageSize: USERS_PAGE_SIZE,
    pageToken,
  });
  return {
    users: r.users.map(mapUser),
    nextPageToken: r.nextPageToken,
    roles: r.roles.map((role) => ({ key: role.key, displayName: role.displayName, group: role.group, declared: role.declared })),
  };
}

/** Creates the account if needed, grants the console-managed roles, and has the instance email an invitation. */
export async function inviteIdentityUser(
  orgId: string,
  environmentId: string,
  args: { email: string; roles: string[] }
): Promise<{ userId: string; created: boolean }> {
  const r = await client().inviteUser({ orgId, environmentId, email: args.email.trim(), roles: args.roles });
  return { userId: r.userId, created: r.created };
}

/** Replaces the user's COMPLETE set of console-managed roles. Declared roles are left as they are. */
export async function setIdentityUserRoles(
  orgId: string,
  environmentId: string,
  args: { userId: string; roles: string[] }
): Promise<IdentityUser | null> {
  const r = await client().setUserRoles({ orgId, environmentId, userId: args.userId, roles: args.roles });
  return r.user ? mapUser(r.user) : null;
}

export async function sendIdentityPasswordReset(orgId: string, environmentId: string, userId: string): Promise<void> {
  await client().sendPasswordReset({ orgId, environmentId, userId });
}

export async function lockIdentityUser(orgId: string, environmentId: string, userId: string): Promise<void> {
  await client().lockUser({ orgId, environmentId, userId });
}

export async function unlockIdentityUser(orgId: string, environmentId: string, userId: string): Promise<void> {
  await client().unlockUser({ orgId, environmentId, userId });
}

export async function deleteIdentityUser(orgId: string, environmentId: string, userId: string): Promise<void> {
  await client().deleteUser({ orgId, environmentId, userId });
}

/** Ends the user's sign-in sessions; returns how many. Access tokens already issued stay valid until they expire. */
export async function revokeIdentitySessions(orgId: string, environmentId: string, userId: string): Promise<number> {
  const r = await client().revokeSessions({ orgId, environmentId, userId });
  return r.sessionsRevoked;
}

/** A fresh signed URL for the export that already exists. The URL is a bearer credential: never log it. */
export async function getIdentityExportURL(orgId: string, environmentId: string): Promise<IdentityExportURL> {
  const r = await client().getExportURL({ orgId, environmentId });
  return { url: r.exportUrl, expiresAtMs: msOf(r.expiresAt) };
}
