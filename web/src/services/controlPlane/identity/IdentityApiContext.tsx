// Copyright (c) 2025 Reliant Labs

/**
 * The seam between the Identity tab and the RPCs it calls — the same shape as
 * ObservabilityApiContext. Every hook in hooks/identity-queries.ts calls
 * through it, so component tests hand it an in-memory fake. Production never
 * provides anything but the default.
 */

import { createContext, useContext, type ReactNode } from "react";

import * as rpc from "./api";

export interface IdentityApi {
  currentOrgId: typeof rpc.currentOrgId;
  getStatus: typeof rpc.getIdentityStatus;
  listUsers: typeof rpc.listIdentityUsers;
  inviteUser: typeof rpc.inviteIdentityUser;
  setUserRoles: typeof rpc.setIdentityUserRoles;
  sendPasswordReset: typeof rpc.sendIdentityPasswordReset;
  lockUser: typeof rpc.lockIdentityUser;
  unlockUser: typeof rpc.unlockIdentityUser;
  deleteUser: typeof rpc.deleteIdentityUser;
  revokeSessions: typeof rpc.revokeIdentitySessions;
  getExportURL: typeof rpc.getIdentityExportURL;
}

export const controlPlaneIdentityApi: IdentityApi = {
  currentOrgId: rpc.currentOrgId,
  getStatus: rpc.getIdentityStatus,
  listUsers: rpc.listIdentityUsers,
  inviteUser: rpc.inviteIdentityUser,
  setUserRoles: rpc.setIdentityUserRoles,
  sendPasswordReset: rpc.sendIdentityPasswordReset,
  lockUser: rpc.lockIdentityUser,
  unlockUser: rpc.unlockIdentityUser,
  deleteUser: rpc.deleteIdentityUser,
  revokeSessions: rpc.revokeIdentitySessions,
  getExportURL: rpc.getIdentityExportURL,
};

const IdentityApiContext = createContext<IdentityApi>(controlPlaneIdentityApi);

export function IdentityApiProvider({ api, children }: { api: IdentityApi; children: ReactNode }) {
  return <IdentityApiContext.Provider value={api}>{children}</IdentityApiContext.Provider>;
}

export function useIdentityApi(): IdentityApi {
  return useContext(IdentityApiContext);
}
