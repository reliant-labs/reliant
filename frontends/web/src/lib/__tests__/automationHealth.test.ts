// Copyright (c) 2025 Reliant Labs

/**
 * An automation's health, in the one vocabulary every surface uses, from the
 * server's TriggerHealth plus client facts: whether the trigger is enabled,
 * whether it has ever fired and what kind of source it has. Every server
 * status is crossed with the first two; each state carries one call to action.
 */

import { describe, expect, it } from "vitest";

import { RunDisplayState } from "../../gen/reliant/v1/run_pb";
import type { TriggerEvent, TriggerHealth, TriggerHealthStatusKey } from "@/hooks/trigger-model";
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
      label: "Recent failure",
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
      label: "4 failed in a row",
    },
    {
      server: "UNKNOWN",
      health: health("unknown"),
      key: "waiting_first_event",
      label: "Waiting for first event",
      firedOnly: ["no_result", "No result yet"],
    },
  ];

  for (const s of servers) {
    for (const enabled of [true, false]) {
      for (const fired of [true, false]) {
        const [key, label] = !enabled
          ? (["paused", "Off"] as const)
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
    expect(dot(health("healthy"))).toBe("success");
    expect(dot(health("failing", { consecutiveFailures: 2 }))).toBe("danger");
    expect(dot(health("degraded", { consecutiveSkips: 5 }))).toBe("warning");
    expect(dot(health("degraded", { consecutiveFailures: 1 }))).toBe("warning");
    expect(dot(health("unknown"), true, undefined)).toBe("neutral");
    expect(dot(health("healthy"), false)).toBe("neutral");
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
    expect(display.detail).toBe("Skipped the last 3: previous run still going");
  });

  it("only the states the user must act on need them; a single recent failure is a badge", () => {
    const attention = (h: TriggerHealth, enabled = true) =>
      automationHealth({ enabled, health: h, lastEvent: EVENT }).needsAttention;
    expect(attention(health("failing", { consecutiveFailures: 2 }))).toBe(true);
    expect(attention(health("degraded", { consecutiveSkips: 3 }))).toBe(true);
    expect(attention(health("degraded", { consecutiveFailures: 1 }))).toBe(false);
    expect(attention(health("needs_setup"))).toBe(true);
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
    expect(display.dotVariant).toBe("danger");
    expect(display.badgeVariant).toBe("error");
    expect(display.needsAttention).toBe(true);
    expect(display.severity).toBeLessThan(automationHealth({ enabled: true, health: health("failing", { consecutiveFailures: 9 }) }).severity);
  });

  it("stays Broken when paused: resuming would not fix it", () => {
    const display = automationHealth({ enabled: false, health: health("broken", { lastFailureDetail: reason }) });
    expect(display.key).toBe("broken");
    expect(display.detail).toBe(`Off. ${reason}`);
  });
});

describe("automationHealth: setup and launch states", () => {
  it("reads Needs setup with Finish setup, outranking Paused", () => {
    for (const enabled of [true, false]) {
      const display = automationHealth({ enabled, health: health("needs_setup", { lastFailureDetail: "Choose at least one repository to watch." }) });
      expect(display.key).toBe("needs_setup");
      expect(display.label).toBe("Needs setup");
      expect(display.detail).toBe("Choose at least one repository to watch.");
      expect(display.cta).toEqual({ kind: "finish_setup", label: "Finish setup" });
      expect(display.needsAttention).toBe(true);
    }
  });

  it("reads Can't start when the newest firing failed to launch, with the reason", () => {
    const display = automationHealth({
      enabled: true,
      health: health("failing", { consecutiveFailures: 2 }),
      lastEvent: { ...EVENT, outcome: "failed", outcomeDetail: "GitHub connection was revoked." },
    });
    expect(display.key).toBe("cant_start");
    expect(display.detail).toBe("GitHub connection was revoked.");
    expect(display.cta?.kind).toBe("fix_settings");
  });

  it("tells a schedule that has not fired from an event trigger that has not", () => {
    const schedule = automationHealth({
      enabled: true,
      health: health("unknown"),
      source: { kind: "schedule", schedule: { cron: ["0 9 * * 1"], timezone: "UTC" } } as never,
      nextFireAt: "2026-10-26T09:00:00Z",
    });
    expect(schedule.key).toBe("scheduled");
    expect(schedule.label).toMatch(/^Next run /);
    expect(schedule.cta?.kind).toBe("run_now");
    const event = automationHealth({ enabled: true, health: health("unknown") });
    expect(event.key).toBe("waiting_first_event");
    expect(event.cta).toEqual({ kind: "try_now", label: "Try it now" });
  });

  it("gives every state at most one call to action, and a broken one's is Remove trigger", () => {
    const broken = automationHealth({ enabled: true, health: health("broken", { lastFailureDetail: "its workflow does not resolve" }) });
    expect(broken.cta).toEqual({ kind: "remove_trigger", label: "Remove trigger" });
    expect(broken.detail).toBe("its workflow does not resolve");
    expect(automationHealth({ enabled: false, health: health("healthy") }).cta).toEqual({ kind: "resume", label: "Resume" });
    expect(automationHealth({ enabled: true, health: health("failing", { consecutiveFailures: 2 }), lastEvent: EVENT }).cta?.label).toBe("Open last run");
    expect(automationHealth({ enabled: true, health: health("healthy"), lastEvent: EVENT }).cta).toBeUndefined();
  });
});
