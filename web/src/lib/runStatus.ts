// Copyright (c) 2025 Reliant Labs

/**
 * The run-status vocabulary: one mapping from the wire lifecycle of a run to
 * the words and colors a user reads. Every surface that shows a run's state
 * (the sidebar dot, the workflow viewer, the mobile pill, automation history)
 * reads it from here, so "Paused" cannot mean one thing in the sidebar and
 * "running" in the viewer. The table is research/WORKFLOW_UI.md §0, "Display
 * vocabulary"; change it there first.
 *
 * Two things this module is deliberately NOT:
 *
 *   - Trigger-event outcomes. "Launched", "Skipped" and "Failed to launch"
 *     describe a FIRING, not a run. A launched event points at a run, and that
 *     run has its own status here. Showing "Launched" as a result is how a run
 *     that launched and then failed came to read as green on /automations.
 *   - Tool-call display state (ToolExecution's executing/completed/...), which
 *     describes a single call, not a run.
 */

import {
  ChatActivity,
  WorkflowState,
  WorkflowStopReason,
} from "../gen/reliant/v1/chat_pb";
import { RunDisplayState } from "../gen/reliant/v1/run_pb";

/** forge-ui StatusDot variants (components/forge-ui/status_dot.tsx). */
export type RunStatusDotVariant =
  | "active"
  | "paused"
  | "pending"
  | "error"
  | "warning"
  | "neutral";

/** forge-ui Badge variants (components/forge-ui/badge.tsx). */
export type RunStatusBadgeVariant = "success" | "warning" | "error" | "info" | "neutral";

/** A row of the lifecycle table. */
export type RunLifecycleKey =
  | "queued"
  | "running"
  | "needs_you"
  | "paused"
  | "completed"
  | "succeeded"
  | "failed"
  | "cancelled"
  | "unknown";

/**
 * Statuses that override the lifecycle row because the run is blocked on
 * something outside itself.
 *
 * "Waiting for machine" (§9, G7): a live run whose last tool call could not
 * reach its daemon. The signal travels as the chat's activity
 * (`ChatActivity.WAITING_FOR_DAEMON`), and the server folds the same thing
 * into `RunDisplayState.WAITING_FOR_MACHINE` for the Runs list.
 */
export type RunStatusOverlayKey = "waiting_for_machine";

export type RunStatusKey = RunLifecycleKey | RunStatusOverlayKey;

export interface RunStatusDisplay {
  key: RunStatusKey;
  label: string;
  dotVariant: RunStatusDotVariant;
  badgeVariant: RunStatusBadgeVariant;
  /** Only a run that is executing right now pulses. */
  pulse: boolean;
}

export interface RunStatusInput {
  state: WorkflowState;
  stopReason: WorkflowStopReason;
  /** The chat's derived activity, when the caller has it. */
  activity?: ChatActivity;
  /**
   * The workflow's declared verdict ("success" / "failure"), from the
   * terminal node it reached. Empty when the workflow declared none, which is
   * not a failure.
   */
  outcome?: string;
}

const LIFECYCLE_ROWS: Record<RunLifecycleKey, RunStatusDisplay> = {
  queued: { key: "queued", label: "Queued", dotVariant: "pending", badgeVariant: "info", pulse: false },
  running: { key: "running", label: "Running", dotVariant: "active", badgeVariant: "info", pulse: true },
  needs_you: { key: "needs_you", label: "Needs you", dotVariant: "warning", badgeVariant: "warning", pulse: false },
  paused: { key: "paused", label: "Paused", dotVariant: "paused", badgeVariant: "warning", pulse: false },
  completed: { key: "completed", label: "Completed", dotVariant: "neutral", badgeVariant: "success", pulse: false },
  succeeded: { key: "succeeded", label: "Succeeded", dotVariant: "neutral", badgeVariant: "success", pulse: false },
  failed: { key: "failed", label: "Failed", dotVariant: "error", badgeVariant: "error", pulse: false },
  cancelled: { key: "cancelled", label: "Cancelled", dotVariant: "neutral", badgeVariant: "neutral", pulse: false },
  unknown: { key: "unknown", label: "Unknown", dotVariant: "neutral", badgeVariant: "neutral", pulse: false },
};

const OVERLAY_ROWS: Record<RunStatusOverlayKey, RunStatusDisplay> = {
  waiting_for_machine: {
    key: "waiting_for_machine",
    label: "Waiting for machine",
    dotVariant: "pending",
    badgeVariant: "warning",
    pulse: false,
  },
};

/** The G7 hook point. See RunStatusOverlayKey. */
function overlayFor(input: { activity?: ChatActivity }): RunStatusOverlayKey | null {
  return input.activity === ChatActivity.WAITING_FOR_DAEMON ? "waiting_for_machine" : null;
}

