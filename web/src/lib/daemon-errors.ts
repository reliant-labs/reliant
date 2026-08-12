import { ConnectError } from "@connectrpc/connect";

/**
 * Transient "your machine hasn't connected yet" detection.
 *
 * When a cloud machine is still PENDING/connecting — or is suspended, or has
 * died — daemon RPCs (chat send, file tree, editor, terminal, search) fail
 * with a Connect error whose message is `[internal] unavailable: no daemon
 * connected for user`. Note the Connect CODE on the wire is `internal`, not
 * `unavailable` — the "unavailable" and "no daemon connected" text lives in
 * the MESSAGE — so the reliable signal is the "no daemon connected" marker,
 * not the code.
 *
 * This module answers exactly one question: *is this error the machine, or is
 * it real?* What to SAY about it, how long to wait, and when to give up are
 * decided in `daemon-wait.ts` (rendered waits) and `daemon-retry.ts`
 * (one-shot actions). Keeping the classifier separate from the policy is what
 * stopped each surface from inventing its own copy and its own timeout.
 *
 * Genuine failures (auth, not-found, bad request) don't carry the marker, so
 * they still surface as real errors.
 */

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
