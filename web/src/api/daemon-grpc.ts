import { createClient } from "@connectrpc/connect";
import { DaemonService } from "../gen/reliant/v1/daemon_pb";
import type { StartOAuthFlowResponse } from "../gen/reliant/v1/daemon_pb";
import { getDaemonTransport } from "./grpc-client";

/**
 * Start an OAuth flow via the daemon. The daemon spins up a localhost callback
 * server, opens the user's browser to the authorize URL (with the real redirect
 * URI substituted for the `{redirect_uri}` placeholder), waits for the callback,
 * then returns the authorization code, state, and redirect URI.
 */
export async function startOAuthViaDaemon(
  authorizeUrlTemplate: string,
  timeoutSeconds: number,
  signal?: AbortSignal
): Promise<StartOAuthFlowResponse> {
  const transport = await getDaemonTransport();
  const client = createClient(DaemonService, transport);
  return client.startOAuthFlow({ authorizeUrlTemplate, timeoutSeconds }, { signal });
}