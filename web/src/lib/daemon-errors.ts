import { ConnectError } from "@connectrpc/connect";

import { DaemonWakingSchema } from "@/gen/reliant/v1/daemon_registry_pb";
import {
  ProjectCheckoutMissingSchema,
  type ProjectCheckoutMissing,
} from "@/gen/reliant/v1/filesystem_pb";

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

/**
 * Returns true for toolexec.ErrDaemonPending as it reads inside a tool result:
 * "the machine for this request is suspended and will wake…" or "…is still
 * starting", both wrapping the "no daemon connected" marker.
 *
 * This is the FALLBACK. A run's state is read structurally from
 * `ChatActivity.WAITING_FOR_DAEMON` / `RunDisplayState.WAITING_FOR_MACHINE`
 * (WORKFLOW_UI.md §9.2, G7); never parse tool text where that is available.
 */
export function isDaemonPendingError(error: unknown): boolean {
  const message = extractMessage(error).toLowerCase();
  if (!message.includes("no daemon connected")) return false;
  return message.includes("still starting") || message.includes("suspended");
}

/**
 * The machine the server woke for this failed request, when it woke one.
 *
 * A file or worktree request that finds its machine asleep wakes it and fails
 * as Unavailable with a `DaemonWaking` detail naming the machine
 * (internal/grpc/services/machine_wake.go). The message still carries the
 * "no daemon connected" marker, so `isDaemonConnectingError` — and every
 * surface's wait and retry — already treat it as "the machine is coming"; the
 * detail adds WHICH machine, so the wait can say "Waking up…" about it.
 */
export function wakingDaemonId(error: unknown): string | null {
  if (!(error instanceof ConnectError)) return null;
  for (const detail of error.findDetails(DaemonWakingSchema)) {
    if (detail.daemonId) return detail.daemonId;
  }
  return null;
}

/**
 * The project's directory does not exist on the machine a request reached —
 * the server's ProjectCheckoutMissing detail on a NOT_FOUND — or null for any
 * other error.
 *
 * Not a machine wait and not a failure. It is either a clone still landing
 * (state CLONING: the window right after "Clone from GitHub", which used to
 * render a raw "no such file or directory" 500) or a project that is simply
 * not on this machine (ABSENT / CLONE_FAILED), which the user can fix by
 * cloning it here.
 */
export function projectCheckoutMissing(error: unknown): ProjectCheckoutMissing | null {
  if (!(error instanceof ConnectError)) return null;
  for (const detail of error.findDetails(ProjectCheckoutMissingSchema)) {
    return detail;
  }
  return null;
}
