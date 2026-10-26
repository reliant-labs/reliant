// Copyright (c) 2025 Reliant Labs

/**
 * The unit a person means by "an automation": a workflow's triggers in one
 * project, or one hand-made trigger.
 *
 *   - A WORKFLOW AUTOMATION is one workflow turned on in one project. The
 *     server stores one activation row per declared trigger, keyed by (user,
 *     project, workflow, declared trigger), so a (workflow, project) pair holds
 *     at most one row per trigger: grouping them is exact, and the unit needs
 *     no schema of its own. EnableWorkflow edits it as one (it creates the
 *     missing rows and brings the existing ones to the same settings).
 *   - A CUSTOM AUTOMATION is one hand-made trigger row (no workflow_trigger),
 *     and is its own unit.
 *
 * Turning PR review on used to add three rows to Automations, one per trigger;
 * this is what folds them back into the one thing the user turned on.
 */

import type { Trigger } from "@/hooks/trigger-model";
import { normalizeWorkflowRef } from "../components/workflow/useWorkflowInputs";
import { automationHealth, worstHealth, type AutomationHealthDisplay } from "./automationHealth";

export interface WorkflowAutomation {
  kind: "workflow";
  /** Stable across renders: normalized workflow ref + project. */
  key: string;
  /** The ref as stored on the activations (`builtin://x` or a slug). */
  workflowRef: string;
  projectId: string;
  projectName?: string;
  /** Its activations, by declared trigger name. */
  triggers: Trigger[];
  /** The worst trigger's health, labelled "· 1 of 3 triggers" when they disagree. */
  health: AutomationHealthDisplay;
  /** Every trigger is on. */
  enabled: boolean;
  /** Some triggers are on and some paused. */
  partlyEnabled: boolean;
}

export interface CustomAutomation {
  kind: "custom";
  key: string;
  trigger: Trigger;
  health: AutomationHealthDisplay;
  enabled: boolean;
}

export type AutomationUnit = WorkflowAutomation | CustomAutomation;

export function workflowAutomationKey(workflowRef: string, projectId: string): string {
  return `${normalizeWorkflowRef(workflowRef).trim().toLowerCase()}::${projectId}`;
}

/** The health of one trigger row, with the facts the vocabulary needs. */
export function triggerHealth(trigger: Trigger): AutomationHealthDisplay {
  return automationHealth(trigger);
}

/** The worst of the triggers, saying how many share it when they disagree. */
export function aggregateHealth(triggers: readonly Trigger[]): AutomationHealthDisplay {
  const healths = triggers.map(triggerHealth);
  const worst = worstHealth(healths)!;
  if (triggers.length < 2) return worst;
  const sharing = healths.filter((h) => h.key === worst.key).length;
  if (sharing === healths.length) return worst;
  return { ...worst, label: `${worst.label} · ${sharing} of ${healths.length} triggers` };
}

export function groupAutomationUnits(triggers: readonly Trigger[]): {
  workflowAutomations: WorkflowAutomation[];
  custom: CustomAutomation[];
} {
  const byKey = new Map<string, Trigger[]>();
  const custom: CustomAutomation[] = [];
  for (const trigger of triggers) {
    if (!trigger.workflowTrigger) {
      custom.push({ kind: "custom", key: `custom:${trigger.id}`, trigger, health: triggerHealth(trigger), enabled: trigger.enabled });
      continue;
    }
    const key = workflowAutomationKey(trigger.workflow, trigger.projectId);
    byKey.set(key, [...(byKey.get(key) ?? []), trigger]);
  }
  const workflowAutomations = [...byKey.entries()].map(([key, rows]): WorkflowAutomation => {
    const sorted = [...rows].sort((a, b) => (a.workflowTrigger ?? "").localeCompare(b.workflowTrigger ?? ""));
    const on = sorted.filter((t) => t.enabled).length;
    return {
      kind: "workflow",
      key,
      workflowRef: sorted[0].workflow,
      projectId: sorted[0].projectId,
      projectName: sorted.find((t) => t.projectName)?.projectName,
      triggers: sorted,
      health: aggregateHealth(sorted),
      enabled: on === sorted.length,
      partlyEnabled: on > 0 && on < sorted.length,
    };
  });
  return { workflowAutomations, custom };
}

/** Every unit, worst health first, then by name. */
export function automationUnits(triggers: readonly Trigger[]): AutomationUnit[] {
  const { workflowAutomations, custom } = groupAutomationUnits(triggers);
  return [...workflowAutomations, ...custom].sort(compareUnits);
}

export function unitName(unit: AutomationUnit, workflowTitle: (ref: string) => string): string {
  return unit.kind === "workflow" ? workflowTitle(unit.workflowRef) : unit.trigger.name;
}

function unitSortName(unit: AutomationUnit): string {
  return unit.kind === "workflow" ? normalizeWorkflowRef(unit.workflowRef) : unit.trigger.name;
}

export function compareUnits(a: AutomationUnit, b: AutomationUnit): number {
  return a.health.severity - b.health.severity || unitSortName(a).localeCompare(unitSortName(b)) || a.key.localeCompare(b.key);
}

/** The units that need the user: what "Needs you" lists and the nav badge counts. */
export function unitsNeedingYou(units: readonly AutomationUnit[]): AutomationUnit[] {
  return units.filter((unit) => unit.health.needsAttention);
}

/** The triggers of one workflow automation, for the workflow page's "Turned on" card. */
export function workflowAutomationsFor(triggers: readonly Trigger[], workflowRef: string): WorkflowAutomation[] {
  const target = normalizeWorkflowRef(workflowRef).trim().toLowerCase();
  return groupAutomationUnits(triggers.filter((t) => t.workflowTrigger && normalizeWorkflowRef(t.workflow).trim().toLowerCase() === target))
    .workflowAutomations.sort((a, b) => a.health.severity - b.health.severity || (a.projectName ?? "").localeCompare(b.projectName ?? ""));
}

/** The soonest next fire across a unit's triggers, for the Next column. */
export function unitNextFireAt(unit: AutomationUnit): string | undefined {
  const rows = unit.kind === "workflow" ? unit.triggers : [unit.trigger];
  let best: string | undefined;
  for (const t of rows) {
    if (!t.enabled || !t.nextFireAt) continue;
    if (!best || Date.parse(t.nextFireAt) < Date.parse(best)) best = t.nextFireAt;
  }
  return best;
}

/** The newest firing across a unit's triggers, for the Last run column. */
export function unitLastEvent(unit: AutomationUnit): Trigger["lastEvent"] {
  const rows = unit.kind === "workflow" ? unit.triggers : [unit.trigger];
  let best: Trigger["lastEvent"];
  for (const t of rows) {
    if (!t.lastEvent) continue;
    if (!best || Date.parse(t.lastEvent.occurredAt) > Date.parse(best.occurredAt)) best = t.lastEvent;
  }
  return best;
}

/** The counts strip over what is on: how many need you, are on and healthy, are off, and when the next run is. */
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
    needYou: unitsNeedingYou(units).length,
    healthyOn: units.filter((u) => !u.health.needsAttention && u.health.key !== "paused").length,
    paused: units.filter((u) => u.health.key === "paused").length,
    nextRunAt,
  };
}
