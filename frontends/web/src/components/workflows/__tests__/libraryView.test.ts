// Copyright (c) 2025 Reliant Labs

/**
 * What the Library shows for a search, source and sort (WORKFLOW_UI.md §2.2):
 * search over name and description, the source filter, "Recently run" as one
 * list, and "Needs attention" for workflows whose automations are FAILING.
 */

import { describe, expect, it } from "vitest";

import type { RunSummary } from "@/hooks/run-queries";
import type { Trigger, TriggerHealthStatusKey } from "@/hooks/trigger-model";
import type { InvalidWorkflow, WorkflowResponse } from "@/hooks/workflow-queries";
import { automationsDeletedWith, deletedAutomationsSentence, libraryView, turnedOnSummary } from "../library/libraryView";
import { automationHealth } from "@/lib/automationHealth";

describe("turnedOnSummary (the Library's Turned on column)", () => {
  const h = (status: "healthy" | "failing" | "needs_setup", enabled = true) =>
    automationHealth({ enabled, health: { status, consecutiveFailures: 2, consecutiveSkips: 0, lastFailureDetail: "" } });
  it("is null when nothing could run it by itself — the cell's dash under a labelled column", () => {
    expect(turnedOnSummary(undefined, false)).toBeNull();
  });
  it("says Off for a workflow that declares triggers and is not on here", () => {
    expect(turnedOnSummary(undefined, true)).toEqual({ text: "Off", attention: false });
  });
  it("says On when all is well, and the worst state in the health vocabulary's words otherwise", () => {
    expect(turnedOnSummary(h("healthy"), true)).toEqual({ text: "On", attention: false });
    expect(turnedOnSummary(h("needs_setup"), true)).toEqual({ text: "Needs setup", attention: true });
    expect(turnedOnSummary(h("failing"), true)).toEqual({ text: "2 failed in a row", attention: true });
    expect(turnedOnSummary(h("healthy", false), true)).toEqual({ text: "Paused", attention: false });
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
    noMachine: false,
    notifyOnComplete: false,
    actingIdentity: "owner",
    health: { status, consecutiveFailures: status === "failing" ? 3 : 0, consecutiveSkips: 0, lastFailureDetail: "" },
    createdAt: "",
    updatedAt: "",
    source: { kind: "unknown" } as Trigger["source"],
    webhookManaged: false,
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
  it("lists hidden built-ins as building blocks, but not an automation's internal steps", () => {
    const view = libraryView({
      workflows: [
        ...workflows,
        { ...wf("builtin://structured-agent", "builtin"), isHidden: true },
        // Autopilot's helpers: hidden AND automation_only. Nobody can run
        // them, so they are listed nowhere.
        { ...wf("builtin://autopilot-lease-acquire", "builtin"), isHidden: true, automationOnly: true },
        { ...wf("builtin://autopilot-agent", "builtin"), isHidden: true, automationOnly: true },
      ],
      invalid,
      triggers: [],
    });
    expect(view.buildingBlocks.map((w) => w.name)).toEqual(["builtin://structured-agent"]);
    expect(view.rows.map((row) => row.workflow.name)).not.toContain("builtin://autopilot-lease-acquire");
  });

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

  it("pins workflows whose automation needs the user under Needs attention, out of their section", () => {
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
    expect(view.worstHealth.get("triage")?.key).toBe("failing");
    expect(view.worstHealth.get("release-notes")?.key).toBe("paused");
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

describe("automationsDeletedWith (what Delete takes along)", () => {
  const trigger = (id: string, workflow: string, projectId = "p1") =>
    ({ id, name: `Auto ${id}`, workflow, projectId }) as unknown as Trigger;

  it("matches a user workflow by the slug every ref spelling resolves to, across projects", () => {
    const triggers = [
      trigger("a", "pr-review"),
      trigger("b", "project://PR Review"),
      trigger("c", "pr_review", "p2"),
      trigger("d", "issue-worker"),
      trigger("e", "builtin://pr-review"),
    ];
    expect(automationsDeletedWith({ name: "pr-review" }, triggers).map((t) => t.id)).toEqual(["a", "b", "c"]);
  });

  it("is empty for a workflow nothing runs", () => {
    expect(automationsDeletedWith({ name: "pr-review" }, [trigger("d", "issue-worker")])).toEqual([]);
  });
});

describe("deletedAutomationsSentence", () => {
  const named = (n: number) => Array.from({ length: n }, (_, i) => ({ name: `A${i + 1}` }) as unknown as Trigger);

  it("says nothing without automations", () => expect(deletedAutomationsSentence([])).toBe(""));
  it("counts and names them", () => {
    expect(deletedAutomationsSentence(named(1))).toBe("Also deletes its 1 automation: “A1”.");
    expect(deletedAutomationsSentence(named(2))).toBe("Also deletes its 2 automations: “A1”, “A2”.");
  });
  it("abbreviates a long list", () => {
    expect(deletedAutomationsSentence(named(5))).toBe("Also deletes its 5 automations: “A1”, “A2”, “A3”, and 2 more.");
  });
});
