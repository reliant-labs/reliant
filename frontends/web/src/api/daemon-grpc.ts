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

/**
 * Ask the daemon to open its localhost OAuth helper port for ONE linking
 * session.
 *
 * Returns immediately — unlike `startOAuthViaDaemon` this does not block on a
 * human at a consent screen, which is what made that RPC fail at ~15s in the
 * packaged app.
 *
 * Pair every call with `closeOAuthHelper`. The daemon also closes the port
 * after an idle timeout, but that is the backstop for a UI that never got to
 * ask (a closed tab, a crash) — not the normal path.
 */
export async function openOAuthHelper(
  webOrigin: string,
  signal?: AbortSignal
): Promise<{ port: number; addr: string; alreadyRunning: boolean }> {
  const transport = getTransport();
  const client = createClient(DaemonService, transport);
  const resp = await client.openOAuthHelper({ webOrigin }, { signal });
  return {
    port: resp.port,
    addr: resp.addr,
    alreadyRunning: resp.alreadyRunning,
  };
}

/**
 * Release the helper port. Best-effort by design: the caller is usually in a
 * `finally`, and a failure here must not mask the outcome of the flow itself.
 * The daemon's idle timeout covers a missed close.
 */
export async function closeOAuthHelper(): Promise<void> {
  try {
    const transport = getTransport();
    const client = createClient(DaemonService, transport);
    await client.closeOAuthHelper({});
  } catch {
    // Ignored: the idle timeout is the backstop.
  }
}