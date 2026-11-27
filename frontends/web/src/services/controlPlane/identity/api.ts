// Copyright (c) 2025 Reliant Labs

/**
 * control-plane `controlplane.v1.IdentityService`'s answers, mapped into the
 * console's model. The calls are the generated hooks'; this is the only file
 * that knows the wire shape of what they return.
 *
 * WHAT IT IS. An environment's managed identity is a Zitadel instance the
 * control plane runs for it. platform-api relays every call to the identity
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
 *
 * MAIL. An instance sends mail only when its environment declares smtp
 * (`mailDelivery`). Without it, invite and reset are refused unless the caller
 * asks for the password-set code back (`returnCode`), which also needs the
 * identity:manage permission. A returned code is a credential: this file maps
 * it and hands it to the screen, and nothing logs it.
 */

import type { MessageInitShape } from "@bufbuild/protobuf";
import type { Timestamp } from "@bufbuild/protobuf/wkt";

import {
  IdentityDeliveryState as WireDeliveryState,
  IdentityMailDelivery as WireMailDelivery,
  IdentityNotEnabledReason as WireNotEnabledReason,
  IdentityState as WireState,
  IdentityUserState as WireUserState,
  type IdentityEnvironment as WireEnvironment,
  type IdentityPasswordCode as WirePasswordCode,
  type IdentityUser as WireUser,
} from "@/gen/controlplane/controlplane/v1/identity_pb";
import type {
  GetIdentityExportURLResponse,
  GetIdentityStatusResponse,
  GetIdentityUserResponse,
  ListIdentityUsersRequestSchema,
  ListIdentityUsersResponse,
  RequestIdentityExportResponse,
  SetIdentityUserRolesResponse,
} from "@/gen/controlplane/services/identity/v1/identity_pb";

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
  /** When the outputs last CHANGED at the destination (verification alone does not move it). */
  lastWrittenAtMs: number | null;
}

/**
 * Why an environment has no instance. `unknown`: no reason given (the
 * environment has an instance, or the server named none).
 */
export type IdentityNotEnabledReason = "not-declared" | "refused" | "pending" | "no-shape" | "unknown";

/** An environment of the caller's org, named by the control plane. */
export interface IdentityEnvironmentRef {
  id: string;
  /** "" when the environment is no longer live. */
  name: string;
  project: string;
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
  /**
   * Whether the instance can send mail. `none`: invitations and resets are
   * refused unless the code is handed back (return_code). `unknown`: the
   * server could not say, so the console claims neither.
   */
  mailDelivery: IdentityMailDelivery;
  /** Why there is no instance, when `hasInstance` is false, and the server's sentence for it. */
  notEnabledReason: IdentityNotEnabledReason;
  notEnabledMessage: string;
  /** ownerEnvironmentId / sharerEnvironmentIds with names; null when there is no owner. */
  ownerEnvironment: IdentityEnvironmentRef | null;
  sharerEnvironments: IdentityEnvironmentRef[];
}

export type IdentityMailDelivery = "none" | "smtp" | "unknown";

/**
 * A one-time password-set code handed back to an admin to deliver themselves.
 * It is a CREDENTIAL: shown once, never logged, never toasted, never cached.
 */
export interface IdentityPasswordCode {
  code: string;
  /** An estimate (issue time plus the instance's 1h default). */
  expiresAtMs: number | null;
  /** The environment's reset page with the code filled in; "" when it declares nothing to build it from. */
  resetUrl: string;
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
  /** The newest successful sign-in the instance recorded; null for never (see `reportsSignIns`). */
  lastSignInAtMs: number | null;
}

