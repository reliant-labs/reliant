// Copyright (c) 2025 Reliant Labs

/**
 * The health vocabulary for an automation: what the Health column says, in
 * which color, and whether the row belongs under "Needs attention". The table
 * is research/WORKFLOW_UI.md §7.2; the run-status words it sits beside come
 * from lib/runStatus.ts.
 *
 * The SERVER decides health (TriggerHealth, computed from the last 10 firings
 * and the runs they launched; rules on TriggerHealthStatus in
 * proto/reliant/v1/trigger.proto). The server has four statuses and the
 * design has more display states, because some facts are the client's:
 *
 *   Broken               BROKEN: it activates a workflow-declared trigger
 *                        that is gone or changed (the workflow deleted, the
 *                        trigger renamed, its kind or integration changed).
 *                        Wins over everything, Paused included: resuming it
 *                        would not make it fire, only fixing the workflow or
 *                        removing the activation does.
 *   Paused               the trigger is disabled. Wins over everything else:
 *                        a paused automation is not firing, so not failing.
 *   Failing              FAILING, shown as "N failed".
 *   Waiting for machine  the last firing's run is blocked on its daemon
 *                        (RunDisplayState.WAITING_FOR_MACHINE). Only a
 *                        failing streak outranks it.
 *   Skipping             DEGRADED with 3+ skips in a row.
 *   Degraded             any other DEGRADED (a recent failure that is not a
 *                        streak); the failure detail is the tooltip.
 *   Healthy              HEALTHY.
 *   New                  UNKNOWN and it has never fired.
 *   No result yet        UNKNOWN but it has fired: every recent firing is
 *                        still running, was cancelled, or lost its chat, so
 *                        there is no verdict either way.
 */

import { RunDisplayState } from "../gen/reliant/v1/run_pb";
import type { TriggerEvent, TriggerHealth } from "../api/trigger-grpc";
import type { RunStatusBadgeVariant, RunStatusDotVariant } from "./runStatus";

export type AutomationHealthKey =
  | "broken"
  | "failing"
  | "waiting_for_machine"
  | "skipping"
  | "degraded"
  | "healthy"
  | "no_result"
  | "new"
  | "paused";

export interface AutomationHealthDisplay {
  key: AutomationHealthKey;
  label: string;
  dotVariant: RunStatusDotVariant;
  badgeVariant: RunStatusBadgeVariant;
  /** Why, for a tooltip. Unset when the label says it all. */
  detail?: string;
  /** Pinned under "Needs attention". */
  needsAttention: boolean;
  /** Lower is worse. Rows sort by this, then by next fire. */
  severity: number;
}

/** The server's skip streak that turns DEGRADED into Skipping (§7.2: "the last 3+"). */
export const SKIPPING_STREAK = 3;

type Row = Omit<AutomationHealthDisplay, "label" | "detail"> & { label: string };

const ROWS: Record<AutomationHealthKey, Row> = {
  broken: { key: "broken", label: "Broken", dotVariant: "error", badgeVariant: "error", needsAttention: true, severity: -1 },
  failing: { key: "failing", label: "Failing", dotVariant: "error", badgeVariant: "error", needsAttention: true, severity: 0 },
  waiting_for_machine: {
    key: "waiting_for_machine",
    label: "Waiting for machine",
    dotVariant: "warning",
    badgeVariant: "warning",
    needsAttention: true,
    severity: 1,
  },
  skipping: { key: "skipping", label: "Skipping", dotVariant: "warning", badgeVariant: "warning", needsAttention: true, severity: 2 },
  degraded: { key: "degraded", label: "Degraded", dotVariant: "warning", badgeVariant: "warning", needsAttention: true, severity: 3 },
  no_result: { key: "no_result", label: "No result yet", dotVariant: "neutral", badgeVariant: "neutral", needsAttention: false, severity: 4 },
  new: { key: "new", label: "New", dotVariant: "neutral", badgeVariant: "neutral", needsAttention: false, severity: 4 },
  healthy: { key: "healthy", label: "Healthy", dotVariant: "active", badgeVariant: "success", needsAttention: false, severity: 5 },
  paused: { key: "paused", label: "Paused", dotVariant: "paused", badgeVariant: "neutral", needsAttention: false, severity: 6 },
};

export interface AutomationHealthInput {
  enabled: boolean;
  health: TriggerHealth;
  lastEvent?: TriggerEvent;
}

function detailOrUndefined(text: string): string | undefined {
  return text.trim() === "" ? undefined : text;
}

export function automationHealth({ enabled, health, lastEvent }: AutomationHealthInput): AutomationHealthDisplay {
  if (health.status === "broken") {
    const reason = detailOrUndefined(health.lastFailureDetail) ?? "Its workflow's declared trigger is missing or changed";
    return { ...ROWS.broken, detail: enabled ? reason : `Paused. ${reason}` };
  }

  if (!enabled) return { ...ROWS.paused };

  if (health.status === "failing") {
    return {
      ...ROWS.failing,
      label: `${health.consecutiveFailures} failed`,
      detail: detailOrUndefined(health.lastFailureDetail),
    };
  }

  if (lastEvent?.runDisplayState === RunDisplayState.WAITING_FOR_MACHINE) {
    return {
      ...ROWS.waiting_for_machine,
      detail: "The last run is waiting for its machine to wake up",
    };
  }

  switch (health.status) {
    case "degraded":
      if (health.consecutiveSkips >= SKIPPING_STREAK) {
        const reason = lastEvent?.outcome === "skipped" ? detailOrUndefined(lastEvent.outcomeDetail) : undefined;
        return {
          ...ROWS.skipping,
          detail: reason
            ? `Skipping: ${reason}`
            : `Skipped the last ${health.consecutiveSkips} times`,
        };
      }
      return { ...ROWS.degraded, detail: detailOrUndefined(health.lastFailureDetail) };
    case "healthy":
      return { ...ROWS.healthy };
    default:
      return lastEvent ? { ...ROWS.no_result } : { ...ROWS.new };
  }
}
