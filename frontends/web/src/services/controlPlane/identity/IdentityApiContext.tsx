// Copyright (c) 2025 Reliant Labs

/**
 * The seam between the Identity tab and the RPCs it calls — the same shape as
 * ObservabilityApiContext. Every hook in hooks/identity-queries.ts calls
 * through it, so component tests hand it an in-memory fake. Production never
 * provides anything but the default: the real client on the nearest
 * TransportScope's transport, else on the app's.
 */

import { createContext, useContext, type ReactNode } from "react";
import type { Transport } from "@connectrpc/connect";

import { transport as appTransport } from "@/lib/connect";
import { useTransport } from "@/lib/transportScope";

import * as rpc from "./api";

export interface IdentityApi {
  currentOrgId: typeof rpc.currentOrgId;
  getStatus: typeof rpc.getIdentityStatus;
  getUser?: typeof rpc.getIdentityUser;
  listUsers: typeof rpc.listIdentityUsers;
  inviteUser: typeof rpc.inviteIdentityUser;
  setUserRoles: typeof rpc.setIdentityUserRoles;
  sendPasswordReset: typeof rpc.sendIdentityPasswordReset;
  lockUser: typeof rpc.lockIdentityUser;
  unlockUser: typeof rpc.unlockIdentityUser;
  deleteUser: typeof rpc.deleteIdentityUser;
  revokeSessions: typeof rpc.revokeIdentitySessions;
  getExportURL: typeof rpc.getIdentityExportURL;
  requestExport: typeof rpc.requestIdentityExport;
}

/**
 * The real client on `transport`: a TransportScope's (lib/transportScope.tsx),
 * or undefined for the app's. Each method hands it to its RPC function, which
 * passes it to getControlPlaneClient.
 */
function apiOn(transport: Transport | undefined): IdentityApi {
  const reads = rpc.identityReadsOn(transport);
  return {
    // The active org (the switcher's choice, else the person's default): the app's, in a scope too.
    currentOrgId: rpc.currentOrgId,
    getStatus: reads.getStatus,
    getUser: reads.getUser,
    listUsers: reads.listUsers,
    inviteUser: (orgId, environmentId, args) => rpc.inviteIdentityUser(orgId, environmentId, args, transport),
    setUserRoles: (orgId, environmentId, args) => rpc.setIdentityUserRoles(orgId, environmentId, args, transport),
    sendPasswordReset: (orgId, environmentId, userId, opts) => rpc.sendIdentityPasswordReset(orgId, environmentId, userId, opts, transport),
    lockUser: (orgId, environmentId, userId) => rpc.lockIdentityUser(orgId, environmentId, userId, transport),
    unlockUser: (orgId, environmentId, userId) => rpc.unlockIdentityUser(orgId, environmentId, userId, transport),
    deleteUser: (orgId, environmentId, userId) => rpc.deleteIdentityUser(orgId, environmentId, userId, transport),
    revokeSessions: (orgId, environmentId, userId) => rpc.revokeIdentitySessions(orgId, environmentId, userId, transport),
    getExportURL: (orgId, environmentId) => rpc.getIdentityExportURL(orgId, environmentId, transport),
    requestExport: (orgId, environmentId) => rpc.requestIdentityExport(orgId, environmentId, transport),
  };
}

/** The real client on the app's transport (lib/connect.ts): production's, outside every scope. */
export const controlPlaneIdentityApi: IdentityApi = apiOn(undefined);

const scopedApis = new WeakMap<Transport, IdentityApi>();

/**
 * The real client on `transport` (a TransportScope's, lib/transportScope.tsx),
 * memoized per transport. The app's transport is controlPlaneIdentityApi.
 */
export function identityApiOf(transport: Transport): IdentityApi {
  if (transport === appTransport) return controlPlaneIdentityApi;
  let api = scopedApis.get(transport);
  if (!api) {
    api = apiOn(transport);
    scopedApis.set(transport, api);
  }
  return api;
}

const IdentityApiContext = createContext<IdentityApi | null>(null);

export function IdentityApiProvider({ api, children }: { api: IdentityApi; children: ReactNode }) {
  return <IdentityApiContext.Provider value={api}>{children}</IdentityApiContext.Provider>;
}

/** The provided api, else the real client on the nearest TransportScope's transport, else on the app's. */
export function useIdentityApi(): IdentityApi {
  const provided = useContext(IdentityApiContext);
  const transport = useTransport(appTransport);
  return provided ?? identityApiOf(transport);
}
