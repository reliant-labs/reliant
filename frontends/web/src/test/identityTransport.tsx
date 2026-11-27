/**
 * IdentityService at the transport, for tests of the Identity tab, which
 * reads and writes it through the generated hooks
 * (hooks/controlplane/identity-service-hooks_gen.ts, via
 * hooks/identity-queries.ts).
 *
 *   const api = fakeIdentity({ getStatus: vi.fn(async () => status()) });
 *   render(<IdentityFakeScope fakes={api}><IdentityTab … /></IdentityFakeScope>);
 *   expect(api.lockUser).toHaveBeenCalledWith("org-a", "env-prod", "u-alice");
 *
 * Each fake speaks the console's model (services/controlPlane/identity/api.ts),
 * so a test keeps stating its fixture and its assertions as the tab sees
 * them; the handlers map each request to the fake's arguments and its answer
 * back to the wire. A fake that throws answers with that error.
 *
 * IdentityFakeScope mounts the fakes as a TransportScope over a test
 * QueryClient: the tab's hooks resolve the scope's transport from it, as they
 * do under a sample app's scope. The caller's org outside the org tree is
 * `currentOrgId` (services/controlPlane/orgs.ts), not an IdentityService
 * read: a test file points it at the scope's fake with
 *
 *   vi.mock("@/services/controlPlane/orgs", async (importOriginal) => ({
 *     ...(await importOriginal<typeof import("@/services/controlPlane/orgs")>()),
 *     currentOrgId: () => fakeCurrentOrgId(),
 *   }));
 */

import { timestampFromMs, type Timestamp } from "@bufbuild/protobuf/wkt";
import type { QueryClient } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";

import {
  IdentityDeliveryState as WireDeliveryState,
  IdentityMailDelivery as WireMailDelivery,
  IdentityNotEnabledReason as WireNotEnabledReason,
  IdentityState as WireState,
  IdentityUserState as WireUserState,
} from "@/gen/controlplane/controlplane/v1/identity_pb";
import { mockTransport, type MockHandlers } from "@/lib/test-utils";
import { createTransportScope, TransportScopeProvider, type TransportScope } from "@/lib/transportScope";
import type {
  IdentityDeliveryState,
  IdentityEnvironmentRef,
  IdentityExportURL,
  IdentityMailDelivery,
  IdentityNotEnabledReason,
  IdentityPasswordCode,
  IdentityState,
  IdentityStatus,
  IdentityUser,
  IdentityUserFilter,
  IdentityUserState,
  IdentityUsersPage,
} from "@/services/controlPlane/identity/api";
import { createTestQueryClient } from "./renderWithQuery";
import { serviceHandlers } from "./serviceTransport";

type Maybe<T> = T | Promise<T>;

/** IdentityService, one fake per rpc the tab calls, in the console's model. */
export interface IdentityFakes {
  /** The caller's org outside the org tree (see the file comment). */
  currentOrgId: () => Promise<string | null>;
  getStatus: (orgId: string, environmentId: string) => Maybe<IdentityStatus>;
  getUser?: (orgId: string, environmentId: string, userId: string) => Maybe<IdentityUser | null>;
  listUsers: (orgId: string, environmentId: string, filter: IdentityUserFilter, pageToken: string) => Maybe<IdentityUsersPage>;
  inviteUser: (
    orgId: string,
    environmentId: string,
    args: { email: string; roles: string[]; returnCode: boolean }
  ) => Maybe<{ userId: string; created: boolean; code: IdentityPasswordCode | null }>;
  setUserRoles: (orgId: string, environmentId: string, args: { userId: string; roles: string[] }) => Maybe<IdentityUser | null>;
  /** Called with `{ returnCode: true }` only when the code is asked for, as the tab's email reset never does. */
  sendPasswordReset: (orgId: string, environmentId: string, userId: string, opts?: { returnCode: true }) => Maybe<IdentityPasswordCode | null>;
  lockUser: (orgId: string, environmentId: string, userId: string) => Maybe<void>;
  unlockUser: (orgId: string, environmentId: string, userId: string) => Maybe<void>;
  deleteUser: (orgId: string, environmentId: string, userId: string) => Maybe<void>;
  revokeSessions: (orgId: string, environmentId: string, userId: string) => Maybe<number>;
  getExportURL: (orgId: string, environmentId: string) => Maybe<IdentityExportURL>;
  requestExport: (orgId: string, environmentId: string) => Maybe<IdentityExportURL>;
}

