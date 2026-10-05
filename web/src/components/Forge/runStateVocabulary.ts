// Copyright (c) 2025 Reliant Labs

/**
 * RUN-STATE WORDS: what a hosted workload's declared run state (what its owner
 * asked for) and observed state (what the platform confirmed) add up to.
 *
 * The control plane exposes the two halves, not the reason a workload is
 * suspended, so the owner's stop and a billing suspension are told apart by
 * the pair:
 *
 *   declared suspended + observed suspended     the owner stopped it
 *   declared suspended + still running          stopping (converging)
 *   declared running   + observed suspended     billing — the platform holds
 *                                               it down; the server's own
 *                                               explanation is in last_error —
 *                                               unless there is no explanation
 *                                               yet, which is a start in flight
 *   declared running   + not yet ready          starting
 */

import type { ForgeHostedWorkload } from "@/services/forge/topology";

export type RunStateKind = "running" | "stopped" | "billing" | "starting" | "stopping" | "unknown";

export interface RunStateView {
  kind: RunStateKind;
  label: string;
  /** One sentence for a tooltip or inline note. */
  detail: string;
}

export const BILLING_FIX =
  "Suspended — billing. Subscribe to a compute plan or settle the invoice in Billing, then start it again.";

export function runStateOf(workload: ForgeHostedWorkload): RunStateView {
  const declared = workload.declared_run_state;
  const observed = workload.observed_state;
  const error = (workload.last_error ?? "").trim();

  if (declared === "suspended") {
    if (observed === "suspended") {
      return {
        kind: "stopped",
        label: "Stopped by you",
        detail: "Compute is off. Data and URLs are kept. Start it to bring it back.",
      };
    }
    return { kind: "stopping", label: "Stopping…", detail: "Asked to stop; the platform has not confirmed yet." };
  }
  if (declared === "running") {
    if (observed === "suspended") {
      if (error !== "") {
        return { kind: "billing", label: "Suspended — billing", detail: `${BILLING_FIX} Platform says: ${error}` };
      }
      return { kind: "starting", label: "Starting…", detail: "Asked to start; the platform has not confirmed yet." };
    }
    if (observed === "ready") {
      return { kind: "running", label: "Running", detail: "Serving." };
    }
    if (observed === "pending" || observed === "progressing") {
      return { kind: "starting", label: "Starting…", detail: "Coming up; not ready yet." };
    }
  }
  return { kind: "unknown", label: "", detail: "" };
}

/** Whether the whole environment reads as stopped: every workload declared suspended. */
export function environmentStopped(workloads: ForgeHostedWorkload[]): boolean {
  return workloads.length > 0 && workloads.every((w) => w.declared_run_state === "suspended");
}
