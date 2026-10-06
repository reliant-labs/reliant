// Copyright (c) 2025 Reliant Labs

/**
 * RUN-STATE WORDS: what a hosted workload's declared run state (what its owner
 * asked for) and observed state (what the platform confirmed) add up to.
 *
 * The control plane reports WHY a workload is suspended (suspend_reason, read
 * off the tier CR's Suspended condition), and that is used whenever it is
 * present. Only when it is absent (an older control plane) are the owner's stop
 * and a billing suspension told apart by inference from the pair:
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
export const NO_COMPUTE_PLAN_FIX = "Subscribe to a compute plan, then start it again.";
export const BILLING_LAPSED_FIX = "Settle the invoice in Billing, then start it again.";

const STOPPED_BY_OWNER: RunStateView = {
  kind: "stopped",
  label: "Stopped by you",
  detail: "Compute is off. Data and URLs are kept. Start it to bring it back.",
};

/** The exact reading for a reported reason, or null when none was reported. */
function viewForSuspendReason(reason: string | undefined, error: string): RunStateView | null {
  const platform = error !== "" ? ` Platform says: ${error}` : "";
  switch (reason) {
    case "owner":
      return STOPPED_BY_OWNER;
    case "no_compute_plan":
      return {
        kind: "billing",
        label: "Suspended — no compute plan",
        detail: `${NO_COMPUTE_PLAN_FIX}${platform}`,
      };
    case "billing_lapsed":
      return {
        kind: "billing",
        label: "Suspended — billing",
        detail: `${BILLING_LAPSED_FIX}${platform}`,
      };
    default:
      return null;
  }
}

export function runStateOf(workload: ForgeHostedWorkload): RunStateView {
  const declared = workload.declared_run_state;
  const observed = workload.observed_state;
  const error = (workload.last_error ?? "").trim();

  // The reported reason is exact; inference below is only for UNSPECIFIED.
  if (observed === "suspended") {
    const exact = viewForSuspendReason(workload.suspend_reason, error);
    if (exact) {
      // A stop the owner has since reversed (declared running) is the start
      // in flight, not a stop: the CR has not caught up yet.
      if (exact.kind === "stopped" && declared === "running") {
        return { kind: "starting", label: "Starting…", detail: "Asked to start; the platform has not confirmed yet." };
      }
      return exact;
    }
  }

  if (declared === "suspended") {
    if (observed === "suspended") {
      return STOPPED_BY_OWNER;
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
