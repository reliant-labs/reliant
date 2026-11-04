// Copyright (c) 2025 Reliant Labs

/**
 * Which machine a new automation runs on when the person has not said, and
 * how a machine reads in the picker (report 03 §5, §7).
 *
 *   1. No machine, when the workflow needs none (it works through its
 *      integrations): nothing to choose.
 *   2. The machine last used in this project — its newest chat's or
 *      automation's — when it is still eligible: what they would pick.
 *   3. The single obvious one (daemonChoices.defaultDaemonId).
 *   4. Nothing: with several equally valid machines, the person picks.
 *
 * Pure.
 */

import { defaultDaemonId, NO_MACHINE, type DaemonChoice } from "../daemonChoices";

/** One string and one position everywhere: listed last. */
export const NO_MACHINE_OPTION_LABEL = "No machine (web and integrations only)";

export interface MachineUse {
  daemonId?: string;
  /** When it was used: an ISO time or epoch ms. */
  at?: string | number;
}

/** The daemon of the newest use, or undefined. */
export function lastUsedDaemon(uses: readonly MachineUse[]): string | undefined {
  let best: { id: string; at: number } | undefined;
  for (const use of uses) {
    if (!use.daemonId) continue;
    const at = typeof use.at === "number" ? use.at : use.at ? Date.parse(use.at) : 0;
    if (!best || (Number.isFinite(at) && at > best.at)) best = { id: use.daemonId, at: Number.isFinite(at) ? at : 0 };
  }
  return best?.id;
}

export function defaultMachine(opts: {
  choices: readonly DaemonChoice[];
  /** The workflow runs without a machine (the server's needs_machine is empty). */
  needsNoMachine: boolean;
  lastUsed?: string;
}): string {
  if (opts.needsNoMachine) return NO_MACHINE;
  const available = opts.choices.filter((c) => c.eligible && c.statusLabel !== "failed");
  if (opts.lastUsed && available.some((c) => c.daemonId === opts.lastUsed)) return opts.lastUsed;
  return defaultDaemonId(available) ?? "";
}

// Idle is awake enough to start a run; these are woken first.
const ASLEEP = new Set(["offline", "suspended"]);

/** "MacBook", "MacBook (offline: woken when it runs)", "MacBook (project not installed)". */
export function machineOptionLabel(choice: DaemonChoice): string {
  if (!choice.eligible) return `${choice.label} (${choice.ineligibleReason ?? "not available"})`;
  if (ASLEEP.has(choice.statusLabel)) return `${choice.label} (offline: woken when it runs)`;
  if (choice.statusLabel === "failed") return `${choice.label} (failed)`;
  return choice.label;
}
