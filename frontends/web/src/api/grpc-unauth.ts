/**
 * The pre-session sign-in calls: the dev-mode session file (DevAuth*) and the
 * Electron-local OAuth sign-in.
 *
 * They run BEFORE a session exists, so they take the unauthenticated route:
 * no bearer token, no 401 recovery (a 401 here means "not signed in yet", and
 * recovering from it would loop). That routing is not decided here —
 * `routeFor` in src/lib/connect.ts sends these four procedures to the
 * "reliant-unauth" transport whoever calls them, which still runs the rest of
 * the shared chain (deadline, tracing, error reporting, upgrade modal).
 *
 * Kept apart from the authenticated wrappers for the import graph: supabase.ts
 * imports this module for its session storage, so it must not pull in the
 * wrappers (and through them the stores) that those wrappers bring.
 */

import { create } from "@bufbuild/protobuf";
import {
  DevAuthLoadRequestSchema,
  DevAuthSaveRequestSchema,
  DevAuthClearRequestSchema,
  StartOAuthSignInRequestSchema,
  SystemService,
} from "../gen/reliant/v1/system_pb";
import { connectClient } from "../lib/connect";

/**
 * DevAuth gRPC methods - used by supabase storage adapter
 * These don't require authentication (used before auth is established)
 */
export const devAuthGrpc = {
  async startOAuthSignIn(provider: string): Promise<{
    accessToken: string
    refreshToken: string
    userId: string
    email: string
    providerToken: string
  }> {
    const client = connectClient(SystemService);
    const response = await client.startOAuthSignIn(
      create(StartOAuthSignInRequestSchema, { provider })
    );
    return {
      accessToken: response.accessToken,
      refreshToken: response.refreshToken,
      userId: response.userId,
      email: response.email,
      providerToken: response.providerToken,
    };
  },

  /**
   * Load auth session from file (development only)
   */
  async load(): Promise<{ success: boolean; sessionJson?: string }> {
    const client = connectClient(SystemService);
    const response = await client.devAuthLoad(create(DevAuthLoadRequestSchema));
    return {
      success: response.success,
      sessionJson: response.sessionJson,
    };
  },

  /**
   * Save auth session to file (development only)
   */
  async save(sessionJson: string): Promise<{ success: boolean }> {
    const client = connectClient(SystemService);
    const response = await client.devAuthSave(
      create(DevAuthSaveRequestSchema, { sessionJson })
    );
    return {
      success: response.success,
    };
  },

  /**
   * Clear auth session file (development only)
   */
  async clear(): Promise<{ success: boolean }> {
    const client = connectClient(SystemService);
    const response = await client.devAuthClear(create(DevAuthClearRequestSchema));
    return {
      success: response.success,
    };
  },
};