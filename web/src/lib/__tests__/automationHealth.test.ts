// Copyright (c) 2025 Reliant Labs

/**
 * An automation's health, as research/WORKFLOW_UI.md §7.2 names it, from the
 * server's TriggerHealth plus two client facts: whether the trigger is enabled
 * and whether it has ever fired. Every server status is crossed with both.
 */

import { describe, expect, it } from "vitest";

import { RunDisplayState } from "../../gen/reliant/v1/run_pb";
import type { TriggerEvent, TriggerHealth, TriggerHealthStatusKey } from "../../api/trigger-grpc";
import { automationHealth, type AutomationHealthKey } from "../automationHealth";

const EVENT: TriggerEvent = {
  id: "ev-1",
  occurredAt: "2026-10-02T09:00:00Z",
  outcome: "launched",
  outcomeDetail: "",
  chatId: "chat-1",
  manual: false,
};

function health(status: TriggerHealthStatusKey, overrides: Partial<TriggerHealth> = {}): TriggerHealth {
  return { status, consecutiveFailures: 0, consecutiveSkips: 0, lastFailureDetail: "", ...overrides };
}

describe("automationHealth: every server status × enabled × never fired", () => {
  const cases: Array<{
    server: string;
    health: TriggerHealth;
    enabled: boolean;
    fired: boolean;
    key: AutomationHealthKey;
    label: string;
  }> = [];

  const servers: Array<{ server: string; health: TriggerHealth; key: AutomationHealthKey; label: string; firedOnly?: [AutomationHealthKey, string] }> = [
    { server: "HEALTHY", health: health("healthy"), key: "healthy", label: "Healthy" },
    {
      server: "DEGRADED (a failure)",
      health: health("degraded", { consecutiveFailures: 1, lastFailureDetail: "model unavailable" }),
      key: "degraded",
      label: "Degraded",
    },
    {
      server: "DEGRADED (3 skips)",
      health: health("degraded", { consecutiveSkips: 3 }),
      key: "skipping",
      label: "Skipping",
    },
    {
      server: "FAILING",
      health: health("failing", { consecutiveFailures: 4, lastFailureDetail: "run failed" }),
      key: "failing",
      label: "4 failed",
    },
    {
      server: "UNKNOWN",
      health: health("unknown"),
      key: "new",
      label: "New",
      firedOnly: ["no_result", "No result yet"],
    },
  ];

  for (const s of servers) {
    for (const enabled of [true, false]) {
      for (const fired of [true, false]) {
        const [key, label] = !enabled
          ? (["paused", "Paused"] as const)
          : fired && s.firedOnly
            ? s.firedOnly
            : [s.key, s.label];
        cases.push({ server: s.server, health: s.health, enabled, fired, key, label });
      }
    }
  }

  it.each(cases)("$server, enabled=$enabled, fired=$fired → $label", ({ health: h, enabled, fired, key, label }) => {
    const display = automationHealth({ enabled, health: h, lastEvent: fired ? EVENT : undefined });
    expect(display.key).toBe(key);
    expect(display.label).toBe(label);
  });

  it("uses the §7.2 dot variants", () => {
    const dot = (h: TriggerHealth, enabled = true, lastEvent: TriggerEvent | undefined = EVENT) =>
      automationHealth({ enabled, health: h, lastEvent }).dotVariant;
    expect(dot(health("healthy"))).toBe("active");
    expect(dot(health("failing", { consecutiveFailures: 2 }))).toBe("error");
    expect(dot(health("degraded", { consecutiveSkips: 5 }))).toBe("warning");
    expect(dot(health("degraded", { consecutiveFailures: 1 }))).toBe("warning");
    expect(dot(health("unknown"), true, undefined)).toBe("neutral");
    expect(dot(health("healthy"), false)).toBe("paused");
  });

  it("carries the server's failure detail for a tooltip", () => {
    const display = automationHealth({
      enabled: true,
      health: health("degraded", { consecutiveFailures: 1, lastFailureDetail: "model unavailable" }),
      lastEvent: EVENT,
    });
    expect(display.detail).toBe("model unavailable");
  });

  it("explains a skipping automation with the last skip's reason", () => {
    const display = automationHealth({
      enabled: true,
      health: health("degraded", { consecutiveSkips: 3 }),
      lastEvent: { ...EVENT, outcome: "skipped", outcomeDetail: "previous run still going" },
    });
    expect(display.detail).toBe("Skipping: previous run still going");
  });

  it("only the problem states need attention", () => {
    const attention = (h: TriggerHealth, enabled = true) =>
      automationHealth({ enabled, health: h, lastEvent: EVENT }).needsAttention;
    expect(attention(health("failing", { consecutiveFailures: 2 }))).toBe(true);
    expect(attention(health("degraded", { consecutiveSkips: 3 }))).toBe(true);
    expect(attention(health("degraded", { consecutiveFailures: 1 }))).toBe(true);
    expect(attention(health("healthy"))).toBe(false);
    expect(attention(health("unknown"))).toBe(false);
    // A paused automation is not firing, so it is not failing anyone.
    expect(attention(health("failing", { consecutiveFailures: 2 }), false)).toBe(false);
  });
});

describe("automationHealth: waiting for a machine", () => {
  const waitingEvent: TriggerEvent = {
    ...EVENT,
    runDisplayState: RunDisplayState.WAITING_FOR_MACHINE,
  };

  it("reads Waiting for machine when the last firing's run is blocked on its daemon", () => {
    for (const status of ["healthy", "degraded", "unknown"] as const) {
      const display = automationHealth({ enabled: true, health: health(status), lastEvent: waitingEvent });
      expect(display.key).toBe("waiting_for_machine");
      expect(display.label).toBe("Waiting for machine");
      expect(display.dotVariant).toBe("warning");
      expect(display.needsAttention).toBe(true);
    }
  });

  it("a failing streak still outranks it", () => {
    const display = automationHealth({
      enabled: true,
      health: health("failing", { consecutiveFailures: 3 }),
      lastEvent: waitingEvent,
    });
    expect(display.key).toBe("failing");
  });

  it("a paused automation is Paused whatever its last run is doing", () => {
    expect(automationHealth({ enabled: false, health: health("healthy"), lastEvent: waitingEvent }).key).toBe(
      "paused",
    );
  });
});

describe("automationHealth: broken activations", () => {
  const reason =
    'declared trigger "nightly" of workflow "triage": the workflow no longer declares a trigger named "nightly"';

  it("reads Broken with the server's reason, outranking every firing-derived state", () => {
    const display = automationHealth({
      enabled: true,
      health: health("broken", { lastFailureDetail: reason }),
      lastEvent: { ...EVENT, runDisplayState: RunDisplayState.WAITING_FOR_MACHINE },
    });
    expect(display.key).toBe("broken");
    expect(display.label).toBe("Broken");
    expect(display.detail).toBe(reason);
    expect(display.dotVariant).toBe("error");
    expect(display.badgeVariant).toBe("error");
    expect(display.needsAttention).toBe(true);
    expect(display.severity).toBeLessThan(automationHealth({ enabled: true, health: health("failing", { consecutiveFailures: 9 }) }).severity);
  });

  it("stays Broken when paused: resuming would not fix it", () => {
    const display = automationHealth({ enabled: false, health: health("broken", { lastFailureDetail: reason }) });
    expect(display.key).toBe("broken");
    expect(display.detail).toBe(`Paused. ${reason}`);
  });
});
