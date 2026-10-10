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
export type RunStatusOverlayKey = "waiting_for_machine" | "queued_for_machine";

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
  // The run ended because its machine never came up, and its message is
  // queued for the machine (`ChatActivity.QUEUED_FOR_MACHINE`): not failed —
  // the server sends it when the machine connects.
  queued_for_machine: {
    key: "queued_for_machine",
    label: "Queued for machine",
    dotVariant: "pending",
    badgeVariant: "warning",
    pulse: false,
  },
};

/** The G7 hook point. See RunStatusOverlayKey. */
function overlayFor(input: { activity?: ChatActivity }): RunStatusOverlayKey | null {
  switch (input.activity) {
    case ChatActivity.WAITING_FOR_DAEMON:
      return "waiting_for_machine";
    case ChatActivity.QUEUED_FOR_MACHINE:
      return "queued_for_machine";
    default:
      return null;
  }
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
  | "integration"
  | "workflow_event";

/** What the "Started by" line can name. Every field is optional. */
export interface LaunchContext {
  /**
   * The automation that fired the run, for every kind a stored automation
   * fires: a schedule, a webhook, a provider event, a workflow event.
   */
  triggerName?: string;
  /** A "Run now" fire of a schedule (payload `manual: true`). */
  manual?: boolean;
  /** The slot a scheduled fire was for, already formatted ("Tue 09:00 (Europe/London)"). */
  scheduledFor?: string;
  /** The chat the starting agent was in, for agent.start_run. */
  parentChatTitle?: string;
  /** When a webhook delivery arrived, already formatted ("14:02"). */
  at?: string;
  /** The integration a provider event came through ("github"). */
  providerName?: string;
  /** The provider event's type ("issues.opened"). */
  providerEvent?: string;
  /** For a workflow event: the workflow of the run whose outcome fired it, as a display name. */
  sourceWorkflow?: string;
  /** For a workflow event: that run's outcome ("finished", "failed", "blocked"). */
  sourceOutcome?: string;
}

/**
 * A "Started by" line split around the automation's name, so a surface can
 * render the name as a link without re-deriving the sentence:
 * `lead + automation + trail` is exactly the line.
 */
export interface StartedByParts {
  lead: string;
  automation: string;
  trail: string;
}

export interface LaunchKindDisplay {
  /** The kind, with null (a chat that predates launch kinds) read as chat.start. */
  kind: string;
  shortLabel: string;
  startedByLine: string;
  /** Set whenever the line names the automation that fired the run. */
  automationParts?: StartedByParts;
}

/** A line naming the automation, with the parts that let a surface link it. */
function naming(lead: string, automation: string, trail = ""): Pick<LaunchKindDisplay, "startedByLine" | "automationParts"> {
  return { startedByLine: `${lead}${automation}${trail}`, automationParts: { lead, automation, trail } };
}

/** A source run's outcome as the end of "when <workflow> …". */
const SOURCE_OUTCOME_WORDS: Record<string, string> = {
  finished: "finished",
  failed: "failed",
  blocked: "was blocked",
};

/** " when code-review finished", or "" when the source run is unknown. */
function sourceRunPhrase(context: LaunchContext): string {
  if (!context.sourceWorkflow) return "";
  const outcome = context.sourceOutcome ? (SOURCE_OUTCOME_WORDS[context.sourceOutcome] ?? context.sourceOutcome) : "ended";
  return ` when ${context.sourceWorkflow} ${outcome}`;
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
          ...(name ? naming("Started by Run now on ", name) : { startedByLine: "Started by Run now" }),
        };
      }
      const slot = context.scheduledFor ? ` for ${context.scheduledFor}` : "";
      return {
        kind,
        shortLabel: "Schedule",
        ...(name ? naming("Started by schedule ", name, slot) : { startedByLine: `Started by a schedule${slot}` }),
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
      // A webhook trigger's name is the webhook's name.
      const at = context.at ? ` at ${context.at}` : "";
      return {
        kind,
        shortLabel: "Webhook",
        ...(context.triggerName
          ? naming("Started by webhook ", context.triggerName, at)
          : { startedByLine: `Started by a webhook${at}` }),
      };
    }
    case "integration": {
      const provider = context.providerName;
      const source = provider ? (context.providerEvent ? `${provider}: ${context.providerEvent}` : provider) : "";
      return {
        kind,
        shortLabel: provider || "Integration",
        ...(context.triggerName
          ? naming("Started by ", context.triggerName, source ? ` on ${source}` : "")
          : { startedByLine: source ? `Started by ${source}` : "Started by an integration" }),
      };
    }
    case "workflow_event": {
      const phrase = sourceRunPhrase(context);
      return {
        kind,
        shortLabel: "Workflow event",
        ...(context.triggerName
          ? naming("Started by ", context.triggerName, phrase)
          : { startedByLine: phrase ? `Started${phrase}` : "Started by a workflow event" }),
      };
    }
    default:
      // A kind this client does not know yet: name it rather than guess.
      return { kind, shortLabel: kind, startedByLine: `Started by ${kind}` };
  }
}
