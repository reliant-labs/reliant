// Copyright (c) 2025 Reliant Labs

/**
 * An automation is one workflow turned on in one project: its activations
 * fold into one unit, a hand-made trigger is its own, and the unit reads as
 * its worst trigger (automation-ux-spec.md §2.1).
 */

import { describe, expect, it } from "vitest";

import type { Trigger, TriggerHealth } from "@/hooks/trigger-model";
import { aggregateHealth, automationUnits, groupAutomationUnits, unitsNeedingYou, workflowAutomationsFor } from "../automationUnits";

function health(status: TriggerHealth["status"], extra: Partial<TriggerHealth> = {}): TriggerHealth {
  return { status, consecutiveFailures: 0, consecutiveSkips: 0, lastFailureDetail: "", ...extra };
}

function trigger(overrides: Partial<Trigger> & { id: string }): Trigger {
  return {
    name: overrides.id,
    projectId: "p1",
    projectName: "Reliant",
    enabled: true,
    workflow: "builtin://autopilot-pr-review",
    presets: {},
    params: {},
    message: "",
    daemonId: "",
    noMachine: true,
    notifyOnComplete: false,
    actingIdentity: "owner",
    health: health("healthy"),
    createdAt: "",
    updatedAt: "",
    source: { kind: "unknown" } as Trigger["source"],
    webhookManaged: false,
    ...overrides,
  };
}

describe("groupAutomationUnits", () => {
  it("folds one workflow's activations in one project into one unit, and keeps custom triggers apart", () => {
    const { workflowAutomations, custom } = groupAutomationUnits([
      trigger({ id: "a", workflowTrigger: "pr-opened" }),
      trigger({ id: "b", workflowTrigger: "pr-ready" }),
      trigger({ id: "c", workflowTrigger: "pr-opened", projectId: "p2", projectName: "Docs" }),
      trigger({ id: "d", workflow: "autopilot-pr-review", workflowTrigger: "pr-synced" }),
      trigger({ id: "hand", workflow: "triage" }),
    ]);
    expect(workflowAutomations.map((u) => [u.projectId, u.triggers.map((t) => t.id)])).toEqual([
      ["p1", ["a", "b", "d"]],
      ["p2", ["c"]],
    ]);
    expect(custom.map((u) => u.trigger.id)).toEqual(["hand"]);
  });

  it("reads as its worst trigger, saying how many share it", () => {
    const h = aggregateHealth([
      trigger({ id: "a", workflowTrigger: "x" }),
      trigger({ id: "b", workflowTrigger: "y", health: health("failing", { consecutiveFailures: 2 }), lastEvent: { id: "e", occurredAt: "", outcome: "launched", outcomeDetail: "", manual: false } }),
      trigger({ id: "c", workflowTrigger: "z" }),
    ]);
    expect(h.key).toBe("failing");
    expect(h.label).toBe("2 failed in a row · 1 of 3 triggers");
  });

  it("is on only when every trigger is, and partly on otherwise", () => {
    const [unit] = groupAutomationUnits([trigger({ id: "a", workflowTrigger: "x" }), trigger({ id: "b", workflowTrigger: "y", enabled: false })]).workflowAutomations;
    expect(unit.enabled).toBe(false);
    expect(unit.partlyEnabled).toBe(true);
  });
});

describe("needs you", () => {
  it("lists the units in an attention state, worst first", () => {
    const units = automationUnits([
      trigger({ id: "fine", workflowTrigger: "x", workflow: "a" }),
      trigger({ id: "setup", workflowTrigger: "x", workflow: "b", health: health("needs_setup") }),
      trigger({ id: "broken", workflow: "c", health: health("broken") }),
    ]);
    expect(unitsNeedingYou(units).map((u) => u.health.key)).toEqual(["broken", "needs_setup"]);
  });
});

describe("workflowAutomationsFor", () => {
  it("finds a workflow's units by ref, with or without the builtin scheme", () => {
    const units = workflowAutomationsFor(
      [trigger({ id: "a", workflowTrigger: "x" }), trigger({ id: "b", workflow: "other", workflowTrigger: "x" }), trigger({ id: "c" })],
      "autopilot-pr-review",
    );
    expect(units.map((u) => u.triggers.map((t) => t.id))).toEqual([["a"]]);
  });
});
