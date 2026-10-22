// Copyright (c) 2025 Reliant Labs

/**
 * The Automations overview's order (automation-ux-spec.md §2.1): one unit per
 * automation, "Needs you" first and only there, then groups by workflow or
 * project, and the counts across the top.
 */

import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";

import type { Trigger, TriggerHealth } from "@/api/trigger-grpc";
import { RunDisplayState } from "@/gen/reliant/v1/run_pb";
import { IntegrationSourceSchema } from "@/gen/reliant/v1/trigger_pb";
import { automationCounts, automationOverview, groupSummary, unitTitle } from "../automationGrouping";
import { unitStartsOn } from "../AutomationUnitRow";

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
    noMachine: false,
    notifyOnComplete: false,
    actingIdentity: "owner",
    health: health("healthy"),
    createdAt: "",
    updatedAt: "",
    nextFireAt: inMinutes(60),
    lastEvent: { id: "e", occurredAt: "", outcome: "launched", outcomeDetail: "", manual: false },
    source: { kind: "schedule", schedule: { cron: ["0 9 * * *"], timezone: "UTC", overlap: "skip" } },
    ...overrides,
  } as Trigger;
}

const keys = (units: Array<{ key: string }>) => units.map((u) => u.key);

describe("automationOverview", () => {
  it("folds a workflow's triggers in one project into one row, and keeps custom triggers as their own", () => {
    const { groups, all } = automationOverview(
      [
        trigger({ id: "a", workflow: "builtin://autopilot-pr-review", workflowTrigger: "pr-opened" }),
        trigger({ id: "b", workflow: "builtin://autopilot-pr-review", workflowTrigger: "pr-ready" }),
        trigger({ id: "custom", workflow: "builtin://agent" }),
      ],
      "workflow",
    );
    expect(all).toHaveLength(2);
    expect(groups.map((g) => [g.key, keys(g.units)])).toEqual([
      ["workflow:builtin://agent", ["custom:custom"]],
      ["workflow:builtin://autopilot-pr-review", ["autopilot-pr-review::p1"]],
    ]);
  });

  it("groups by project, using the server's project name", () => {
    const { groups } = automationOverview(
      [trigger({ id: "a" }), trigger({ id: "b", projectId: "p2", projectName: "Docs" }), trigger({ id: "c", projectId: "p3", projectName: undefined })],
      "project",
    );
    expect(groups.map((g) => g.label)).toEqual(["Deleted project", "Docs", "Reliant"]);
  });

  it("lists what needs you first and only there, worst first; a single recent failure stays put", () => {
    const { needsYou, groups } = automationOverview(
      [
        trigger({ id: "fine" }),
        trigger({ id: "recent", health: health("degraded", { consecutiveFailures: 1 }) }),
        trigger({ id: "skipping", health: health("degraded", { consecutiveSkips: 3 }) }),
        trigger({ id: "failing", health: health("failing", { consecutiveFailures: 2 }), workflow: "other" }),
        trigger({ id: "setup", workflow: "review", workflowTrigger: "pr-opened", health: health("needs_setup") }),
        trigger({
          id: "waiting",
          lastEvent: { id: "e", occurredAt: "", outcome: "launched", outcomeDetail: "", manual: false, runDisplayState: RunDisplayState.WAITING_FOR_MACHINE },
        }),
      ],
      "workflow",
    );
    expect(keys(needsYou)).toEqual(["review::p1", "custom:failing", "custom:waiting", "custom:skipping"]);
    expect(groups.map((g) => [g.label, keys(g.units)])).toEqual([["Agent", ["custom:recent", "custom:fine"]]]);
  });

  it("sorts within a group by health, then next run, then name", () => {
    const { groups } = automationOverview(
      [
        trigger({ id: "later", nextFireAt: inMinutes(120) }),
        trigger({ id: "sooner", nextFireAt: inMinutes(10) }),
        trigger({ id: "paused", enabled: false }),
      ],
      "workflow",
    );
    expect(keys(groups[0]!.units)).toEqual(["custom:sooner", "custom:later", "custom:paused"]);
  });

  it("names a workflow automation by its workflow and a custom one by itself", () => {
    const { all } = automationOverview([trigger({ id: "a", workflow: "my-flow", workflowTrigger: "x" }), trigger({ id: "Nightly digest", name: "Nightly digest" })], "workflow");
    expect(all.map(unitTitle).sort()).toEqual(["My Flow", "Nightly digest"]);
  });
});

describe("counts", () => {
  it("counts what needs you, what is on and healthy, what is paused, and the next run", () => {
    const { all } = automationOverview(
      [
        trigger({ id: "a", nextFireAt: inMinutes(30) }),
        trigger({ id: "b", nextFireAt: inMinutes(5) }),
        trigger({ id: "c", enabled: false }),
        trigger({ id: "d", health: health("broken") }),
      ],
      "workflow",
    );
    expect(automationCounts(all)).toEqual({ needYou: 1, healthyOn: 2, paused: 1, nextRunAt: inMinutes(5) });
    expect(groupSummary(all)).toBe("4 automations · 3 on");
  });
});

describe("unitStartsOn", () => {
  const github = (events: string[]) =>
    ({
      kind: "activation",
      workflowTrigger: "x",
      declared: { kind: "passthrough", arm: { case: "integration", value: create(IntegrationSourceSchema, { integration: "github", events }) } },
    }) as unknown as Trigger["source"];

  it("names the one integration a workflow's triggers share, even before the catalog has loaded", () => {
    const { all } = automationOverview(
      [
        trigger({ id: "a", workflow: "builtin://autopilot-pr-review", workflowTrigger: "pr-opened", source: github(["pull_request.opened"]) }),
        trigger({ id: "b", workflow: "builtin://autopilot-pr-review", workflowTrigger: "pr-synced", source: github(["pull_request.synchronize"]) }),
      ],
      "workflow",
    );
    expect(unitStartsOn(all[0]!)).toBe("GitHub · 2 triggers");
    expect(unitStartsOn(all[0]!, { integration: () => "GitHub Enterprise", event: (_i, e) => e })).toBe("GitHub Enterprise · 2 triggers");
  });
});
