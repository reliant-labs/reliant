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

/** How long a teardown may take to list its kept databases before the page warns. */
export const CONFIRM_WINDOW_MS = 10 * 60_000;

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

export function endKeptDataCheck(environmentId: string): void {
  useKeptDataChecks.setState((state) => ({ checks: state.checks.filter((c) => c.environmentId !== environmentId) }));
}
