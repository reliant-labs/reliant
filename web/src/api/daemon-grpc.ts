import { createClient } from "@connectrpc/connect";
import { DaemonService } from "../gen/reliant/v1/daemon_pb";
import type { StartOAuthFlowResponse } from "../gen/reliant/v1/daemon_pb";
import { getTransport } from "./grpc-client";

/**
 * Start an OAuth flow via the daemon. The daemon spins up a localhost callback
 * server, opens the user's browser to the authorize URL (with the real redirect
 * URI substituted for the `{redirect_uri}` placeholder), waits for the callback,
 * then returns the authorization code, state, and redirect URI.
 *
 * ⚠️ This RPC CANNOT complete a real sign-in and is not used by the desktop
 * app any more. It blocks one request/response RPC on a human working through
 * a consent screen; in the packaged app that failed at ~15s with "Failed to
 * fetch", and cancelling the request tore down the daemon's listener, so the
 * browser's redirect hit a closed port. It is also unreachable by construction
 * in distributed mode, where the daemon is a pod and its loopback listener is
 * on the wrong machine entirely.
 *
 * Electron now uses its own local receiver (`startProviderOAuth` /
 * `waitForProviderOAuth` in electron/src/oauth-provider-login.js), which binds
 * the port and returns immediately, then waits over IPC that no network
 * deadline bounds. Kept only for the non-Electron fallback path.
 */
export async function startOAuthViaDaemon(
  authorizeUrlTemplate: string,
  signal?: AbortSignal
): Promise<StartOAuthFlowResponse> {
  const transport = getTransport();
  const client = createClient(DaemonService, transport);
  return client.startOAuthFlow({ authorizeUrlTemplate }, { signal });
}