// Copyright (c) 2025 Reliant Labs

/**
 * "Keep data" is VERIFIED, not trusted. A delete with keep_data answers OK
 * before anything is torn down; the control plane's converger records the
 * kept databases only when the teardown finishes (control-plane
 * internal/hubintent executor.go, teardown). A server that dropped the field
 * would answer the same OK and delete the data. So each keep-data delete
 * leaves a check here, and the Environments page (RetainedDatabases.tsx)
 * re-reads ListRetainedDatabases until the environment's databases are
 * listed, or warns when they are not listed within CONFIRM_WINDOW_MS, or at
 * once when the control plane cannot list kept databases at all.
 *
 * In memory, per tab: the check outlives the dialog (which closes and
 * navigates to the Environments page) but not a reload, after which the
 * kept-databases list itself is the record.
 */

import { create } from "zustand";

import type { RetainedReadFailure } from "@/services/forge/cloudEnvs";

/**
 * Why a kept database cannot be listed here, by failure class, for the delete
 * dialog (which then offers no Keep data) and for an outstanding check (which
 * then cannot be confirmed).
 */
export const RETAINED_READ_FAILURE_COPY: Record<RetainedReadFailure, string> = {
  unsupported: "This control plane can't list kept databases.",
  "no-access": "You can't view kept databases here.",
  unavailable: "Reliant couldn't check the kept databases. Try again.",
};

/** How long a teardown may take to list its kept databases before the page warns. */
export const CONFIRM_WINDOW_MS = 10 * 60_000;

/**
 * How often the kept-databases list is re-read while a keep-data delete is
 * being confirmed. Mutable so a test can shrink the poll instead of waiting
 * out the production interval.
 */
export const keptDataPoll = { intervalMs: 5_000 };

export interface KeptDataCheck {
  environmentId: string;
  envName: string;
  /** The forge project (app); "" shows the check on every app's page. */
  project: string;
  /** The database workloads the environment ran, when its status said. */
  databases: string[];
  /** When the delete was accepted, epoch ms. */
  since: number;
}

interface KeptDataChecksState {
  checks: KeptDataCheck[];
}

export const useKeptDataChecks = create<KeptDataChecksState>(() => ({ checks: [] }));

export function startKeptDataCheck(check: Omit<KeptDataCheck, "since"> & { since?: number }): void {
  const entry = { ...check, since: check.since ?? Date.now() };
  useKeptDataChecks.setState((state) => ({
    checks: [...state.checks.filter((c) => c.environmentId !== entry.environmentId), entry],
  }));
}

export interface KeptDataProgress {
  /** Recorded database names listed as kept. */
  kept: string[];
  /** Recorded database names not listed (yet). */
  missing: string[];
  /** Every recorded database is listed: the check may end. */
  done: boolean;
}

/**
 * How far a check has got against the kept-databases list.
 *
 * EVERY recorded database must be listed, by name, before the check ends: an
 * environment with two databases of which one was kept and one deleted is a
 * loss, not a success. The names match because a managed database's CNPG
 * Cluster, which the control plane records as database_name, is named after
 * the ManagedDatabase, which is the database workload (control-plane
 * manageddatabase controller.go, cluster.SetName(db.Name)). A mismatch would
 * err towards a warning, never towards silence.
 *
 * With no names recorded (the status read had not said which workloads are
 * databases) the check can only ask for some database of the environment.
 */
export function keptDataProgress(
  check: KeptDataCheck,
  listed: readonly { environmentId: string; databaseName: string }[]
): KeptDataProgress {
  const fromEnv = listed.filter((row) => row.environmentId === check.environmentId);
  if (check.databases.length === 0) return { kept: [], missing: [], done: fromEnv.length > 0 };
  const names = new Set(fromEnv.map((row) => row.databaseName));
  const kept = check.databases.filter((name) => names.has(name));
  const missing = check.databases.filter((name) => !names.has(name));
  return { kept, missing, done: missing.length === 0 };
}

export function endKeptDataCheck(environmentId: string): void {
  useKeptDataChecks.setState((state) => ({ checks: state.checks.filter((c) => c.environmentId !== environmentId) }));
}
