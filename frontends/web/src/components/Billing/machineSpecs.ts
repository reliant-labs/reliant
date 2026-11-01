/**
 * What a hosted machine actually gives you, per size.
 *
 * ── Why this is a constant and not a server field ─────────────────────
 *
 * A second copy of a control-plane ladder is normally the bug, so the
 * alternative was checked first and it does not exist. `ListPlans` returns
 * `PlanLimits` (control-plane controlplane/v1/shared.proto), whose only machine facts
 * are `allowed_daemon_sizes`, `daemon_compute_included_minutes` and
 * `daemon_overage_per_minute_cents`. The message that DOES carry cpu_request /
 * cpu_limit / memory_request / memory_limit is `ResourceRequirements`, and it
 * hangs off a machine that has already been provisioned (and off the
 * create/update requests). There is nothing to read before the user has bought
 * one, and helping them decide before that is this screen's entire job.
 *
 * SOURCE OF TRUTH: config.DaemonTierResources in control-plane's
 * internal/config/workspace_config.go. Guaranteed CPU and RAM requests equal
 * limits. machineSpecs.contract.test.ts pins this consumer contract and checks
 * the actual sibling source when available. Replace this mirror with public
 * hardware fields when that API is available; this is not a new authority.
 *
 * Only what these screens RENDER is duplicated. The real ladder also carries a
 * per-size data disk (config.DaemonStorageSizeForTier), deliberately absent
 * here, because an unrendered copy of a ladder is a drift bug with nobody
 * reading it to notice.
 *
 * ── Every sold tier is here ───────────────────────────────────────────
 *
 * Both machine lists are built from `offeredDaemonSizes`, the union of what
 * the CATALOG allows, and it drops any size `DAEMON_SIZE_ORDER` does not list.
 * A priced tier the client cannot name is therefore not omitted gracefully:
 * it is silently unbuyable. So every tier the catalog sells (S1 to 3XL) has
 * its order, its label and its shape here, and the catalog still decides
 * whether a row APPEARS.
 */

import type { DaemonSizeName } from "@/components/Settings/cloud/billingUtils";

export interface MachineSpec {
  /** Cores reserved for this machine — the floor it always has. */
  cpuReserved: number;
  /** Cores it may burst to when a build asks for them. */
  cpuBurst: number;
  /** Gigabytes of memory reserved. */
  memoryReservedGb: number;
  /** Gigabytes of memory it may burst to. */
  memoryBurstGb: number;
}

/**
 * Keyed by `DaemonSizeName`, so the sizes this client can label are exactly
 * the sizes with specs — a size added to one and forgotten in the other is a
 * type error rather than a blank cell.
 */
export const MACHINE_SPECS: Record<DaemonSizeName, MachineSpec> = {
  // Guaranteed guest requests == limits; CP config.DaemonTierResources is canonical.
  s1: { cpuReserved: 1, cpuBurst: 1, memoryReservedGb: 2, memoryBurstGb: 2 },
  s2: { cpuReserved: 1, cpuBurst: 1, memoryReservedGb: 4, memoryBurstGb: 4 },
  m: { cpuReserved: 2, cpuBurst: 2, memoryReservedGb: 4, memoryBurstGb: 4 },
  l: { cpuReserved: 4, cpuBurst: 4, memoryReservedGb: 8, memoryBurstGb: 8 },
  xl: { cpuReserved: 8, cpuBurst: 8, memoryReservedGb: 16, memoryBurstGb: 16 },
  "2xl": { cpuReserved: 15, cpuBurst: 15, memoryReservedGb: 32, memoryBurstGb: 32 },
  "3xl": { cpuReserved: 30, cpuBurst: 30, memoryReservedGb: 64, memoryBurstGb: 64 },
};

/**
 * The reserved figures, as one short phrase to sit beside a size name.
 *
 * Memory leads because it is the constraint a user actually hits — a build that
 * outgrows it dies, where one short of cores merely takes longer. "CPU" is not
 * pluralised: "0.5 CPUs" is technically correct and reads as a typo, and the
 * unit is doing the work of a label here rather than counting objects.
 *
 * The BURST figures are deliberately not in this string. See
 * MACHINE_BURST_MULTIPLE.
 */
export function formatMachineSpec(size: DaemonSizeName): string | undefined {
  const spec = MACHINE_SPECS[size];
  if (!spec) return undefined;
  return `${spec.memoryReservedGb} GB RAM · ${spec.cpuReserved} CPU`;
}

/**
 * How far a machine may burst above what it reserves.
 *
 * Guaranteed guests currently have requests equal to limits: there is no
 * additional burst entitlement. The generic ratio helper below withholds the
 * optional sentence when both ratios are one.
 *
 * This was written as a single shared multiple first, because control-plane's
 * own comments say so twice — `daemonSizeResources` claims "Limits are 4x
 * requests across the whole ladder" and TestDaemonSizeResourcesLadder repeats
 * "Limits are deliberately 4x requests". Both are true of CPU and wrong about
 * memory: every tier pairs a 2Gi/4Gi, 4Gi/8Gi, 8Gi/16Gi … request/limit, which
 * is 2x. Read the numbers, not the sentence above them.
 *
 * The single-multiple version did not ship a false claim — the uniformity
 * check below saw mixed ratios and collapsed to `undefined`, which deleted the
 * burst sentence from the page. That is the behaviour to preserve: an
 * unrepresentable claim is better than a confident wrong one next to a price.
 *
 * Stated once under the list rather than per row, because both multiples hold
 * at every size. Repeating them beside four already-dense labels turns the
 * fact most likely to change someone's mind into noise — and it is worth
 * stating at all precisely because "0.5 CPU" undersells a machine that
 * compiles on two full cores.
 */
export interface MachineBurst {
  cpu: number;
  memory: number;
}

/**
 * The multiple shared by every size along one axis, or `undefined` if the
 * sizes disagree — in which case the caller says nothing rather than picking
 * one tier's ratio to speak for the rest.
 */
function uniformRatio(ratioOf: (spec: MachineSpec) => number): number | undefined {
  const ratios = Object.values(MACHINE_SPECS).map(ratioOf);
  const first = ratios[0];
  if (first == null) return undefined;
  return ratios.every((ratio) => ratio === first) ? first : undefined;
}

function machineBurst(): MachineBurst | undefined {
  const cpu = uniformRatio((spec) => spec.cpuBurst / spec.cpuReserved);
  const memory = uniformRatio(
    (spec) => spec.memoryBurstGb / spec.memoryReservedGb,
  );
  // Both or neither: a sentence naming one axis and silently omitting the
  // other invites the reader to assume they match, which is the specific
  // mistake control-plane's own comment already made.
  if (cpu == null || memory == null || (cpu <= 1 && memory <= 1)) return undefined;
  return { cpu, memory };
}

export const MACHINE_BURST = machineBurst();