/** The status of a run, from its root workflow's lifecycle. */
export function runStatus(input: RunStatusInput): RunStatusDisplay {
  const overlay = overlayFor(input);
  if (overlay !== null) return OVERLAY_ROWS[overlay];

  switch (input.state) {
    case WorkflowState.PENDING:
      return LIFECYCLE_ROWS.queued;
    case WorkflowState.ACTIVE:
      return input.activity === ChatActivity.AWAITING_INPUT
        ? LIFECYCLE_ROWS.needs_you
        : LIFECYCLE_ROWS.running;
    case WorkflowState.STOPPED:
      return stoppedStatus(input.stopReason, input.outcome);
    default:
      return LIFECYCLE_ROWS.unknown;
  }
}

function stoppedStatus(stopReason: WorkflowStopReason, outcome?: string): RunStatusDisplay {
  switch (stopReason) {
    case WorkflowStopReason.PAUSED:
      return LIFECYCLE_ROWS.paused;
    case WorkflowStopReason.COMPLETED:
      // A declared verdict replaces the bare "Completed": a run that routes to
      // a `failed` node finished executing, but did not pass.
      if (outcome === "success") return LIFECYCLE_ROWS.succeeded;
      if (outcome === "failure") return LIFECYCLE_ROWS.failed;
      return LIFECYCLE_ROWS.completed;
    case WorkflowStopReason.FAILED:
      return LIFECYCLE_ROWS.failed;
    case WorkflowStopReason.CANCELLED:
      return LIFECYCLE_ROWS.cancelled;
    default:
      return LIFECYCLE_ROWS.unknown;
  }
}

/**
 * The status of a chat's run from its live activity alone, for surfaces that
 * track activity (the sidebar, via activityStore) and not the lifecycle pair.
 * Returns null for an idle chat: there is nothing in progress to report.
 */
export function runStatusFromActivity(activity: ChatActivity): RunStatusDisplay | null {
  const overlay = overlayFor({ activity });
  if (overlay !== null) return OVERLAY_ROWS[overlay];

  switch (activity) {
    case ChatActivity.RUNNING:
      return LIFECYCLE_ROWS.running;
    case ChatActivity.AWAITING_INPUT:
      return LIFECYCLE_ROWS.needs_you;
    case ChatActivity.PAUSED:
      return LIFECYCLE_ROWS.paused;
    case ChatActivity.ERROR:
      return LIFECYCLE_ROWS.failed;
    default:
      return null;
  }
}

/**
 * The status of a run from the server's folded `Run.display_state`, which the
 * cross-cutting run list carries instead of the raw lifecycle triple. The
 * server derives it with the same table as `runStatus`, so the two agree.
 * `outcome` still refines a completed run into its declared verdict.
 */
export function runStatusFromDisplayState(
  displayState: RunDisplayState,
  outcome?: string,
): RunStatusDisplay {
  switch (displayState) {
    case RunDisplayState.QUEUED:
      return LIFECYCLE_ROWS.queued;
    case RunDisplayState.RUNNING:
      return LIFECYCLE_ROWS.running;
    case RunDisplayState.NEEDS_INPUT:
      return LIFECYCLE_ROWS.needs_you;
    case RunDisplayState.PAUSED:
      return LIFECYCLE_ROWS.paused;
    case RunDisplayState.COMPLETED:
      return stoppedStatus(WorkflowStopReason.COMPLETED, outcome);
    case RunDisplayState.FAILED:
      return LIFECYCLE_ROWS.failed;
    case RunDisplayState.CANCELLED:
      return LIFECYCLE_ROWS.cancelled;
    case RunDisplayState.WAITING_FOR_MACHINE:
      return OVERLAY_ROWS.waiting_for_machine;
    default:
      return LIFECYCLE_ROWS.unknown;
  }
}

/** Whether a status still has work ahead of it, so a view of it goes stale. */
export function isLiveRunStatus(status: RunStatusDisplay): boolean {
  return (
    status.key === "queued" ||
    status.key === "running" ||
    status.key === "needs_you" ||
    status.key === "paused" ||
    status.key === "waiting_for_machine"
  );
}

// ── Trigger-event outcomes ──────────────────────────────────────────────────

/** What one firing of an automation did. Not a run status. */
export type TriggerEventOutcomeKey = "launched" | "skipped" | "failed" | "unknown";

export interface TriggerEventOutcomeDisplay {
  label: string;
  badgeVariant: RunStatusBadgeVariant;
}

/**
 * The words for a firing. "Launched" is neutral on purpose: it says a run
 * started, nothing about how that run went, so it must never borrow the
 * success color. The run's own status comes from `runStatus`.
 */
