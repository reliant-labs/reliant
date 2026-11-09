// Copyright (c) 2025 Reliant Labs

/** Every demo shell uses these isolated, auth-free synthetic clients, even when
 * the surrounding product has a real control-plane session. No network fallback. */
import { Code, ConnectError, createClient } from "@connectrpc/connect";
import { IdentityService } from "@/gen/controlplane/services/identity/v1/identity_pb";
import { createIdentityReadApi } from "@/services/controlPlane/identity/api";
import type { IdentityApi } from "@/services/controlPlane/identity/IdentityApiContext";
import { AppDatabaseService } from "@/gen/controlplane/services/app_database/v1/app_database_pb";
import { StorageBrowserService } from "@/gen/controlplane/services/storage_browser/v1/storage_browser_pb";
import { createDataBrowserApi } from "@/services/controlPlane/observability/appDatabaseApi";
import { createStorageBrowserApi } from "@/services/controlPlane/storageBrowser/storageBrowserApi";
import { createDemoTransport } from "./demoTransport";

export function createDemoBrowserApis() {
  const transport = createDemoTransport({ source: "fixtures", url: "" });
  const refused = async (): Promise<never> => { throw new ConnectError("This is a demo. Identity changes are turned off.", Code.PermissionDenied); };
  const reads = createIdentityReadApi(createClient(IdentityService, transport));
  const requireScope = (org: string, env: string) => {
    if (org !== "demo-org" || !["env-dev", "env-staging", "env-prod"].includes(env)) throw new ConnectError("No such sample identity scope.", Code.NotFound);
  };
  const identity: IdentityApi & { getUser: ReturnType<typeof createIdentityReadApi>["getUser"] } = {
    currentOrgId: async () => "demo-org",
    getStatus: async (org, env) => { requireScope(org, env); return reads.getStatus(org, env); },
    getUser: async (org, env, user) => { requireScope(org, env); return reads.getUser(org, env, user); },
    listUsers: async (org, env, filter, token) => { requireScope(org, env); return reads.listUsers(org, env, filter, token); },
    inviteUser: refused, setUserRoles: refused, sendPasswordReset: refused, lockUser: refused,
    unlockUser: refused, deleteUser: refused, revokeSessions: refused, getExportURL: refused, requestExport: refused,
  };
  return {
    identity,
    data: createDataBrowserApi(createClient(AppDatabaseService, transport)),
    storage: createStorageBrowserApi(createClient(StorageBrowserService, transport)),
  };
}
