// Copyright (c) 2025 Reliant Labs

/**
 * The run-status vocabulary, row by row against research/WORKFLOW_UI.md §0.
 * A failure here means a surface would show a user different words or colors
 * than the design says, so fix the module (or the doc first), not the test.
 */

import { describe, expect, it } from "vitest";

import {
  ChatActivity,
  WorkflowState,
  WorkflowStopReason,
} from "../../gen/reliant/v1/chat_pb";
import {
  chatListIndicators,
  isLiveRunStatus,
  isUnattendedLaunchKind,
  launchKindDisplay,
  runStatus,
  runStatusFromActivity,
  runStatusFromDisplayState,
  triggerEventOutcomeDisplay,
  type RunStatusInput,
} from "../runStatus";
import { RunDisplayState } from "../../gen/reliant/v1/run_pb";

describe("runStatus: the §0 display vocabulary", () => {
  const rows: Array<{
    wire: string;
    input: RunStatusInput;
    label: string;
    dot: string;
    badge: string;
  }> = [
    {
      wire: "PENDING",
      input: { state: WorkflowState.PENDING, stopReason: WorkflowStopReason.UNSPECIFIED },
      label: "Queued",
      dot: "neutral",
      badge: "info",
    },
    {
      wire: "ACTIVE",
      input: { state: WorkflowState.ACTIVE, stopReason: WorkflowStopReason.UNSPECIFIED },
      label: "Running",
      dot: "success",
      badge: "info",
    },
    {
      wire: "ACTIVE + activity RUNNING",
      input: {
        state: WorkflowState.ACTIVE,
        stopReason: WorkflowStopReason.UNSPECIFIED,
        activity: ChatActivity.RUNNING,
      },
      label: "Running",
      dot: "success",
      badge: "info",
    },
    {
      wire: "ACTIVE + activity AWAITING_INPUT",
      input: {
        state: WorkflowState.ACTIVE,
        stopReason: WorkflowStopReason.UNSPECIFIED,
        activity: ChatActivity.AWAITING_INPUT,
      },
      label: "Needs you",
      dot: "warning",
      badge: "warning",
    },
    {
      wire: "STOPPED / PAUSED",
      input: { state: WorkflowState.STOPPED, stopReason: WorkflowStopReason.PAUSED },
      label: "Paused",
      dot: "warning",
      badge: "warning",
    },
    {
      wire: "STOPPED / COMPLETED",
      input: { state: WorkflowState.STOPPED, stopReason: WorkflowStopReason.COMPLETED },
      label: "Completed",
      dot: "neutral",
      badge: "success",
    },
    {
      wire: "STOPPED / COMPLETED, outcome success",
      input: {
        state: WorkflowState.STOPPED,
        stopReason: WorkflowStopReason.COMPLETED,
        outcome: "success",
      },
      label: "Succeeded",
      dot: "neutral",
      badge: "success",
    },
    {
      wire: "STOPPED / COMPLETED, outcome failure",
      input: {
        state: WorkflowState.STOPPED,
        stopReason: WorkflowStopReason.COMPLETED,
        outcome: "failure",
      },
      label: "Failed",
      dot: "danger",
      badge: "error",
    },
    {
      wire: "STOPPED / FAILED",
      input: { state: WorkflowState.STOPPED, stopReason: WorkflowStopReason.FAILED },
      label: "Failed",
      dot: "danger",
      badge: "error",
    },
    {
      wire: "STOPPED / CANCELLED",
      input: { state: WorkflowState.STOPPED, stopReason: WorkflowStopReason.CANCELLED },
      label: "Cancelled",
      dot: "neutral",
      badge: "neutral",
    },
  ];

  it.each(rows)("$wire reads $label", ({ input, label, dot, badge }) => {
    const status = runStatus(input);
    expect(status.label).toBe(label);
    expect(status.dotVariant).toBe(dot);
    expect(status.badgeVariant).toBe(badge);
  });

  it("pulses only while a run is executing", () => {
    expect(runStatus(rows[1]!.input).pulse).toBe(true);
    for (const row of rows.filter((r) => r.label !== "Running")) {
      expect(runStatus(row.input).pulse).toBe(false);
    }
  });

  it("does not treat an unspecified state as any real status", () => {
    const status = runStatus({
      state: WorkflowState.UNSPECIFIED,
      stopReason: WorkflowStopReason.UNSPECIFIED,
    });
    expect(status.label).toBe("Unknown");
    expect(status.badgeVariant).toBe("neutral");
  });

  it("a stopped run with no declared outcome is Completed, not Failed (absence is not failure)", () => {
    const status = runStatus({
      state: WorkflowState.STOPPED,
      stopReason: WorkflowStopReason.COMPLETED,
      outcome: "",
    });
    expect(status.label).toBe("Completed");
  });

  it("knows which statuses are still live", () => {
    expect(rows.filter((r) => isLiveRunStatus(runStatus(r.input))).map((r) => r.label)).toEqual([
      "Queued",
      "Running",
      "Running",
      "Needs you",
      "Paused",
    ]);
  });
});

