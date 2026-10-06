/**
 * "Waking up…" for a sleeping machine the user just resumed.
 *
 * The registry has no resuming phase: a machine coming back from sleep reads
 * PENDING, exactly like one being created, so the list said "Pending" for the
 * whole wake — the "machines that wake on demand" moment, rendered as a stall.
 * The page that sent Resume does know it is a wake, so it records that here,
 * and the status shown is derived from the record plus what the registry says.
 *
 * Module-level rather than component state because the list and the detail
 * view are separate components: resuming from the list and opening the
 * machine must still say "Waking up…".
 */
import { useSyncExternalStore } from "react";

/** How long a resumed machine may still read SUSPENDED before we stop claiming it is waking. */
export const WAKE_SUSPENDED_GRACE_MS = 60_000;
/** A wake that has not finished in this long is not presented as one any more. */
export const WAKE_MAX_MS = 15 * 60_000;

const wakeStartedAt = new Map<string, number>();
const listeners = new Set<() => void>();
let snapshot: ReadonlyMap<string, number> = new Map();

function emit() {
  snapshot = new Map(wakeStartedAt);
  for (const listener of listeners) listener();
}

export function markWaking(daemonId: string, now = Date.now()): void {
  wakeStartedAt.set(daemonId, now);
  emit();
}

export function clearWaking(daemonId: string): void {
  if (wakeStartedAt.delete(daemonId)) emit();
}

export function useWakingMachines(): ReadonlyMap<string, number> {
  return useSyncExternalStore(
    (listener) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    () => snapshot,
  );
}

export type MachineWsStatus = "active" | "suspended" | "failed" | "pending" | "disconnected";

export interface MachineStatusPresentation {
  /** Overrides the status badge's label; undefined keeps the default. */
  label?: string;
  /** Plain-English progress the control plane reported, when it has one. */
  progress?: string;
  /** Whether a recorded wake is over and should be forgotten. */
  wakeFinished: boolean;
}

/**
 * What to say about a machine's status, given whether the user woke it.
 *
 * Progress comes from `last_status_message`, which the control plane fills
 * with the cold-start stage while a pod comes up ("Preparing your workspace
 * image. This is the last slow step.") — shown for any starting machine, not
 * only a woken one, because it is true either way.
 */
export function presentMachineStatus(
  status: MachineWsStatus,
  lastStatusMessage: string | undefined,
  wakeStarted: number | undefined,
  now = Date.now(),
): MachineStatusPresentation {
  const message = lastStatusMessage?.trim() || undefined;
  const starting = status === "pending" || status === "disconnected";

  if (wakeStarted !== undefined) {
    const age = now - wakeStarted;
    const settled = status === "active" || status === "failed" || age > WAKE_MAX_MS;
    // Right after Resume the list may still hold the SUSPENDED row it had;
    // past a short grace, a machine that stayed suspended did not wake.
    const stillSuspended = status === "suspended" && age > WAKE_SUSPENDED_GRACE_MS;
    if (settled || stillSuspended) return { wakeFinished: true };
    if (starting || status === "suspended") {
      return { label: "Waking up…", progress: starting ? message : undefined, wakeFinished: false };
    }
  }

  return { progress: status === "pending" ? message : undefined, wakeFinished: false };
}
