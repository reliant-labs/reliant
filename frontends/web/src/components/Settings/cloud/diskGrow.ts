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

import { hasErrorReason } from "@/lib/errorReasons";

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
 * The reason GrowDaemonDisk attaches when the target
 * exceeds the plan's limit, or the plan offers no growth at all
 * (control-plane svcdaemon ReasonDaemonStorageDenied).
 */
export const DAEMON_STORAGE_DENIED_REASON = "daemon_storage_denied" as const;

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
  /** The most the grow adds a month: "+$5.00/mo". */
  added: string;
}

/**
 * What growing a disk from `currentGib` to `targetGib` can add, at the
 * server's storage price: "+$5.00/mo". Storage is pooled across the org's
 * machines, so this is the most it adds (none of it, while the org is under
 * its plan's included storage). Null without a price list.
 */
export function diskGrowPrice(
  pricing: Pick<DaemonPricingLike, "storageCentsPerGibMonth"> | undefined,
  { currentGib, targetGib }: { currentGib: number; targetGib: number },
): DiskGrowPrice | null {
  if (!pricing) return null;
  const centsPerGib = Number(pricing.storageCentsPerGibMonth);
  if (!Number.isFinite(centsPerGib)) return null;
  return { added: `+${centsDecimalToDollars(String(centsPerGib * Math.max(0, targetGib - currentGib)))}/mo` };
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
export function diskGrowStatus(disk: DiskLike | undefined, running: boolean): DiskGrowStatus | null {
  if (!disk) return null;
  if (disk.state === DaemonDiskState.EXPANDING) return { kind: "expanding", targetGib: disk.requestedGib };
  if (disk.state === DaemonDiskState.FAILED) {
    return { kind: "failed", targetGib: disk.requestedGib, message: disk.message };
  }
  if (running && disk.usableGib > 0 && disk.capacityGib > disk.usableGib) {
    return { kind: "restart-to-use", extraGib: disk.capacityGib - disk.usableGib };
  }
  return null;
}

/** Whether `err` is GrowDaemonDisk refusing a size the plan does not allow. */
export function isStorageDenied(err: unknown): boolean {
  return hasErrorReason(err, DAEMON_STORAGE_DENIED_REASON);
}
