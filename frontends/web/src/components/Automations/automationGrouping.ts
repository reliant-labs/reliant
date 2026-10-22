// Copyright (c) 2025 Reliant Labs

/**
 * How the Automations overview is split and ordered (automation-ux-spec.md
 * §2.1), kept pure so the order a user sees is pinned by tests.
 *
 *   - The unit is an AUTOMATION (lib/automationUnits.ts): one workflow turned
 *     on in one project, or one custom trigger — never one row per trigger.
 *   - "Needs you" holds every unit in an attention state, worst first. A unit
 *     there is listed INSTEAD of in its group, never in both.
 *   - The rest are grouped by Workflow (the default) or by Project; within a
 *     group, worst health first, then soonest next run, then name. Groups are
 *     ordered by their label, and a group left empty is dropped.
 */

import type { Trigger } from "@/api/trigger-grpc";
import { automationUnits, compareUnits, unitNextFireAt, type AutomationUnit } from "@/lib/automationUnits";
import { getWorkflowDisplayName } from "../workflow/useWorkflowInputs";

export type AutomationGroupBy = "workflow" | "project";

export interface AutomationGroup {
  key: string;
  label: string;
  units: AutomationUnit[];
}

export interface AutomationOverview {
  /** Every unit that needs the user, worst first. */
  needsYou: AutomationUnit[];
  /** Everything else, grouped. */
  groups: AutomationGroup[];
  /** Every unit, for the counts. */
  all: AutomationUnit[];
}

/** An empty workflow means the owner's default, resolved at fire time. */
export function workflowGroupLabel(workflow: string): string {
  return workflow ? getWorkflowDisplayName(workflow, true) : "Default workflow";
}

export function unitWorkflow(unit: AutomationUnit): string {
  return unit.kind === "workflow" ? unit.workflowRef : unit.trigger.workflow;
}

export function unitProject(unit: AutomationUnit): { id: string; name: string } {
  const t = unit.kind === "workflow" ? unit.triggers[0]! : unit.trigger;
  return { id: t.projectId, name: t.projectName ?? "Deleted project" };
}

/** What a unit is called on the list: its workflow's title, or a custom trigger's own name. */
export function unitTitle(unit: AutomationUnit): string {
  return unit.kind === "workflow" ? workflowGroupLabel(unit.workflowRef) : unit.trigger.name;
}

function groupKeyAndLabel(unit: AutomationUnit, by: AutomationGroupBy): [string, string] {
  if (by === "project") {
    const project = unitProject(unit);
    return [`project:${project.id}`, project.name];
  }
  const workflow = unitWorkflow(unit);
  return [`workflow:${workflow}`, workflowGroupLabel(workflow)];
}

function compareWithin(a: AutomationUnit, b: AutomationUnit): number {
  const next = (u: AutomationUnit) => {
    const at = unitNextFireAt(u);
    return at ? Date.parse(at) : Number.POSITIVE_INFINITY;
  };
  return a.health.severity - b.health.severity || next(a) - next(b) || compareUnits(a, b);
}

export function automationOverview(triggers: readonly Trigger[], by: AutomationGroupBy): AutomationOverview {
  const all = automationUnits(triggers);
  const needsYou = all.filter((u) => u.health.needsAttention).sort(compareUnits);
  const groups = new Map<string, AutomationGroup>();
  for (const unit of all) {
    if (unit.health.needsAttention) continue;
    const [key, label] = groupKeyAndLabel(unit, by);
    const group = groups.get(key) ?? { key, label, units: [] };
    group.units.push(unit);
    groups.set(key, group);
  }
  return {
    needsYou,
    groups: [...groups.values()]
      .map((group) => ({ ...group, units: [...group.units].sort(compareWithin) }))
      .sort((a, b) => a.label.localeCompare(b.label) || a.key.localeCompare(b.key)),
    all,
  };
}

/** "3 automations · 2 on". */
export function groupSummary(units: readonly AutomationUnit[]): string {
  const on = units.filter((u) => u.enabled || (u.kind === "workflow" && u.partlyEnabled)).length;
  return `${units.length} ${units.length === 1 ? "automation" : "automations"} · ${on} on`;
}

export interface AutomationCounts {
  needYou: number;
  healthyOn: number;
  paused: number;
  /** The soonest scheduled run across everything that is on. */
  nextRunAt?: string;
}

export function automationCounts(units: readonly AutomationUnit[]): AutomationCounts {
  let nextRunAt: string | undefined;
  for (const unit of units) {
    const at = unitNextFireAt(unit);
    if (at && (!nextRunAt || Date.parse(at) < Date.parse(nextRunAt))) nextRunAt = at;
  }
  return {
    needYou: units.filter((u) => u.health.needsAttention).length,
    healthyOn: units.filter((u) => !u.health.needsAttention && u.health.key !== "paused").length,
    paused: units.filter((u) => u.health.key === "paused").length,
    nextRunAt,
  };
}