describe("runStatusFromActivity: the sidebar's view of the same vocabulary", () => {
  it.each([
    [ChatActivity.RUNNING, "Running", "success"],
    [ChatActivity.AWAITING_INPUT, "Needs you", "warning"],
    [ChatActivity.PAUSED, "Paused", "warning"],
    [ChatActivity.ERROR, "Failed", "danger"],
  ] as const)("activity %s reads %s", (activity, label, dot) => {
    const status = runStatusFromActivity(activity);
    expect(status?.label).toBe(label);
    expect(status?.dotVariant).toBe(dot);
  });

  it("an idle chat has no status to show", () => {
    expect(runStatusFromActivity(ChatActivity.IDLE)).toBeNull();
  });
});

describe("runStatusFromDisplayState: the server-folded state the Runs list carries", () => {
  it.each([
    [RunDisplayState.QUEUED, "Queued", "neutral", "info"],
    [RunDisplayState.RUNNING, "Running", "success", "info"],
    [RunDisplayState.NEEDS_INPUT, "Needs you", "warning", "warning"],
    [RunDisplayState.PAUSED, "Paused", "warning", "warning"],
    [RunDisplayState.COMPLETED, "Completed", "neutral", "success"],
    [RunDisplayState.FAILED, "Failed", "danger", "error"],
    [RunDisplayState.CANCELLED, "Cancelled", "neutral", "neutral"],
    [RunDisplayState.WAITING_FOR_MACHINE, "Waiting for machine", "neutral", "warning"],
  ] as const)("%s reads %s", (state, label, dot, badge) => {
    const status = runStatusFromDisplayState(state);
    expect(status.label).toBe(label);
    expect(status.dotVariant).toBe(dot);
    expect(status.badgeVariant).toBe(badge);
  });

  it("agrees with runStatus for every lifecycle row the server folds", () => {
    // The server derives display_state with the same table; a drift here
    // means the Runs list and a run's own header could disagree.
    expect(runStatusFromDisplayState(RunDisplayState.NEEDS_INPUT)).toEqual(
      runStatus({
        state: WorkflowState.ACTIVE,
        stopReason: WorkflowStopReason.UNSPECIFIED,
        activity: ChatActivity.AWAITING_INPUT,
      }),
    );
  });

  it("a completed run with a declared outcome reads the verdict", () => {
    expect(runStatusFromDisplayState(RunDisplayState.COMPLETED, "success").label).toBe("Succeeded");
    expect(runStatusFromDisplayState(RunDisplayState.COMPLETED, "failure").label).toBe("Failed");
  });

  it("waiting for a machine is still live", () => {
    expect(isLiveRunStatus(runStatusFromDisplayState(RunDisplayState.WAITING_FOR_MACHINE))).toBe(true);
  });

  it("unspecified is Unknown", () => {
    expect(runStatusFromDisplayState(RunDisplayState.UNSPECIFIED).label).toBe("Unknown");
  });
});

