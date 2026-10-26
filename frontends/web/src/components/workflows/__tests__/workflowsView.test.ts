// Copyright (c) 2025 Reliant Labs

/**
 * What the Workflows list shows for a scope, search, filter and sort
 * (WORKFLOW_UI.md §2.2): one row per workflow with what runs it by itself
 * folded in (state, worst health, next run), search over name and
 * description, the filter chips, "Recently run" as one list, "Needs you"
 * pinned on top, a project's own workflow per project across every project,
 * and a row for triggers whose workflow is listed nowhere.
 */

import { describe, expect, it } from "vitest";

import type { RunSummary } from "@/hooks/run-queries";
import type { Trigger, TriggerHealthStatusKey } from "@/hooks/trigger-model";
import type { InvalidWorkflow, WorkflowResponse } from "@/hooks/workflow-queries";
import { automationsDeletedWith, deletedAutomationsSentence, runsByItself, workflowsView, type ListedWorkflow } from "../list/workflowsView";

function wf(name: string, source: WorkflowResponse["source"], description = ""): WorkflowResponse {
  return { name, filename: name, source, description, stepCount: 1, status: "complete", validationErrors: [], nodes: [], edges: [] };
}

function trigger(id: string, workflow: string, status: TriggerHealthStatusKey = "healthy", enabled = true, projectId = "p"): Trigger {
  return {
    id,
    name: id,
    projectId,
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

const listed = (workflows: WorkflowResponse[]): ListedWorkflow[] => workflows.map((workflow) => ({ workflow }));
const workflows = listed([
  wf("builtin://agent", "builtin", "General-purpose coding agent"),
  wf("builtin://planner", "builtin", "Plans before it codes"),
  wf("triage", "project", "Triage new GitHub issues"),
  wf("release-notes", "user", "Draft the weekly release notes"),
]);
const invalid: InvalidWorkflow[] = [
  { name: "broken", source: "project", path: ".reliant/workflows/broken.yaml", errors: ["bad indent"] },
];
const names = (view: ReturnType<typeof workflowsView>) =>
  Object.fromEntries(view.sections.map((section) => [section.key, section.rows.map((row) => row.workflow.name)]));

describe("runsByItself (a row's On/Off)", () => {
  it("is none when nothing runs it and it declares nothing; Off when it declares triggers that are not set up", () => {
    expect(runsByItself([], false)).toBe("none");
    expect(runsByItself([], true)).toBe("off");
  });
  it("is On when every trigger is, partly when some are, Off when none are", () => {
    expect(runsByItself([trigger("a", "x"), trigger("b", "x")], false)).toBe("on");
    expect(runsByItself([trigger("a", "x"), trigger("b", "x", "healthy", false)], false)).toBe("partly");
    expect(runsByItself([trigger("a", "x", "healthy", false)], true)).toBe("off");
  });
});

describe("workflowsView", () => {
  it("lists hidden built-ins as building blocks, but not an automation's internal steps", () => {
    const view = workflowsView({
      workflows: [
        ...workflows,
        ...listed([
          { ...wf("builtin://structured-agent", "builtin"), isHidden: true },
          // Autopilot's helpers: hidden AND automation_only. Nobody can run
          // them, so they are listed nowhere.
          { ...wf("builtin://autopilot-lease-acquire", "builtin"), isHidden: true, automationOnly: true },
          { ...wf("builtin://autopilot-agent", "builtin"), isHidden: true, automationOnly: true },
        ]),
      ],
      invalid,
      triggers: [],
    });
    expect(view.buildingBlocks.map((row) => row.workflow.name)).toEqual(["builtin://structured-agent"]);
    expect(view.rows.map((row) => row.workflow.name)).not.toContain("builtin://autopilot-lease-acquire");
  });

  it("by default keeps the origin sections, sorted by name, and the broken definitions", () => {
    const view = workflowsView({ workflows, invalid, triggers: [] });
    expect(names(view)).toEqual({
      yours: ["release-notes", "triage"],
      builtin: ["builtin://agent", "builtin://planner"],
    });
    expect(view.invalid).toHaveLength(1);
    expect(view.noMatches).toBe(false);
  });

  it("folds every trigger that runs a workflow into its row: state, worst health, soonest next run", () => {
    const soon = new Date(Date.now() + 60_000).toISOString();
    const later = new Date(Date.now() + 3_600_000).toISOString();
    const view = workflowsView({
      workflows,
      invalid,
      triggers: [
        { ...trigger("t1", "triage"), nextFireAt: later },
        { ...trigger("t2", "triage", "degraded"), nextFireAt: soon, workflowTrigger: "nightly" },
        // A builtin's triggers match on its stored ref.
        trigger("t3", "agent"),
      ],
    });
    const triage = view.rows.find((row) => row.workflow.name === "triage")!;
    expect(triage.triggers.map((t) => t.id)).toEqual(["t1", "t2"]);
    expect(triage.state).toBe("on");
    expect(triage.health?.key).toBe("degraded");
    expect(triage.nextFireAt).toBe(soon);
    expect(triage.projectsOn).toBe(1);
    expect(view.rows.find((row) => row.workflow.name === "builtin://agent")!.triggers).toHaveLength(1);
    expect(view.rows.find((row) => row.workflow.name === "release-notes")!.state).toBe("none");
  });

  it("search matches the display name and the description, case-insensitively", () => {
    expect(names(workflowsView({ workflows, invalid, triggers: [], q: "GITHUB" }))).toEqual({ yours: ["triage"] });
    expect(names(workflowsView({ workflows, invalid, triggers: [], q: "release notes" }))).toEqual({
      yours: ["release-notes"],
    });
    // A broken definition matches on its file path; it has no description.
    expect(workflowsView({ workflows, invalid, triggers: [], q: "broken.yaml" }).invalid).toHaveLength(1);
    expect(workflowsView({ workflows, invalid, triggers: [], q: "agent" }).invalid).toHaveLength(0);
  });

  it("the source chips narrow to one section", () => {
    expect(names(workflowsView({ workflows, invalid, triggers: [], show: "builtin" }))).toEqual({
      builtin: ["builtin://agent", "builtin://planner"],
    });
    expect(workflowsView({ workflows, invalid, triggers: [], show: "builtin" }).invalid).toEqual([]);
    const failed = workflowsView({ workflows, invalid, triggers: [], show: "failed" });
    expect(failed.sections).toEqual([]);
    expect(failed.invalid).toHaveLength(1);
    expect(names(workflowsView({ workflows, invalid, triggers: [], show: "yours" }))).toEqual({
      yours: ["release-notes", "triage"],
    });
  });

  it("On lists what runs by itself, partly on included; Needs you, what needs the user", () => {
    const triggers = [
      trigger("t1", "triage"),
      trigger("t2", "release-notes", "healthy", false),
      trigger("t3", "release-notes"),
      trigger("t4", "builtin://planner", "healthy", false),
      trigger("t5", "builtin://agent", "failing"),
    ];
    const on = workflowsView({ workflows, invalid, triggers, show: "on" });
    expect(on.rows.map((row) => row.workflow.name).sort()).toEqual(["builtin://agent", "release-notes", "triage"]);
    expect(on.invalid).toEqual([]);
    const needsYou = workflowsView({ workflows, invalid, triggers, show: "needs-you" });
    expect(names(needsYou)).toEqual({ "needs-you": ["builtin://agent"] });
  });

  it("Recently run is one list, newest run first, never-run last by name", () => {
    const lastRuns = new Map([ran("builtin://planner", 5), ran("triage", 60)]);
    const view = workflowsView({ workflows, invalid, triggers: [], lastRuns, sort: "recent" });
    expect(names(view)).toEqual({
      all: ["builtin://planner", "triage", "builtin://agent", "release-notes"],
    });
    expect(view.sections[0]!.label).toBe("All workflows");
    // With a source, the one list is that source, and says so.
    const builtinOnly = workflowsView({ workflows, invalid, triggers: [], lastRuns, sort: "recent", show: "builtin" });
    expect(builtinOnly.sections[0]!.label).toBe("Built-in");
    expect(names(builtinOnly)).toEqual({ all: ["builtin://planner", "builtin://agent"] });
  });

  it("pins workflows with a trigger that needs the user under Needs you, out of their section", () => {
    const triggers = [
      trigger("t1", "triage", "failing"),
      trigger("t2", "triage", "healthy"),
      trigger("t3", "builtin://agent", "degraded"),
      // Off wins over failing: a trigger that is off is not firing.
      trigger("t4", "release-notes", "failing", false),
    ];
    const view = workflowsView({ workflows, invalid, triggers });
    expect(names(view)).toEqual({
      "needs-you": ["triage"],
      yours: ["release-notes"],
      builtin: ["builtin://agent", "builtin://planner"],
    });
    expect(view.sections[0]!.label).toBe("Needs you");
    expect(view.rows[0]!.health?.key).toBe("failing");
    expect(view.rows.find((row) => row.workflow.name === "release-notes")!.health?.key).toBe("paused");
  });

  it("Needs you respects the search and the filter", () => {
    const triggers = [trigger("t1", "triage", "failing")];
    expect(workflowsView({ workflows, invalid, triggers, show: "builtin" }).sections.map((s) => s.key)).toEqual(["builtin"]);
    expect(names(workflowsView({ workflows, invalid, triggers, q: "release" }))).toEqual({ yours: ["release-notes"] });
  });

  it("an empty Your workflows stays (it offers create-your-own) unless a search emptied it", () => {
    const builtinsOnly = listed([wf("builtin://agent", "builtin")]);
    expect(workflowsView({ workflows: builtinsOnly, invalid: [], triggers: [] }).sections.map((s) => s.key)).toEqual([
      "yours",
      "builtin",
    ]);
    expect(workflowsView({ workflows: builtinsOnly, invalid: [], triggers: [], q: "agent" }).sections.map((s) => s.key)).toEqual([
      "builtin",
    ]);
    expect(workflowsView({ workflows: builtinsOnly, invalid: [], triggers: [] }).noWorkflowsOfYourOwn).toBe(true);
    expect(workflowsView({ workflows: builtinsOnly, invalid: [], triggers: [], q: "agent" }).noWorkflowsOfYourOwn).toBe(false);
  });

  it("across projects, a project's own workflow is one row per project and claims only its project's triggers", () => {
    const view = workflowsView({
      workflows: [
        { workflow: wf("builtin://agent", "builtin") },
        { workflow: wf("triage", "project"), projectId: "p1", projectName: "API" },
        { workflow: wf("triage", "project"), projectId: "p2", projectName: "Web" },
      ],
      invalid: [],
      triggers: [trigger("a", "triage", "healthy", true, "p1"), trigger("b", "triage", "healthy", true, "p2"), trigger("c", "agent", "healthy", true, "p2"), trigger("d", "agent", "healthy", true, "p1")],
    });
    const rows = view.rows.map((row) => [row.workflow.name, row.projectName, row.triggers.map((t) => t.id)]);
    expect(rows).toEqual([
      ["triage", "API", ["a"]],
      ["triage", "Web", ["b"]],
      ["builtin://agent", undefined, ["c", "d"]],
    ]);
    expect(view.rows.find((row) => row.workflow.name === "builtin://agent")!.projectsOn).toBe(2);
    expect(new Set(view.rows.map((row) => row.key)).size).toBe(3);
  });

  it("a trigger whose workflow is listed nowhere gets a row of its own, once everything has loaded", () => {
    const triggers = [trigger("o1", "removed-flow"), trigger("o2", "removed-flow")];
    const view = workflowsView({ workflows, invalid, triggers });
    const orphan = view.rows.find((row) => row.workflow.name === "removed-flow")!;
    expect(orphan.missing).toBe(true);
    expect(orphan.triggers.map((t) => t.id)).toEqual(["o1", "o2"]);
    // Not while a project's list is still loading: it may yet claim them.
    expect(workflowsView({ workflows, invalid, triggers, complete: false }).rows.some((row) => row.missing)).toBe(false);
  });

  it("says when a search or filter leaves nothing", () => {
    expect(workflowsView({ workflows, invalid, triggers: [], q: "zzz" }).noMatches).toBe(true);
    expect(workflowsView({ workflows, invalid: [], triggers: [], show: "failed" }).noMatches).toBe(true);
    expect(workflowsView({ workflows: [], invalid: [], triggers: [], q: "zzz" }).noMatches).toBe(false);
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

  it("says nothing without triggers", () => expect(deletedAutomationsSentence([])).toBe(""));
  it("counts and names them", () => {
    expect(deletedAutomationsSentence(named(1))).toBe("Also deletes its 1 trigger: “A1”.");
    expect(deletedAutomationsSentence(named(2))).toBe("Also deletes its 2 triggers: “A1”, “A2”.");
  });
  it("abbreviates a long list", () => {
    expect(deletedAutomationsSentence(named(5))).toBe("Also deletes its 5 triggers: “A1”, “A2”, “A3”, and 2 more.");
  });
});
