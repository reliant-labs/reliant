import { ConnectError } from "@connectrpc/connect";

import { DaemonWakingSchema } from "@/gen/reliant/v1/daemon_registry_pb";
import {
  ProjectCheckoutMissingSchema,
  type ProjectCheckoutMissing,
} from "@/gen/reliant/v1/filesystem_pb";

import { DAEMON_CONNECTING_REASONS, hasErrorReason } from "./errorReasons";

/**
 * Transient "your machine hasn't connected yet" detection.
 *
 * When a cloud machine is still starting, asleep, waking or reconnecting,
 * daemon RPCs (chat send, file tree, editor, terminal, search) fail with one
 * of the DAEMON_CONNECTING_REASONS. The Connect CODE is not the signal — the
 * same states have surfaced as `internal` and as `unavailable` — the reason is
 * (lib/errorReasons.ts, which also holds the "no daemon connected" message
 * marker older servers are still matched by).
 *
 * This module answers exactly one question: *is this error the machine, or is
 * it real?* What to SAY about it, how long to wait, and when to give up are
 * decided in `daemon-wait.ts` (rendered waits) and `daemon-retry.ts`
 * (one-shot actions). Keeping the classifier separate from the policy is what
 * stopped each surface from inventing its own copy and its own timeout.
 *
 * Genuine failures (auth, not-found, bad request) carry none of these
 * reasons, so they still surface as real errors.
 */

/**
 * Returns true for the transient "the machine is on its way" error class — the
 * cloud daemon is starting, asleep, waking or reconnecting.
 */
export function isDaemonConnectingError(error: unknown): boolean {
  return hasErrorReason(error, DAEMON_CONNECTING_REASONS);
}

/**
 * Returns true when a request found no machine at all: toolexec.ErrNoDaemon,
 * reason `no_daemon`. There is nothing to wait for — unlike
 * isDaemonConnectingError — so a surface says "no machine" instead of
 * spinning, and instead of showing the server's text as an error.
 */
export function isNoMachineError(error: unknown): boolean {
  return hasErrorReason(error, "no_daemon");
}

/**
 * Returns true for toolexec.ErrDaemonPending — the machine is still starting
 * or is suspended — as an RPC error's reason or as it reads inside a tool
 * result ("the machine for this request is suspended and will wake…").
 *
 * The tool-result text is the FALLBACK, and it stays after the RPC markers
 * go: the LLM reads daemonoffline.ErrorSubstring in tool text, and a string
 * has no metadata to carry a reason. A run's state is read structurally from
 * `ChatActivity.WAITING_FOR_DAEMON` / `RunDisplayState.WAITING_FOR_MACHINE`
 * (WORKFLOW_UI.md §9.2, G7); never parse tool text where that is available.
 */
export function isDaemonPendingError(error: unknown): boolean {
  if (hasErrorReason(error, ["daemon_starting", "daemon_suspended"])) return true;
  // Tool-result text (a string, or an Error built from one): not an RPC error.
  if (error instanceof ConnectError) return false;
  const text = error instanceof Error ? error.message : typeof error === "string" ? error : "";
  const message = text.toLowerCase();
  if (!message.includes("no daemon connected")) return false;
  return message.includes("still starting") || message.includes("suspended");
}

/**
 * The machine the server woke for this failed request, when it woke one.
 *
 * A file or worktree request that finds its machine asleep wakes it and fails
 * as Unavailable with a `DaemonWaking` detail naming the machine
 * (internal/grpc/services/machine_wake.go). Its reason is `daemon_waking`, so
 * `isDaemonConnectingError` — and every surface's wait and retry — already
 * treat it as "the machine is coming"; the detail adds WHICH machine, so the wait can say "Waking up…" about it.
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
