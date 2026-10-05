// Copyright (c) 2025 Reliant Labs

/**
 * What the Library shows for a search, source and sort (WORKFLOW_UI.md §2.2):
 * search over name and description, the source filter, "Recently run" as one
 * list, and "Needs attention" for workflows whose automations are FAILING.
 */

import { describe, expect, it } from "vitest";

import type { RunSummary } from "@/api/run-grpc";
import type { Trigger, TriggerHealthStatusKey } from "@/api/trigger-grpc";
import type { InvalidWorkflow, WorkflowResponse } from "@/api/workflow-grpc";
import { automationsSummary, libraryView } from "../library/libraryView";

describe("automationsSummary (the Library's Automations column)", () => {
  it("is null for none — the cell's dash under a labelled column", () => {
    expect(automationsSummary(0)).toBeNull();
  });
  it("counts, and names the failing ones", () => {
    expect(automationsSummary(1)).toBe("1 automation");
    expect(automationsSummary(2)).toBe("2 automations");
    expect(automationsSummary(2, 1)).toBe("2 automations · 1 failing");
  });
});

function wf(name: string, source: WorkflowResponse["source"], description = ""): WorkflowResponse {
  return { name, filename: name, source, description, stepCount: 1, status: "complete", validationErrors: [], nodes: [], edges: [] };
}

function trigger(id: string, workflow: string, status: TriggerHealthStatusKey = "healthy", enabled = true): Trigger {
  return {
    id,
    name: id,
    projectId: "p",
    enabled,
    workflow,
    presets: {},
    params: {},
    message: "",
    daemonId: "d",
    notifyOnComplete: false,
    health: { status, consecutiveFailures: status === "failing" ? 3 : 0, consecutiveSkips: 0, lastFailureDetail: "" },
    createdAt: "",
    updatedAt: "",
  };
}

function ran(workflowName: string, minutesAgo: number): [string, RunSummary] {
  return [workflowName, { workflowName, createdAt: Date.now() - minutesAgo * 60_000 } as RunSummary];
}

const workflows = [
  wf("builtin://agent", "builtin", "General-purpose coding agent"),
  wf("builtin://planner", "builtin", "Plans before it codes"),
  wf("triage", "project", "Triage new GitHub issues"),
  wf("release-notes", "user", "Draft the weekly release notes"),
];
const invalid: InvalidWorkflow[] = [
  { name: "broken", source: "project", path: ".reliant/workflows/broken.yaml", errors: ["bad indent"] },
];
const names = (view: ReturnType<typeof libraryView>) =>
  Object.fromEntries(view.sections.map((section) => [section.key, section.workflows.map((w) => w.name)]));