const STATE: Record<IdentityState, WireState> = {
  pending: WireState.PENDING,
  creating: WireState.CREATING,
  provisioning: WireState.PROVISIONING,
  ready: WireState.READY,
  degraded: WireState.DEGRADED,
  failing: WireState.FAILING,
  withdrawn: WireState.WITHDRAWN,
  grace: WireState.GRACE,
  deleting: WireState.DELETING,
  deleted: WireState.DELETED,
  unknown: WireState.UNSPECIFIED,
};

const DELIVERY_STATE: Record<IdentityDeliveryState, WireDeliveryState> = {
  delivered: WireDeliveryState.DELIVERED,
  pending: WireDeliveryState.PENDING,
  failing: WireDeliveryState.FAILING,
  unbound: WireDeliveryState.UNBOUND,
  withdrawn: WireDeliveryState.WITHDRAWN,
  disabled: WireDeliveryState.DISABLED,
  unknown: WireDeliveryState.UNSPECIFIED,
};

const MAIL_DELIVERY: Record<IdentityMailDelivery, WireMailDelivery> = {
  none: WireMailDelivery.NONE,
  smtp: WireMailDelivery.SMTP,
  unknown: WireMailDelivery.UNSPECIFIED,
};

const NOT_ENABLED: Record<IdentityNotEnabledReason, WireNotEnabledReason> = {
  "not-declared": WireNotEnabledReason.NOT_DECLARED,
  refused: WireNotEnabledReason.REFUSED,
  pending: WireNotEnabledReason.PENDING,
  "no-shape": WireNotEnabledReason.NO_SHAPE,
  unknown: WireNotEnabledReason.UNSPECIFIED,
};

const USER_STATE: Record<IdentityUserState, WireUserState> = {
  invited: WireUserState.INVITED,
  active: WireUserState.ACTIVE,
  locked: WireUserState.LOCKED,
  disabled: WireUserState.DISABLED,
  unknown: WireUserState.UNSPECIFIED,
};

const MODEL_USER_STATE = new Map(Object.entries(USER_STATE).map(([word, wire]) => [wire, word as IdentityUserState]));

const ts = (ms: number | null): Timestamp | undefined => (ms === null ? undefined : timestampFromMs(ms));

const environment = (e: IdentityEnvironmentRef) => ({ id: e.id, name: e.name, project: e.project });

function statusToWire(s: IdentityStatus) {
  return {
    hasInstance: s.hasInstance,
    state: STATE[s.state],
    issuer: s.issuer,
    domain: s.domain,
    public: s.public,
    adopted: s.adopted,
    apps: s.apps.map((a) => ({ name: a.name, clientId: a.clientId, brokerExpiresAt: ts(a.brokerExpiresAtMs) })),
    deliveries: s.deliveries.map((d) => ({
      kind: d.kind,
      cluster: d.cluster,
      namespace: d.namespace,
      name: d.name,
      state: DELIVERY_STATE[d.state],
      lastError: d.lastError,
      lastVerifiedAt: ts(d.lastVerifiedAtMs),
      lastWrittenAt: ts(d.lastWrittenAtMs),
    })),
    ownerEnvironmentId: s.ownerEnvironmentId,
    sharerEnvironmentIds: s.sharerEnvironmentIds,
    lastReconciledAt: ts(s.lastReconciledAtMs),
    nextAttemptAt: ts(s.nextAttemptAtMs),
    lastError: s.lastError,
    failures: s.failures,
    graceUntil: ts(s.graceUntilMs),
    exportUrlAvailable: s.exportUrlAvailable,
    refused: s.refused,
    callerCanManage: s.callerCanManage,
    exportEnabled: s.exportEnabled,
    mailDelivery: MAIL_DELIVERY[s.mailDelivery],
    notEnabledReason: NOT_ENABLED[s.notEnabledReason],
    notEnabledMessage: s.notEnabledMessage,
    ownerEnvironment: s.ownerEnvironment ? environment(s.ownerEnvironment) : undefined,
    sharerEnvironments: s.sharerEnvironments.map(environment),
  };
}

function userToWire(u: IdentityUser) {
  return {
    userId: u.userId,
    email: u.email,
    displayName: u.displayName,
    state: USER_STATE[u.state],
    roles: u.roles,
    createdAt: ts(u.createdAtMs),
    lastSignInAt: ts(u.lastSignInAtMs),
  };
}

function pageToWire(p: IdentityUsersPage) {
  return {
    users: p.users.map(userToWire),
    nextPageToken: p.nextPageToken,
    roles: p.roles,
    // The console reads a count of 0 as "this server does not count".
    totalSize: p.totalSize ?? 0,
  };
}

