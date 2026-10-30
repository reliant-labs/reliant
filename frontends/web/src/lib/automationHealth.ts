// Copyright (c) 2025 Reliant Labs

/**
 * The health vocabulary for an automation: what its state is called, in which
 * colour, the one-line reason, whether it belongs under "Needs you", and the
 * ONE thing to do about it. Every surface — the Workflows list, the workflow
 * page's Triggers card, the Inbox — reads its words from here, so a state is
 * called the same thing everywhere.
 *
 * The SERVER decides health (TriggerHealth, from the last 10 firings and the
 * declaration; rules on TriggerHealthStatus in proto/reliant/v1/trigger.proto).
 * The display has more states than the server, because some facts are the
 * client's. Precedence, first match wins:
 *
 *   Broken               BROKEN: the activation cannot follow its workflow
 *                        any more (gone, does not parse, no longer declares
 *                        the trigger, or changed its kind); the detail says
 *                        which. Resuming would not make it fire, so the
 *                        action is Remove trigger; the switch pauses it.
 *   Needs setup          NEEDS_SETUP: a setting the workflow now requires is
 *                        missing. Outranks Off: Finish setup also turns it on.
 *   Off                  disabled ("paused" in code). Not firing, so not failing either.
 *   Can't start          the newest firing failed to launch (no run started).
 *   Failing              FAILING, "N failed in a row".
 *   Waiting for machine  the last run is blocked on its machine.
 *   Skipping             DEGRADED with 3+ skips in a row.
 *   Recent failure       any other DEGRADED. Not "needs you": a badge only.
 *   Healthy              HEALTHY.
 *   No result yet        UNKNOWN, has fired, no verdict yet.
 *   Next run / Waiting   UNKNOWN, never fired: a schedule says when it runs
 *   for first event      next, an event trigger what it is waiting for.
 */

import { RunDisplayState } from "../gen/reliant/v1/run_pb";
import { triggerSchedule, type Trigger, type TriggerEvent, type TriggerHealth } from "@/hooks/trigger-model";
import type { RunStatusBadgeVariant, RunStatusDotVariant } from "./runStatus";

export type AutomationHealthKey =
  | "broken"
  | "needs_setup"
  | "paused"
  | "cant_start"
  | "failing"
  | "waiting_for_machine"
  | "skipping"
  | "degraded"
  | "healthy"
  | "no_result"
  | "scheduled"
  | "waiting_first_event";

/** What the one call to action does; AutomationCta wires each kind. */
export type AutomationCtaKind =
  | "open_workflow"
  | "remove_trigger"
  | "finish_setup"
  | "fix_settings"
  | "open_last_run"
  | "open_run"
  | "see_why"
  | "resume"
  | "try_now"
  | "run_now";

export interface AutomationCtaSpec {
  kind: AutomationCtaKind;
  label: string;
}

export interface AutomationHealthDisplay {
  key: AutomationHealthKey;
  label: string;
  dotVariant: RunStatusDotVariant;
  badgeVariant: RunStatusBadgeVariant;
  /** The reason line ("Choose at least one repository to watch."). Unset when the label says it all. */
  detail?: string;
  /** Listed under "Needs you" and counted in the Automations badge. */
  needsAttention: boolean;
  /** Lower is worse. Rows sort by this, then by next fire. */
  severity: number;
  /** The one primary call to action; unset when there is nothing to do. */
  cta?: AutomationCtaSpec;
}

/** The server's skip streak that turns DEGRADED into Skipping. */
export const SKIPPING_STREAK = 3;

type Row = Omit<AutomationHealthDisplay, "detail" | "cta">;

const ROWS: Record<AutomationHealthKey, Row> = {
  broken: { key: "broken", label: "Broken", dotVariant: "danger", badgeVariant: "error", needsAttention: true, severity: -2 },
  needs_setup: { key: "needs_setup", label: "Needs setup", dotVariant: "warning", badgeVariant: "warning", needsAttention: true, severity: -1 },
  cant_start: { key: "cant_start", label: "Can't start", dotVariant: "danger", badgeVariant: "error", needsAttention: true, severity: 0 },
  failing: { key: "failing", label: "Failing", dotVariant: "danger", badgeVariant: "error", needsAttention: true, severity: 1 },
  waiting_for_machine: {
    key: "waiting_for_machine",
    label: "Waiting for machine",
    dotVariant: "warning",
    badgeVariant: "warning",
    needsAttention: true,
    severity: 2,
  },
  skipping: { key: "skipping", label: "Skipping", dotVariant: "warning", badgeVariant: "warning", needsAttention: true, severity: 3 },
  degraded: { key: "degraded", label: "Recent failure", dotVariant: "warning", badgeVariant: "warning", needsAttention: false, severity: 4 },
  no_result: { key: "no_result", label: "No result yet", dotVariant: "neutral", badgeVariant: "neutral", needsAttention: false, severity: 5 },
  scheduled: { key: "scheduled", label: "Scheduled", dotVariant: "neutral", badgeVariant: "neutral", needsAttention: false, severity: 5 },
  waiting_first_event: {
    key: "waiting_first_event",
    label: "Waiting for first event",
    dotVariant: "neutral",
    badgeVariant: "neutral",
    needsAttention: false,
    severity: 5,
  },
  healthy: { key: "healthy", label: "Healthy", dotVariant: "success", badgeVariant: "success", needsAttention: false, severity: 6 },
  paused: { key: "paused", label: "Off", dotVariant: "neutral", badgeVariant: "neutral", needsAttention: false, severity: 7 },
};