describe("libraryView", () => {
  it("by default keeps the origin sections, sorted by name, and the broken definitions", () => {
    const view = libraryView({ workflows, invalid, triggers: [] });
    expect(names(view)).toEqual({
      yours: ["release-notes", "triage"],
      builtin: ["builtin://agent", "builtin://planner"],
    });
    expect(view.invalid).toHaveLength(1);
    expect(view.noMatches).toBe(false);
  });

  it("search matches the display name and the description, case-insensitively", () => {
    expect(names(libraryView({ workflows, invalid, triggers: [], q: "GITHUB" }))).toEqual({ yours: ["triage"] });
    expect(names(libraryView({ workflows, invalid, triggers: [], q: "release notes" }))).toEqual({
      yours: ["release-notes"],
    });
    // A broken definition matches on its file path; it has no description.
    expect(libraryView({ workflows, invalid, triggers: [], q: "broken.yaml" }).invalid).toHaveLength(1);
    expect(libraryView({ workflows, invalid, triggers: [], q: "agent" }).invalid).toHaveLength(0);
  });

  it("source narrows to one section", () => {
    expect(names(libraryView({ workflows, invalid, triggers: [], source: "builtin" }))).toEqual({
      builtin: ["builtin://agent", "builtin://planner"],
    });
    expect(libraryView({ workflows, invalid, triggers: [], source: "builtin" }).invalid).toEqual([]);
    const failed = libraryView({ workflows, invalid, triggers: [], source: "failed" });
    expect(failed.sections).toEqual([]);
    expect(failed.invalid).toHaveLength(1);
    expect(names(libraryView({ workflows, invalid, triggers: [], source: "yours" }))).toEqual({
      yours: ["release-notes", "triage"],
    });
  });

  it("Recently run is one list, newest run first, never-run last by name", () => {
    const lastRuns = new Map([ran("builtin://planner", 5), ran("triage", 60)]);
    const view = libraryView({ workflows, invalid, triggers: [], lastRuns, sort: "recent" });
    expect(names(view)).toEqual({
      all: ["builtin://planner", "triage", "builtin://agent", "release-notes"],
    });
    expect(view.sections[0]!.label).toBe("All workflows");
    // With a source, the one list is that source, and says so.
    const builtinOnly = libraryView({ workflows, invalid, triggers: [], lastRuns, sort: "recent", source: "builtin" });
    expect(builtinOnly.sections[0]!.label).toBe("Built-in");
    expect(names(builtinOnly)).toEqual({ all: ["builtin://planner", "builtin://agent"] });
  });

  it("pins workflows with a FAILING automation under Needs attention, out of their section", () => {
    const triggers = [
      trigger("t1", "triage", "failing"),
      trigger("t2", "triage", "healthy"),
      trigger("t3", "builtin://agent", "degraded"),
      // Paused wins over failing: a paused automation is not firing.
      trigger("t4", "release-notes", "failing", false),
    ];
    const view = libraryView({ workflows, invalid, triggers });
    expect(names(view)).toEqual({
      attention: ["triage"],
      yours: ["release-notes"],
      builtin: ["builtin://agent", "builtin://planner"],
    });
    expect(view.sections[0]!.label).toBe("Needs attention");
    expect(view.automationCounts.get("triage")).toBe(2);
    expect(view.failingCounts.get("triage")).toBe(1);
    expect(view.failingCounts.get("release-notes")).toBeUndefined();
    // A builtin's automations match on its stored ref.
    expect(view.automationCounts.get("agent")).toBe(1);
  });

  it("Needs attention respects the search and the source", () => {
    const triggers = [trigger("t1", "triage", "failing")];
    expect(libraryView({ workflows, invalid, triggers, source: "builtin" }).sections.map((s) => s.key)).toEqual([
      "builtin",
    ]);
    expect(names(libraryView({ workflows, invalid, triggers, q: "release" }))).toEqual({ yours: ["release-notes"] });
  });

  it("an empty Your workflows stays (it offers create-your-own) unless a search emptied it", () => {
    const builtinsOnly = [wf("builtin://agent", "builtin")];
    expect(libraryView({ workflows: builtinsOnly, invalid: [], triggers: [] }).sections.map((s) => s.key)).toEqual([
      "yours",
      "builtin",
    ]);
    expect(
      libraryView({ workflows: builtinsOnly, invalid: [], triggers: [], q: "agent" }).sections.map((s) => s.key),
    ).toEqual(["builtin"]);
  });

  it("flattens the sections into one table's rows, attention first and flagged", () => {
    const triggers = [trigger("t1", "triage", "failing")];
    const view = libraryView({ workflows, invalid, triggers });
    expect(view.rows.map((row) => [row.workflow.name, row.attention])).toEqual([
      ["triage", true],
      ["release-notes", false],
      ["builtin://agent", false],
      ["builtin://planner", false],
    ]);
    // Recently run keeps its single order.
    const lastRuns = new Map([ran("builtin://planner", 5)]);
    expect(libraryView({ workflows, invalid, triggers: [], lastRuns, sort: "recent" }).rows[0]!.workflow.name).toBe(
      "builtin://planner",
    );
  });

  it("offers create-your-own only when nothing of yours exists and nothing narrowed it", () => {
    const builtinsOnly = [wf("builtin://agent", "builtin")];
    expect(libraryView({ workflows: builtinsOnly, invalid: [], triggers: [] }).noWorkflowsOfYourOwn).toBe(true);
    expect(libraryView({ workflows: builtinsOnly, invalid: [], triggers: [], q: "agent" }).noWorkflowsOfYourOwn).toBe(false);
    expect(libraryView({ workflows, invalid: [], triggers: [] }).noWorkflowsOfYourOwn).toBe(false);
  });

  it("says when a search or source leaves nothing", () => {
    expect(libraryView({ workflows, invalid, triggers: [], q: "zzz" }).noMatches).toBe(true);
    expect(libraryView({ workflows, invalid: [], triggers: [], source: "failed" }).noMatches).toBe(true);
    expect(libraryView({ workflows: [], invalid: [], triggers: [], q: "zzz" }).noMatches).toBe(false);
  });
});