const TRIGGER_EVENT_OUTCOMES: Record<TriggerEventOutcomeKey, TriggerEventOutcomeDisplay> = {
  launched: { label: "Launched", badgeVariant: "neutral" },
  skipped: { label: "Skipped", badgeVariant: "warning" },
  failed: { label: "Failed to launch", badgeVariant: "error" },
  unknown: { label: "Unknown", badgeVariant: "neutral" },
};

export function triggerEventOutcomeDisplay(outcome: TriggerEventOutcomeKey): TriggerEventOutcomeDisplay {
  return TRIGGER_EVENT_OUTCOMES[outcome];
}

// ── Launch kinds ────────────────────────────────────────────────────────────

/** `Chat.launch_kind` values the UI knows. Anything else renders generically. */
export type LaunchKind =
  | "chat.start"
  | "schedule"
  | "agent.start_run"
  | "builder.test"
  | "webhook"
  | "integration";

/** What the "Started by" line can name. Every field is optional. */
export interface LaunchContext {
  /** The automation's name, for schedule launches. */
  triggerName?: string;
  /** A "Run now" fire of a schedule (payload `manual: true`). */
  manual?: boolean;
  /** The slot a scheduled fire was for, already formatted ("Tue 09:00 (Europe/London)"). */
  scheduledFor?: string;
  /** The chat the starting agent was in, for agent.start_run. */
  parentChatTitle?: string;
  /** The webhook's name. */
  webhookName?: string;
  /** When the launch happened, already formatted ("14:02"). */
  at?: string;
  /** Integration provider ("GitHub", "Linear"). */
  providerName?: string;
  /** What happened at the provider ("issue #412 opened by @alice"). */
  providerDetail?: string;
}

export interface LaunchKindDisplay {
  /** The kind, with null (a chat that predates launch kinds) read as chat.start. */
  kind: string;
  shortLabel: string;
  startedByLine: string;
}

/**
 * Launch kinds a stored automation fires with nobody behind the run: a
 * schedule, a webhook delivery, a provider event, another run's outcome. Such a
 * run had its questions and approvals answered automatically. The server's
 * rule is `core.TriggerEventKind.Unattended` (internal/db/core/trigger.go),
 * which also decides the run's credentials and notifications; keep the two in
 * step. Anything not listed — a chat, a builder test, an agent's start_run, a
 * kind this client does not know — reads as attended.
 */
const UNATTENDED_LAUNCH_KINDS: ReadonlySet<string> = new Set(["schedule", "webhook", "integration", "workflow_event"]);

/** Whether a run of this launch kind ran with nobody behind it. */
export function isUnattendedLaunchKind(launchKind: string | null | undefined): boolean {
  return UNATTENDED_LAUNCH_KINDS.has(launchKind ?? "");
}

/** The launch-kind vocabulary (WORKFLOW_UI.md §0, "Launch-kind vocabulary"). */
export function launchKindDisplay(
  launchKind: string | null | undefined,
  context: LaunchContext = {},
): LaunchKindDisplay {
  const kind = launchKind || "chat.start";
  switch (kind) {
    case "chat.start":
      return { kind, shortLabel: "Chat", startedByLine: "Started by you" };
    case "schedule": {
      const name = context.triggerName;
      if (context.manual) {
        return {
          kind,
          shortLabel: "Run now",
          startedByLine: name ? `Started by Run now on ${name}` : "Started by Run now",
        };
      }
      const who = name ? `Started by schedule ${name}` : "Started by a schedule";
      return {
        kind,
        shortLabel: "Schedule",
        startedByLine: context.scheduledFor ? `${who} for ${context.scheduledFor}` : who,
      };
    }
    case "agent.start_run":
      return {
        kind,
        shortLabel: "Agent",
        startedByLine: context.parentChatTitle
          ? `Started by an agent in ${context.parentChatTitle}`
          : "Started by an agent",
      };
    case "builder.test":
      return { kind, shortLabel: "Test", startedByLine: "Test run from the builder" };
    case "webhook": {
      const who = context.webhookName ? `Started by webhook ${context.webhookName}` : "Started by a webhook";
      return { kind, shortLabel: "Webhook", startedByLine: context.at ? `${who} at ${context.at}` : who };
    }
    case "integration": {
      const provider = context.providerName || "Integration";
      return {
        kind,
        shortLabel: provider,
        startedByLine: context.providerDetail
          ? `Started by ${provider}: ${context.providerDetail}`
          : `Started by ${provider}`,
      };
    }
    default:
      // A kind this client does not know yet: name it rather than guess.
      return { kind, shortLabel: kind, startedByLine: `Started by ${kind}` };
  }
}
