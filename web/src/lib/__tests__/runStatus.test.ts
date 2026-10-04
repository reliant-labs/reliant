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
  isLiveRunStatus,
  launchKindDisplay,
  runStatus,
  runStatusFromActivity,
  triggerEventOutcomeDisplay,
  type RunStatusInput,
} from "../runStatus";

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
      dot: "pending",
      badge: "info",
    },
    {
      wire: "ACTIVE",
      input: { state: WorkflowState.ACTIVE, stopReason: WorkflowStopReason.UNSPECIFIED },
      label: "Running",
      dot: "active",
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
      dot: "active",
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
      dot: "paused",
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
      dot: "error",
      badge: "error",
    },
    {
      wire: "STOPPED / FAILED",
      input: { state: WorkflowState.STOPPED, stopReason: WorkflowStopReason.FAILED },
      label: "Failed",
      dot: "error",
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
    [ChatActivity.RUNNING, "Running", "active"],
    [ChatActivity.AWAITING_INPUT, "Needs you", "warning"],
    [ChatActivity.PAUSED, "Paused", "paused"],
    [ChatActivity.ERROR, "Failed", "error"],
  ] as const)("activity %s reads %s", (activity, label, dot) => {
    const status = runStatusFromActivity(activity);
    expect(status?.label).toBe(label);
    expect(status?.dotVariant).toBe(dot);
  });

  it("an idle chat has no status to show", () => {
    expect(runStatusFromActivity(ChatActivity.IDLE)).toBeNull();
  });
});

describe("triggerEventOutcomeDisplay: a firing, never a run result", () => {
  it.each([
    ["launched", "Launched", "neutral"],
    ["skipped", "Skipped", "warning"],
    ["failed", "Failed to launch", "error"],
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
      "Run now",
      "Started by Run now on Nightly triage",
    ],
    [
      "agent.start_run",
      { parentChatTitle: "Refactor auth" },
      "Agent",
      "Started by an agent in Refactor auth",
    ],
    ["agent.start_run", {}, "Agent", "Started by an agent"],
    [
      "webhook",
      { webhookName: "deploy-hook", at: "14:02" },
      "Webhook",
      "Started by webhook deploy-hook at 14:02",
    ],
    [
      "integration",
      { providerName: "GitHub", providerDetail: "issue #412 opened by @alice" },
      "GitHub",
      "Started by GitHub: issue #412 opened by @alice",
    ],
  ] as const)("%s %j", (kind, context, shortLabel, startedByLine) => {
    const display = launchKindDisplay(kind, context);
    expect(display.shortLabel).toBe(shortLabel);
    expect(display.startedByLine).toBe(startedByLine);
  });

  it("treats null as chat.start", () => {
    expect(launchKindDisplay(null).kind).toBe("chat.start");
  });

  it("names a kind this client does not know rather than guessing", () => {
    expect(launchKindDisplay("builder.test")).toEqual({
      kind: "builder.test",
      shortLabel: "builder.test",
      startedByLine: "Started by builder.test",
    });
  });
});