const codeToWire = (c: IdentityPasswordCode | null) =>
  c ? { code: c.code, expiresAt: ts(c.expiresAtMs), resetUrl: c.resetUrl } : undefined;

const exportToWire = (e: IdentityExportURL) => ({ exportUrl: e.url, expiresAt: ts(e.expiresAtMs) });

interface Env {
  orgId: string;
  environmentId: string;
}
interface UserTarget extends Env {
  userId: string;
}

/** IdentityService's handlers, answered by `fakes` in the console's model. */
export function identityHandlers(fakes: IdentityFakes): MockHandlers {
  return serviceHandlers("IdentityService", {
    getIdentityStatus: async (r: Env) => statusToWire(await fakes.getStatus(r.orgId, r.environmentId)),
    getUser: async (r: UserTarget) => {
      if (!fakes.getUser) throw new Error("This fake has no getUser.");
      const user = await fakes.getUser(r.orgId, r.environmentId, r.userId);
      return { user: user ? userToWire(user) : undefined };
    },
    listUsers: async (r: Env & { query?: string; state?: WireUserState; pageToken: string }) => {
      const state = r.state === undefined ? undefined : MODEL_USER_STATE.get(r.state);
      const filter = { query: r.query ?? "", state: state === "unknown" ? undefined : state };
      return pageToWire(await fakes.listUsers(r.orgId, r.environmentId, filter, r.pageToken));
    },
    inviteUser: async (r: Env & { email: string; roles: string[]; returnCode: boolean }) => {
      const got = await fakes.inviteUser(r.orgId, r.environmentId, { email: r.email, roles: [...r.roles], returnCode: r.returnCode });
      return { userId: got.userId, created: got.created, code: codeToWire(got.code) };
    },
    setUserRoles: async (r: UserTarget & { roles: string[] }) => {
      const user = await fakes.setUserRoles(r.orgId, r.environmentId, { userId: r.userId, roles: [...r.roles] });
      return { user: user ? userToWire(user) : undefined };
    },
    sendPasswordReset: async (r: UserTarget & { returnCode: boolean }) => ({
      code: codeToWire(
        await (r.returnCode
          ? fakes.sendPasswordReset(r.orgId, r.environmentId, r.userId, { returnCode: true })
          : fakes.sendPasswordReset(r.orgId, r.environmentId, r.userId))
      ),
    }),
    lockUser: async (r: UserTarget) => (await fakes.lockUser(r.orgId, r.environmentId, r.userId), {}),
    unlockUser: async (r: UserTarget) => (await fakes.unlockUser(r.orgId, r.environmentId, r.userId), {}),
    deleteUser: async (r: UserTarget) => (await fakes.deleteUser(r.orgId, r.environmentId, r.userId), {}),
    revokeSessions: async (r: UserTarget) => ({ sessionsRevoked: await fakes.revokeSessions(r.orgId, r.environmentId, r.userId) }),
    getExportURL: async (r: Env) => exportToWire(await fakes.getExportURL(r.orgId, r.environmentId)),
    requestExport: async (r: Env) => exportToWire(await fakes.requestExport(r.orgId, r.environmentId)),
  });
}

/** The fakes the newest scope serves, whose currentOrgId the test file's orgs mock calls. */
let current: IdentityFakes | null = null;

/** The caller's org from the newest IdentityFakeScope's fakes: what a test file's orgs mock returns. */
export function fakeCurrentOrgId(): Promise<string | null> {
  if (!current) return Promise.reject(new Error("No IdentityFakeScope is mounted."));
  return current.currentOrgId();
}

const scopes = new WeakMap<QueryClient, WeakMap<IdentityFakes, TransportScope>>();

/** A TransportScope over `client` whose transport answers IdentityService from `fakes`; one per pair. */
export function identityScope(fakes: IdentityFakes, client: QueryClient): TransportScope {
  current = fakes;
  let byFakes = scopes.get(client);
  if (!byFakes) {
    byFakes = new WeakMap();
    scopes.set(client, byFakes);
  }
  let scope = byFakes.get(fakes);
  if (!scope) {
    scope = createTransportScope({ id: "identity-fakes", transport: mockTransport(identityHandlers(fakes)), queryClient: client });
    byFakes.set(fakes, scope);
  }
  return scope;
}

/** The tab under a scope answered by `fakes`, on `client` (a fresh test QueryClient unless given). */
export function IdentityFakeScope({ fakes, client, children }: { fakes: IdentityFakes; client?: QueryClient; children: ReactNode }) {
  const [own] = useState(createTestQueryClient);
  return <TransportScopeProvider scope={identityScope(fakes, client ?? own)}>{children}</TransportScopeProvider>;
}
