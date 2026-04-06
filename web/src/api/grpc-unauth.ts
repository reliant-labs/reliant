/**
 * Unauthenticated gRPC transport for DevAuth calls
 * 
 * This is separated from grpc-client.ts to avoid circular dependencies:
 * - supabase.ts needs devAuth for storage
 * - grpc-client.ts needs supabase for auth interceptor
 * - This file provides transport without needing supabase
 */

import { createClient } from "@connectrpc/connect";
import type { Interceptor } from "@connectrpc/connect";
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

// Simple logger that doesn't depend on the main logger module
const log = {
  info: (...args: unknown[]) => console.log('[gRPC-Unauth]', ...args),
  error: (...args: unknown[]) => console.error('[gRPC-Unauth]', ...args),
  warn: (...args: unknown[]) => console.warn('[gRPC-Unauth]', ...args),
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
    if (window.RELIANT_CONFIG?.backendUrl) {
      return window.RELIANT_CONFIG.backendUrl;
    }
  }

  // If we're in a file:// protocol (Electron but config not loaded yet),
  // return null to indicate not ready
  if (typeof window !== "undefined" && window.location.protocol === "file:") {
    return null;
  }

  // Fallback for development/browser - protocol based on VITE_DISABLE_TLS
  const grpcPort = (import.meta as { env?: { VITE_GRPC_PORT?: string } }).env?.VITE_GRPC_PORT || "9090";
  const fallbackUrl =
    (import.meta as { env?: { VITE_GRPC_URL?: string } }).env?.VITE_GRPC_URL ||
    buildLocalhostUrl(grpcPort);
  return fallbackUrl;
};

// Simple error logging interceptor (no auth)
const errorInterceptor: Interceptor = (next) => async (req) => {
  try {
    log.info('DevAuth request:', req.method.name);
    const result = await next(req);
    log.info('DevAuth request succeeded:', req.method.name);
    return result;
  } catch (error) {
    log.error('DevAuth request failed:', req.method.name, error);
    throw error;
  }
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
    _transport = createConnectTransport({
      baseUrl: currentBaseURL,
      interceptors: [errorInterceptor],
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
  async startOAuthSignIn(provider: string, timeoutSeconds = 120): Promise<{
    accessToken: string
    refreshToken: string
    userId: string
    email: string
  }> {
    const client = createClient(SystemService, getTransport());
    const response = await client.startOAuthSignIn(
      create(StartOAuthSignInRequestSchema, { provider, timeoutSeconds })
    );
    return {
      accessToken: response.accessToken,
      refreshToken: response.refreshToken,
      userId: response.userId,
      email: response.email,
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