describe("waiting for a machine overrides the lifecycle row (G7)", () => {
  it("a live run whose chat is WAITING_FOR_DAEMON reads Waiting for machine", () => {
    const status = runStatus({
      state: WorkflowState.ACTIVE,
      stopReason: WorkflowStopReason.UNSPECIFIED,
      activity: ChatActivity.WAITING_FOR_DAEMON,
    });
    expect(status).toMatchObject({ label: "Waiting for machine", dotVariant: "neutral", badgeVariant: "warning" });
  });

  it("a live run held for a usage-limit reset reads Waiting for usage limit, not Running", () => {
    const status = runStatus({
      state: WorkflowState.ACTIVE,
      stopReason: WorkflowStopReason.UNSPECIFIED,
      activity: ChatActivity.WAITING_FOR_USAGE_LIMIT,
    });
    expect(status).toMatchObject({ label: "Waiting for usage limit", dotVariant: "neutral", badgeVariant: "warning", pulse: false });
    // Live: it retries by itself, so it can still be paused or stopped.
    expect(isLiveRunStatus(status)).toBe(true);
  });

  it("the sidebar's activity view says the same", () => {
    expect(runStatusFromActivity(ChatActivity.WAITING_FOR_DAEMON)?.label).toBe("Waiting for machine");
  });

  // The run ended because its machine never came up, but its message is
  // queued for the machine and goes out when it connects: not "Failed".
  it("a run that ended with its message QUEUED_FOR_MACHINE reads Queued for machine, not Failed", () => {
    const status = runStatus({
      state: WorkflowState.STOPPED,
      stopReason: WorkflowStopReason.FAILED,
      activity: ChatActivity.QUEUED_FOR_MACHINE,
    });
    expect(status).toMatchObject({ label: "Queued for machine", dotVariant: "neutral", badgeVariant: "warning", pulse: false });
    expect(runStatusFromActivity(ChatActivity.QUEUED_FOR_MACHINE)?.label).toBe("Queued for machine");
  });
});

describe("triggerEventOutcomeDisplay: a firing, never a run result", () => {
  it.each([
    ["launched", "Started a run", "neutral"],
    ["skipped", "Skipped", "warning"],
    ["failed", "Couldn't start", "error"],
    ["unknown", "Unknown", "neutral"],
  ] as const)("%s reads %s", (outcome, label, badge) => {
    expect(triggerEventOutcomeDisplay(outcome)).toEqual({ label, badgeVariant: badge });
  });

  it("Launched never borrows the success color", () => {
    expect(triggerEventOutcomeDisplay("launched").badgeVariant).not.toBe("success");
  });
});

describe("launchKindDisplay: the §0 launch-kind vocabulary", () => {
  it.each([
    ["chat.start", {}, "Chat", "Started by you"],
    [null, {}, "Chat", "Started by you"],
    [undefined, {}, "Chat", "Started by you"],
    ["", {}, "Chat", "Started by you"],
    [
      "schedule",
      { triggerName: "Nightly triage", scheduledFor: "Tue 09:00 (Europe/London)" },
      "Schedule",
      "Started by schedule Nightly triage for Tue 09:00 (Europe/London)",
    ],
    ["schedule", {}, "Schedule", "Started by a schedule"],
    [
      "schedule",
      { triggerName: "Nightly triage", manual: true },
      "Fired by hand",
      "Fired by hand: Nightly triage",
    ],
    [
      "agent.start_run",
      { parentChatTitle: "Refactor auth" },
      "Agent",
      "Started by an agent in Refactor auth",
    ],
    ["agent.start_run", {}, "Agent", "Started by an agent"],
    ["builder.test", {}, "Test", "Test run from the builder"],
    ["manual.run", {}, "Run by hand", "Run by you"],
    ["manual.run", { replayedEvent: true }, "Run by hand", "Run by you, replaying a past event"],
    [
      "webhook",
      { triggerName: "deploy-hook", at: "14:02" },
      "Webhook",
      "Started by webhook deploy-hook at 14:02",
    ],
    ["webhook", { at: "14:02" }, "Webhook", "Started by a webhook at 14:02"],
    [
      "integration",
      { triggerName: "Triage new issues", providerName: "github", providerEvent: "issues.opened" },
      "github",
      "Started by Triage new issues on github: issues.opened",
    ],
    ["integration", { providerName: "github", providerEvent: "issues.opened" }, "github", "Started by github: issues.opened"],
    ["integration", {}, "App event", "Started by an app event"],
    [
      "workflow_event",
      { triggerName: "After review", sourceWorkflow: "code-review", sourceOutcome: "finished" },
      "Workflow event",
      "Started by After review when code-review finished",
    ],
    [
      "workflow_event",
      { triggerName: "After review", sourceWorkflow: "code-review", sourceOutcome: "blocked" },
      "Workflow event",
      "Started by After review when code-review was blocked",
    ],
    ["workflow_event", { sourceWorkflow: "code-review", sourceOutcome: "failed" }, "Workflow event", "Started when code-review failed"],
    ["workflow_event", {}, "Workflow event", "Started by a workflow event"],
  ] as const)("%s %j", (kind, context, shortLabel, startedByLine) => {
    const display = launchKindDisplay(kind, context);
    expect(display.shortLabel).toBe(shortLabel);
    expect(display.startedByLine).toBe(startedByLine);
  });

  // A surface links the automation by rendering lead + link + trail, so the
  // parts must be the line exactly, and exist exactly when a name does.
  it.each(["schedule", "webhook", "integration", "workflow_event"])(
    "%s splits its line around the automation it names, and only then",
    (kind) => {
      const named = launchKindDisplay(kind, {
        triggerName: "Auto",
        scheduledFor: "Tue 09:00",
        at: "14:02",
        providerName: "github",
        providerEvent: "push",
        sourceWorkflow: "deploy",
        sourceOutcome: "failed",
      });
      expect(named.automationParts?.automation).toBe("Auto");
      const { lead, automation, trail } = named.automationParts!;
      expect(`${lead}${automation}${trail}`).toBe(named.startedByLine);
      expect(launchKindDisplay(kind, {}).automationParts).toBeUndefined();
    },
  );

  it.each(["chat.start", "agent.start_run", "builder.test", "manual.run"])("%s names no automation", (kind) => {
    expect(launchKindDisplay(kind, { triggerName: "Auto" }).automationParts).toBeUndefined();
  });

  it("treats null as chat.start", () => {
    expect(launchKindDisplay(null).kind).toBe("chat.start");
  });

  it("names a kind this client does not know rather than guessing", () => {
    expect(launchKindDisplay("future.kind")).toEqual({
      kind: "future.kind",
      shortLabel: "future.kind",
      startedByLine: "Started by future.kind",
    });
  });
});