export interface AutomationHealthInput {
  enabled: boolean;
  health: TriggerHealth;
  lastEvent?: TriggerEvent;
  /** Tells a schedule (Next run) from an event trigger (Waiting for first event). */
  source?: Trigger["source"];
  nextFireAt?: string;
}

function detailOrUndefined(text: string | undefined): string | undefined {
  return text && text.trim() !== "" ? text : undefined;
}

/** "Mon 09:00" for the Scheduled label. Locale-aware, short. */
function shortWhen(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "";
  return date.toLocaleString(undefined, { weekday: "short", hour: "2-digit", minute: "2-digit" });
}

export function automationHealth({ enabled, health, lastEvent, source, nextFireAt }: AutomationHealthInput): AutomationHealthDisplay {
  if (health.status === "broken") {
    const reason = detailOrUndefined(health.lastFailureDetail) ?? "It no longer matches its workflow.";
    // Remove, not Open workflow: the workflow may be gone (deleted from the
    // repo, or no longer parsing on this server), and the activation is what
    // keeps firing into a refusal. Fixing the workflow file is the other way
    // out; the reason says what changed.
    return {
      ...ROWS.broken,
      detail: enabled ? reason : `Off. ${reason}`,
      cta: { kind: "remove_trigger", label: "Remove trigger" },
    };
  }

  if (health.status === "needs_setup") {
    return {
      ...ROWS.needs_setup,
      detail: detailOrUndefined(health.lastFailureDetail) ?? "A setting this workflow now requires is missing.",
      cta: { kind: "finish_setup", label: "Finish setup" },
    };
  }

  if (!enabled) return { ...ROWS.paused, cta: { kind: "resume", label: "Resume" } };

  if (lastEvent?.outcome === "failed") {
    return {
      ...ROWS.cant_start,
      detail: detailOrUndefined(lastEvent.outcomeDetail) ?? detailOrUndefined(health.lastFailureDetail),
      cta: { kind: "fix_settings", label: "Open settings" },
    };
  }

  if (health.status === "failing") {
    return {
      ...ROWS.failing,
      label: `${health.consecutiveFailures} failed in a row`,
      detail: detailOrUndefined(health.lastFailureDetail),
      cta: { kind: "open_last_run", label: "Open last run" },
    };
  }

  if (lastEvent?.runDisplayState === RunDisplayState.WAITING_FOR_MACHINE) {
    return {
      ...ROWS.waiting_for_machine,
      detail: "The last run is waiting for its machine to come online.",
      cta: { kind: "open_run", label: "Open run" },
    };
  }

  switch (health.status) {
    case "degraded":
      if (health.consecutiveSkips >= SKIPPING_STREAK) {
        const reason = lastEvent?.outcome === "skipped" ? detailOrUndefined(lastEvent.outcomeDetail) : undefined;
        return {
          ...ROWS.skipping,
          detail: reason
            ? `Skipped the last ${health.consecutiveSkips}: ${reason}`
            : `Skipped the last ${health.consecutiveSkips} times.`,
          cta: { kind: "see_why", label: "See why" },
        };
      }
      return { ...ROWS.degraded, detail: detailOrUndefined(health.lastFailureDetail), cta: { kind: "open_last_run", label: "Open last run" } };
    case "healthy":
      return { ...ROWS.healthy };
    default:
      if (lastEvent) return { ...ROWS.no_result, detail: "The last run is still going.", cta: { kind: "open_run", label: "Open run" } };
      if (source && triggerSchedule({ source })) {
        const when = nextFireAt ? shortWhen(nextFireAt) : "";
        return { ...ROWS.scheduled, label: when ? `Next run ${when}` : "Scheduled", cta: { kind: "run_now", label: "Run…" } };
      }
      return { ...ROWS.waiting_first_event, cta: { kind: "try_now", label: "Try it now" } };
  }
}

/** The worst of several automations' health, and whether they agree. */
export function worstHealth(healths: readonly AutomationHealthDisplay[]): AutomationHealthDisplay | undefined {
  let worst: AutomationHealthDisplay | undefined;
  for (const health of healths) {
    if (!worst || health.severity < worst.severity) worst = health;
  }
  return worst;
}
