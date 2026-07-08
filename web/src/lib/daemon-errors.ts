import { ConnectError } from "@connectrpc/connect";

/**
 * Transient "your cloud daemon hasn't connected yet" detection.
 *
 * When a freshly-provisioned cloud daemon is still PENDING/connecting, daemon
 * RPCs (chat send, file tree, …) fail with a Connect error whose message is
 * `[internal] unavailable: no daemon connected for user`. Note the Connect
 * CODE on the wire is `internal`, not `unavailable` — the "unavailable" and
 * "no daemon connected" text lives in the MESSAGE — so the reliable signal is
 * the "no daemon connected" marker, not the code.
 *
 * This is a CONNECTING state, not a failure: consumers should render a
 * "Connecting to your environment…" spinner + auto-retry (see
 * DaemonConnectingState + the DaemonConnectingGate onboarding flow) rather than
 * a red error banner. Genuine failures (daemon FAILED, auth, not-found, bad
 * request) don't carry the marker, so they still surface as real errors.
 *
 * Centralised here so every daemon-RPC consumer classifies the error the same
 * way and can't drift.
 */

/** User-facing copy for the transient connecting state. */
export const DAEMON_CONNECTING_MESSAGE = "Connecting to your environment…";

/**
 * How long a consumer should keep auto-retrying the connecting state before
 * giving up and surfacing a genuine error. Mirrors DaemonConnectingGate's 60s
 * provisioning window (image pull + relay handshake).
 */
export const DAEMON_CONNECT_TIMEOUT_MS = 60_000;

function extractMessage(error: unknown): string {
  if (error instanceof ConnectError) return error.rawMessage || error.message;
  if (error instanceof Error) return error.message;
  if (typeof error === "string") return error;
  return "";
}

/**
 * Returns true for the transient "no daemon connected yet" error class — the
 * cloud daemon is still coming online. Match on the stable message marker;
 * the Connect code is unreliable here (surfaces as `internal`).
 */
export function isDaemonConnectingError(error: unknown): boolean {
  return extractMessage(error).toLowerCase().includes("no daemon connected");
}
