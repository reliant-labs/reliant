/**
 * Machines the control plane has told this session are gone.
 *
 * The machine list is reliant's registry, a mirror of the control plane's
 * daemon set. The mirror is kept honest by the control plane — a removed event
 * when a machine is deleted, and a periodic snapshot that repairs anything that
 * event missed — but between a lost event and the next snapshot the list can
 * still offer a machine the control plane no longer has. That is exactly how
 * the owner's clone on 2026-10-07 went to cda8a89b, deleted the day before,
 * and came back `not_found`.
 *
 * When a control-plane call answers NotFound for a machine, that answer is the
 * authority, and it is recorded here so no surface auto-picks the machine
 * again this session. A machine that is ATTACHED right now is evidently not
 * gone, so a mark never hides one (see `withoutGoneMachines`).
 */
import { useSyncExternalStore } from "react";

import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";

const gone = new Set<string>();
const listeners = new Set<() => void>();
let version = 0;

function emit() {
  version += 1;
  for (const listener of listeners) listener();
}

/** Record that the control plane says this machine no longer exists. */
export function markMachineGone(daemonId: string): void {
  if (!daemonId || gone.has(daemonId)) return;
  gone.add(daemonId);
  emit();
}

/** Whether this session has been told the machine is gone. */
export function isMachineGone(daemonId: string): boolean {
  return gone.has(daemonId);
}

/**
 * The machines that may still be offered: everything except those marked gone,
 * unless one is attached right now — a connected machine is real whatever an
 * earlier answer said.
 */
export function withoutGoneMachines<T extends { daemonId: string; status: DaemonStatus }>(daemons: T[]): T[] {
  if (gone.size === 0) return daemons;
  return daemons.filter((d) => !gone.has(d.daemonId) || d.status === DaemonStatus.ACTIVE);
}

/** Re-render when the gone set changes; returns a version to key memos on. */
export function useGoneMachinesVersion(): number {
  return useSyncExternalStore(
    (listener) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    () => version,
    () => version,
  );
}

/** Tests only: forget every mark. */
export function resetGoneMachinesForTest(): void {
  gone.clear();
  emit();
}