export interface IdentityUsersPage {
  users: IdentityUser[];
  nextPageToken: string;
  roles: IdentityRole[];
  /** How many users match the filter across every page; null from a server that does not count. */
  totalSize: number | null;
  /**
   * Whether this server reports sign-ins at all, so a null `lastSignInAtMs`
   * means "never" rather than "not reported". A server that does always
   * counts, and a page with users has a count of at least one.
   */
  reportsSignIns: boolean;
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

const MAIL_DELIVERY: Record<number, IdentityMailDelivery> = {
  [WireMailDelivery.NONE]: "none",
  [WireMailDelivery.SMTP]: "smtp",
};

const USER_STATE: Record<number, IdentityUserState> = {
  [WireUserState.INVITED]: "invited",
  [WireUserState.ACTIVE]: "active",
  [WireUserState.LOCKED]: "locked",
  [WireUserState.DISABLED]: "disabled",
};

const NOT_ENABLED: Record<number, IdentityNotEnabledReason> = {
  [WireNotEnabledReason.NOT_DECLARED]: "not-declared",
  [WireNotEnabledReason.REFUSED]: "refused",
  [WireNotEnabledReason.PENDING]: "pending",
  [WireNotEnabledReason.NO_SHAPE]: "no-shape",
};

function mapEnvironment(e: WireEnvironment): IdentityEnvironmentRef {
  return { id: e.id, name: e.name, project: e.project };
}

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
    lastSignInAtMs: msOf(u.lastSignInAt),
  };
}

// ── Wire → model ─────────────────────────────────────────────────────────────
//
// The calls themselves are the generated hooks'
// (hooks/controlplane/identity-service-hooks_gen.ts, through
// hooks/identity-queries.ts); these map their answers, as each hook's
// `select` or a write's onSuccess.

/** GetIdentityStatus' answer. Module-level, so a hook's `select` keeps one result per response. */
export function toIdentityStatus(r: GetIdentityStatusResponse): IdentityStatus {
  return {
    hasInstance: r.hasInstance,
    state: identityStateOf(r.state),
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
      lastWrittenAtMs: msOf(d.lastWrittenAt),
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
    mailDelivery: MAIL_DELIVERY[r.mailDelivery] ?? "unknown",
    notEnabledReason: NOT_ENABLED[r.notEnabledReason] ?? "unknown",
    notEnabledMessage: r.notEnabledMessage,
    ownerEnvironment: r.ownerEnvironment ? mapEnvironment(r.ownerEnvironment) : null,
    sharerEnvironments: r.sharerEnvironments.map(mapEnvironment),
  };
}

/** An instance's state as a word: the status poll reads it off the wire answer before `select` runs. */
export function identityStateOf(state: WireState): IdentityState {
  return STATE[state] ?? "unknown";
}

/** GetUser's answer: null when the instance has no such user. */
export function toIdentityUser(r: GetIdentityUserResponse): IdentityUser | null {
  return r.user ? mapUser(r.user) : null;
}

export const USERS_PAGE_SIZE = 50;

/** ListUsers' first-page request for a filter; the infinite read adds each next page's token. */
export function listIdentityUsersRequest(
  orgId: string,
  environmentId: string,
  filter: IdentityUserFilter
): MessageInitShape<typeof ListIdentityUsersRequestSchema> {
  const query = filter.query?.trim();
  return {
    orgId,
    environmentId,
    query: query ? query : undefined,
    state: filter.state ? USER_STATE_TO_WIRE[filter.state] : undefined,
    pageSize: USERS_PAGE_SIZE,
  };
}

/** One page of ListUsers. */
export function toIdentityUsersPage(r: ListIdentityUsersResponse): IdentityUsersPage {
  return {
    users: r.users.map(mapUser),
    nextPageToken: r.nextPageToken,
    roles: r.roles.map((role) => ({ key: role.key, displayName: role.displayName, group: role.group, declared: role.declared })),
    totalSize: r.totalSize > 0 ? r.totalSize : null,
    reportsSignIns: r.totalSize > 0,
  };
}

/** SetUserRoles' answer: the user with their new roles. */
export function toUpdatedIdentityUser(r: SetIdentityUserRolesResponse): IdentityUser | null {
  return r.user ? mapUser(r.user) : null;
}

/**
 * A returned password-set code (InviteUser, SendPasswordReset with
 * `returnCode`): null when there is none — an absent or empty code is never a
 * blank credential.
 */
export function toIdentityPasswordCode(c: WirePasswordCode | undefined): IdentityPasswordCode | null {
  if (!c?.code) return null;
  return { code: c.code, expiresAtMs: msOf(c.expiresAt), resetUrl: c.resetUrl };
}

/**
 * RequestExport's or GetExportURL's signed download. The URL is a bearer
 * credential for every password hash: never log, toast or cache it.
 */
export function toIdentityExportURL(r: RequestIdentityExportResponse | GetIdentityExportURLResponse): IdentityExportURL {
  return { url: r.exportUrl, expiresAtMs: msOf(r.expiresAt) };
}
