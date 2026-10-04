// Copyright (c) 2025 Reliant Labs

/**
 * Grouping and order for the Automations list (WORKFLOW_UI.md §7.2): which
 * group each automation lands in, the order within a group, and when the
 * "Needs attention" group exists.
 */

import { describe, expect, it } from "vitest";

import type { Trigger, TriggerHealth } from "@/api/trigger-grpc";
import { RunDisplayState } from "@/gen/reliant/v1/run_pb";
import { groupAutomations, groupSummary } from "../automationGrouping";

const NOW = Date.parse("2026-10-02T08:00:00Z");
const inMinutes = (minutes: number) => new Date(NOW + minutes * 60_000).toISOString();

function health(status: TriggerHealth["status"], overrides: Partial<TriggerHealth> = {}): TriggerHealth {
  return { status, consecutiveFailures: 0, consecutiveSkips: 0, lastFailureDetail: "", ...overrides };
}

function trigger(overrides: Partial<Trigger> & Pick<Trigger, "id">): Trigger {
  return {
    name: overrides.id,
    projectId: "p1",
    projectName: "Reliant",
    enabled: true,
    workflow: "builtin://agent",
    presets: {},
    params: {},
    message: "",
    daemonId: "d1",
    daemonName: "MacBook",
    health: health("healthy"),
    createdAt: "",
    updatedAt: "",
    nextFireAt: inMinutes(60),
    lastEvent: { id: "e", occurredAt: "", outcome: "launched", outcomeDetail: "", manual: false },
    schedule: { cron: ["0 9 * * *"], timezone: "UTC", overlap: "skip" },
    ...overrides,
  };
}

const ids = (entries: Array<{ trigger: Trigger }>) => entries.map((entry) => entry.trigger.id);

describe("groupAutomations", () => {
  it("groups by workflow, labelled by the workflow's name, groups in label order", () => {
    const { groups } = groupAutomations(
      [
        trigger({ id: "a", workflow: "workflow://weekly-report" }),
        trigger({ id: "b", workflow: "builtin://agent" }),
        trigger({ id: "c", workflow: "" }),
        trigger({ id: "d", workflow: "workflow://weekly-report" }),
      ],
      "workflow",
    );
    expect(groups.map((g) => [g.label, ids(g.entries)])).toEqual([
      ["Agent", ["b"]],
      ["Default workflow", ["c"]],
      ["Weekly Report", ["a", "d"]],
    ]);
  });

  it("groups by project, using the server's project name", () => {
    const { groups } = groupAutomations(
      [
        trigger({ id: "a", projectId: "p2", projectName: "Zeta" }),
        trigger({ id: "b", projectId: "p1", projectName: "Alpha" }),
        trigger({ id: "c", projectId: "p3", projectName: undefined }),
      ],
      "project",
    );
    expect(groups.map((g) => g.label)).toEqual(["Alpha", "Deleted project", "Zeta"]);
  });

  it("sorts within a group by health severity, then next fire, then name", () => {
    const { groups } = groupAutomations(
      [
        trigger({ id: "paused", enabled: false, nextFireAt: inMinutes(1) }),
        trigger({ id: "healthy-late", nextFireAt: inMinutes(300) }),
        trigger({ id: "new", health: health("unknown"), lastEvent: undefined, nextFireAt: inMinutes(500) }),
        trigger({ id: "healthy-soon", nextFireAt: inMinutes(5) }),
        trigger({ id: "healthy-none", nextFireAt: undefined }),
      ],
      "workflow",
    );
    expect(ids(groups[0]!.entries)).toEqual([
      "new",
      "healthy-soon",
      "healthy-late",
      "healthy-none",
      "paused",
    ]);
  });

  it("pins Failing / Waiting / Skipping / Degraded under Needs attention, worst first, and only there", () => {
    const { attention, groups } = groupAutomations(
      [
        trigger({ id: "fine" }),
        trigger({ id: "degraded", health: health("degraded", { consecutiveFailures: 1 }) }),
        trigger({ id: "skipping", health: health("degraded", { consecutiveSkips: 3 }) }),
        trigger({ id: "failing", health: health("failing", { consecutiveFailures: 2 }), workflow: "workflow://other" }),
        trigger({
          id: "waiting",
          lastEvent: {
            id: "e",
            occurredAt: "",
            outcome: "launched",
            outcomeDetail: "",
            manual: false,
            runDisplayState: RunDisplayState.WAITING_FOR_MACHINE,
          },
        }),
      ],
      "workflow",
    );
    expect(attention?.label).toBe("Needs attention");
    expect(ids(attention!.entries)).toEqual(["failing", "waiting", "skipping", "degraded"]);
    // The "other" workflow's only automation moved up, so its group is gone.
    expect(groups.map((g) => [g.label, ids(g.entries)])).toEqual([["Agent", ["fine"]]]);
  });

  it("has no Needs attention group when nothing needs it", () => {
    const { attention } = groupAutomations(
      [
        trigger({ id: "a" }),
        trigger({ id: "b", enabled: false, health: health("failing", { consecutiveFailures: 5 }) }),
        trigger({ id: "c", health: health("unknown"), lastEvent: undefined }),
      ],
      "workflow",
    );
    expect(attention).toBeUndefined();
  });

  it("an empty list has no groups at all", () => {
    expect(groupAutomations([], "workflow")).toEqual({ attention: undefined, groups: [] });
  });
});

describe("groupSummary", () => {
  it("counts automations and how many are on", () => {
    const { groups } = groupAutomations(
      [trigger({ id: "a" }), trigger({ id: "b", enabled: false }), trigger({ id: "c" })],
      "workflow",
    );
    expect(groupSummary(groups[0]!.entries)).toBe("3 automations · 2 on");
    expect(groupSummary(groups[0]!.entries.slice(0, 1))).toBe("1 automation · 1 on");
  });
});
