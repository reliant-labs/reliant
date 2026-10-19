/**
 * Growing a cloud machine's disk: the choices, the price and the status.
 *
 * Split out of machines.tsx for the reason machineLifecycle.ts is: none of
 * this is presentation, and a grow is a one-way, billed change, so every
 * number a user reads before confirming one is worth a test without a DOM.
 *
 * The rules are control-plane's (docs/design/workspace-disk-resize.md):
 *
 *   - A disk only grows, in whole 10 GiB steps — GrowDaemonDisk refuses any
 *     other target — up to the plan's `max_daemon_storage_gib`.
 *     GetCurrentUserComputeEligibility sends that limit with the platform's
 *     1 TiB ceiling already applied. Zero means the plan offers no growth.
 *   - A stopped machine pays for its whole disk. A running one pays for the
 *     part above `included_gib`, the disk its hourly price includes. Both are
 *     billed at one per-GiB-month rate, ListPlans' daemon_pricing.
 *   - A running machine's guest keeps the disk size it booted with, so it can
 *     use a grow only after a restart. `usable_gib` is the size the running
 *     guest sees, and `capacity_gib` above it is space a restart unlocks.
 */

import { ConnectError } from "@connectrpc/connect";

import { DaemonDiskState } from "@/gen/controlplane/controlplane/v1/shared_pb";
import { centsDecimalToDollars, type DaemonPricingLike } from "@/components/Billing/daemonUsage";

/** The step a disk grows in (control-plane svcdaemon diskGrowStepGiB). */
export const DISK_GROW_STEP_GIB = 10;

/**
 * The largest disk any machine may have, whatever its plan says
 * (control-plane planlimits.DaemonStorageCeilingGiB). The server already
 * applies it to the limit it sends; it is restated only so that a machine at
 * the ceiling is not told a bigger plan would help.
 */
export const DISK_CEILING_GIB = 1024;

/**
 * The reason GrowDaemonDisk attaches (header X-Reliant-Reason) when the target
 * exceeds the plan's limit, or the plan offers no growth at all
 * (control-plane svcdaemon ReasonDaemonStorageDenied).
 */
export const DAEMON_STORAGE_DENIED_REASON = "daemon_storage_denied";

export interface DiskGrowRange {
  /** The smallest size the disk can grow to: the next whole step. */
  min: number;
  /** The largest the plan allows, rounded down to a whole step. */
  max: number;
}

/**
 * The sizes a disk of `currentGib` can grow to under a plan limit of
 * `maxGib`, or null when there is no room to grow: the plan offers no growth,
 * or the disk is already at the limit.
 */
export function diskGrowRange(currentGib: number, maxGib: number): DiskGrowRange | null {
  if (currentGib <= 0 || maxGib <= 0) return null;
  const min = (Math.floor(currentGib / DISK_GROW_STEP_GIB) + 1) * DISK_GROW_STEP_GIB;
  const max = Math.floor(maxGib / DISK_GROW_STEP_GIB) * DISK_GROW_STEP_GIB;
  return min <= max ? { min, max } : null;
}

export interface DiskGrowPrice {
  /**
   * "+$2.50/mo": what the grow adds to the bill. It is the same running or
   * stopped, because a disk is never smaller than the size its hourly price
   * includes (requested_gib is max(standard, grown)), so every added GiB is
   * billed in both states.
   */
  added: string;
  /** A running machine's disk charge at the new size: "$22.50/mo". */
  running: string;
  /** The GiB that charge covers: the part above `includedGib`. */
  runningGib: number;
  /** The whole disk's charge while the machine is stopped: "$42.50/mo". */
  stopped: string;
}

/**
 * The price of growing a disk from `currentGib` to `targetGib`, at the
 * server's per-GiB-month disk rate. Null without a price list, so the modal
 * shows no price rather than a guessed one.
 */
export function diskGrowPrice(
  pricing: Pick<DaemonPricingLike, "suspendedDiskCentsPerGibMonth"> | undefined,
  { currentGib, targetGib, includedGib }: { currentGib: number; targetGib: number; includedGib: number },
): DiskGrowPrice | null {
  if (!pricing) return null;
  const centsPerGib = Number(pricing.suspendedDiskCentsPerGibMonth);
  if (!Number.isFinite(centsPerGib)) return null;
  const monthly = (gib: number) => `${centsDecimalToDollars(String(centsPerGib * gib))}/mo`;
  const runningGib = Math.max(0, targetGib - includedGib);
  return {
    added: `+${monthly(Math.max(0, targetGib - currentGib))}`,
    running: monthly(runningGib),
    runningGib,
    stopped: monthly(targetGib),
  };
}

/** The slice of control-plane's DaemonDisk the status reads. */
export interface DiskLike {
  requestedGib: number;
  capacityGib: number;
  usableGib: number;
  state: DaemonDiskState;
  message: string;
}

export type DiskGrowStatus =
  | { kind: "expanding"; targetGib: number }
  | { kind: "failed"; targetGib: number; message: string }
  | { kind: "restart-to-use"; extraGib: number };

/**
 * What the detail page should say about the disk, or null for nothing.
 *
 * `running` is whether the machine has a pod. A stopped machine has no guest,
 * so it has no "restart to use" either: it uses the whole disk when it starts.
 * A usable size of zero means unknown, for example a pod built before the
 * operator recorded it, and is never read as a disk the guest cannot use.
 */
export function diskGrowStatus(disk: DiskLike | undefined, running: boolean, allowanceGib = 0): DiskGrowStatus | null {
  if (!disk) return null;
  if (disk.state === DaemonDiskState.EXPANDING) return { kind: "expanding", targetGib: disk.requestedGib + allowanceGib };
  if (disk.state === DaemonDiskState.FAILED) {
    return { kind: "failed", targetGib: disk.requestedGib + allowanceGib, message: disk.message };
  }
  if (running && disk.usableGib > 0 && disk.capacityGib > disk.usableGib) {
    return { kind: "restart-to-use", extraGib: disk.capacityGib - disk.usableGib };
  }
  return null;
}

/** Whether `err` is GrowDaemonDisk refusing a size the plan does not allow. */
export function isStorageDenied(err: unknown): boolean {
  return err instanceof ConnectError && err.metadata.get("x-reliant-reason") === DAEMON_STORAGE_DENIED_REASON;
}