describe("isUnattendedLaunchKind: core.TriggerEventKind.Unattended, mirrored", () => {
  it.each([
    ["schedule", true],
    ["webhook", true],
    ["integration", true],
    ["workflow_event", true],
    ["chat.start", false],
    ["agent.start_run", false],
    ["builder.test", false],
    ["manual.run", false],
  ])("%s → %s", (kind, unattended) => {
    expect(isUnattendedLaunchKind(kind)).toBe(unattended);
  });

  it("reads a missing or unknown kind as attended", () => {
    expect(isUnattendedLaunchKind(null)).toBe(false);
    expect(isUnattendedLaunchKind(undefined)).toBe(false);
    expect(isUnattendedLaunchKind("future.kind")).toBe(false);
  });
});

describe("chatListIndicators: needs-input, running and unread are separate signals", () => {
  it("a chat waiting on the user with agents still running shows both, and is not unread", () => {
    const ind = chatListIndicators({ activity: ChatActivity.AWAITING_INPUT, workingThreads: 2, unread: false });
    expect(ind.needsInput?.label).toBe("Needs your input · 2 agents still running");
    expect(ind.status?.key).toBe("running");
    expect(ind.status?.pulse).toBe(true);
    expect(ind.unread).toBe(false);
  });

  it("a chat waiting on the user with nothing else running has no run dot", () => {
    const ind = chatListIndicators({ activity: ChatActivity.AWAITING_INPUT, workingThreads: 0, unread: false });
    expect(ind.needsInput?.label).toBe("Needs your input");
    expect(ind.status).toBeNull();
  });

  it("one agent still running reads in the singular", () => {
    const ind = chatListIndicators({ activity: ChatActivity.AWAITING_INPUT, workingThreads: 1, unread: true });
    expect(ind.needsInput?.label).toBe("Needs your input · 1 agent still running");
    expect(ind.unread).toBe(true);
  });

  it("unread alone is not needs-input", () => {
    const ind = chatListIndicators({ activity: ChatActivity.IDLE, workingThreads: 0, unread: true });
    expect(ind.needsInput).toBeNull();
    expect(ind.status).toBeNull();
    expect(ind.unread).toBe(true);
  });

  it.each([
    [ChatActivity.RUNNING, "running"],
    [ChatActivity.PAUSED, "paused"],
    [ChatActivity.ERROR, "failed"],
    [ChatActivity.WAITING_FOR_DAEMON, "waiting_for_machine"],
  ] as const)("any other activity (%s) is the run dot alone", (activity, key) => {
    const ind = chatListIndicators({ activity, workingThreads: 3, unread: false });
    expect(ind.needsInput).toBeNull();
    expect(ind.status?.key).toBe(key);
  });

  it("runStatusFromActivity says agents are still running beside a question", () => {
    expect(runStatusFromActivity(ChatActivity.AWAITING_INPUT, 2)?.label).toBe("Needs you · 2 agents still running");
    expect(runStatusFromActivity(ChatActivity.AWAITING_INPUT, 2)?.key).toBe("needs_you");
    expect(runStatusFromActivity(ChatActivity.RUNNING, 2)?.label).toBe("Running");
  });
});
