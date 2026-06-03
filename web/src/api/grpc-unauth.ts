/**
 * Unauthenticated gRPC transport for DevAuth calls.
 *
 * This is separated from grpc-client.ts to avoid circular dependencies with
 * Supabase:
 *   supabase.ts → devAuth (this file) for session storage,
 *   grpc-client.ts → supabase for the auth interceptor.
 *
 * The interceptor chain comes from `buildInterceptors({ withAuth: false })`
 * in `./transport`. That call site dynamic-imports Supabase only inside the
 * auth interceptor body — and that interceptor isn't included when
 * `withAuth: false` — so this file doesn't pull Supabase into its dependency
 * graph at module load time. The cycle stays broken.
 */

import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { create } from "@bufbuild/protobuf";
import {
  DevAuthLoadRequestSchema,
  DevAuthSaveRequestSchema,
  DevAuthClearRequestSchema,
  StartOAuthSignInRequestSchema,
  SystemService,
} from "../gen/reliant/v1/system_pb";
import { buildLocalhostUrl } from "../lib/protocol";
import { buildInterceptors } from "./transport";

// Minimal local logger. Avoids depending on `@/lib/logger` to keep the
// pre-auth bootstrap path's import graph small. The interceptor chain
// supplied by buildInterceptors already covers per-RPC request/error
// logging; this is only for the transport-creation event.
const log = {
  info: (...args: unknown[]) => console.log('[gRPC-Unauth]', ...args),
};

// Get gRPC base URL - simplified version without Electron config dependencies
const getGRPCBaseURL = (): string | null => {
  // Check if running in Electron with config available
  if (
    typeof window !== "undefined" &&
    window.RELIANT_CONFIG?.isElectron
  ) {
    if (window.RELIANT_CONFIG?.grpcUrl) {
      return window.RELIANT_CONFIG.grpcUrl;
    }
  }

  // If we're in a file:// protocol (Electron but config not loaded yet),
  // return null to indicate not ready
  if (typeof window !== "undefined" && window.location.protocol === "file:") {
    return null;
  }

  // Fallback for development/browser - protocol based on VITE_DISABLE_TLS
  const grpcPort = import.meta.env.VITE_GRPC_PORT || "9090";
  const fallbackUrl =
    import.meta.env.VITE_GRPC_URL ||
    import.meta.env.VITE_API_URL ||
    buildLocalhostUrl(grpcPort);
  return fallbackUrl;
};

// Lazy-initialized transport
let _transport: ReturnType<typeof createConnectTransport> | null = null;
let _currentBaseURL: string | null = null;

const getTransport = () => {
  const currentBaseURL = getGRPCBaseURL();

  if (currentBaseURL === null) {
    throw new Error("gRPC client not ready - waiting for backend configuration");
  }

  // Recreate transport if URL changed
  if (!_transport || _currentBaseURL !== currentBaseURL) {
    _currentBaseURL = currentBaseURL;
    log.info('Creating DevAuth transport', currentBaseURL);
    // interceptors via buildInterceptors — see api/transport.ts
    //
    // withAuth: false skips the bearer-token + 401-auto-signout interceptors
    // (DevAuth runs *before* a session exists; a 401 here is a normal
    // "not logged in yet" signal, not a stale token). Timeout / tracing /
    // error logging / upgrade-modal still apply so the chain matches the
    // other transports below the auth layer.
    _transport = createConnectTransport({
      baseUrl: currentBaseURL,
      interceptors: buildInterceptors({ withAuth: false }),
      useBinaryFormat: false,
    });
  }

  return _transport;
};

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
    const client = createClient(SystemService, getTransport());
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
    const client = createClient(SystemService, getTransport());
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
    const client = createClient(SystemService, getTransport());
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
    const client = createClient(SystemService, getTransport());
    const response = await client.devAuthClear(create(DevAuthClearRequestSchema));
    return {
      success: response.success,
    };
  },
};