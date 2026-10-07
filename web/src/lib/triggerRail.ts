// Copyright (c) 2025 Reliant Labs

/**
 * What the builder's trigger rail lists. Two kinds of line:
 *
 *   - DECLARED triggers: the workflow's own `triggers:` (its WHEN, saved in
 *     the definition; research/INTEGRATIONS_V1_BRIEF.md §3a), each with the
 *     caller's activations of it — rows whose `workflow_trigger` names it.
 *     A declaration with no activation is inert: "Activate".
 *   - AD HOC automations (research/WORKFLOW_UI.md §3.2): the caller's trigger
 *     rows in this project that run this workflow with an inline source.
 *     A projection of ListTriggers; nothing about them is in the definition.
 */

import type { Trigger } from "../api/trigger-grpc";
import { normalizeWorkflowRef } from "../components/workflow/useWorkflowInputs";
import { automationHealth, type AutomationHealthDisplay } from "./automationHealth";
import { describeTriggerSource } from "./cronText";
import { RAW_EVENT_NAMING, type IntegrationEventNaming } from "./integrationEventNames";
import type { DeclaredTrigger } from "./declaredTriggers";

export interface TriggerRailLine {
  trigger: Trigger;
  /** The schedule in words, e.g. "Weekdays at 09:00". */
  scheduleText: string;
  health: AutomationHealthDisplay;
  /** The trigger is disabled: drawn dimmed. */
  paused: boolean;
  /** The health is FAILING: drawn with a red dot. */
  failing: boolean;
}

/**
 * Whether a trigger runs this workflow. Refs are compared without their
 * scheme: the automation form stores what the workflow list returned
 * (`builtin://agent` for a builtin, the slug for a user or project workflow),
 * while the builder knows the definition's name. An empty trigger workflow
 * means "the owner's default, resolved at fire time", which names no workflow
 * here, so it never matches.
 */
export function triggerRunsWorkflow(trigger: Pick<Trigger, "workflow">, workflowRef: string): boolean {
  const target = normalizeWorkflowRef(workflowRef).trim().toLowerCase();
  const ref = normalizeWorkflowRef(trigger.workflow).trim().toLowerCase();
  return target !== "" && ref === target;
}

export function triggerRailLines(
  triggers: readonly Trigger[],
  workflowRef: string,
  projectId: string,
  naming: IntegrationEventNaming = RAW_EVENT_NAMING,
): TriggerRailLine[] {
  return triggers
    .filter((trigger) => !trigger.workflowTrigger)
    .filter((trigger) => trigger.projectId === projectId && triggerRunsWorkflow(trigger, workflowRef))
    .map((trigger) => {
      const health = automationHealth(trigger);
      return {
        trigger,
        scheduleText: describeTriggerSource(trigger.source, naming),
        health,
        paused: !trigger.enabled,
        failing: health.key === "failing",
      };
    })
    .sort((a, b) => a.trigger.name.localeCompare(b.trigger.name));
}

/**
 * The ref "+ Add trigger" prefills. A builtin is listed as `builtin://<name>`
 * by ListWorkflows, so the prefill matches an option in the form's picker.
 */
export function workflowRefForTrigger(
  workflowName: string,
  source: "builtin" | "user" | "project",
): string {
  const name = normalizeWorkflowRef(workflowName);
  return source === "builtin" ? `builtin://${name}` : name;
}

/** A workflow's declared trigger with the caller's activations of it. */
export interface DeclaredRailLine {
  declared: DeclaredTrigger;
  index: number;
  /** The caller's rows activating it, in any project. */
  activations: Trigger[];
  /** Rolled up across activations for the line's own dot: broken > failing > active > paused > none. */
  state: "inactive" | "active" | "paused" | "failing" | "broken";
  /** The worst activation's health, for the line's tooltip. */
  health?: AutomationHealthDisplay;
}

/**
 * Declared triggers in definition order, each with its activations. An
 * activation matches by workflow ref AND declared name. An activation naming
 * a declaration this workflow no longer has is a BROKEN orphan; those are
 * returned separately so the rail can still show — and fix — them.
 */
export function declaredRailLines(
  declared: readonly DeclaredTrigger[],
  triggers: readonly Trigger[],
  workflowRef: string,
): { lines: DeclaredRailLine[]; orphans: Trigger[] } {
  const mine = triggers.filter((t) => t.workflowTrigger && triggerRunsWorkflow(t, workflowRef));
  const names = new Set(declared.map((d) => d.name));
  const lines = declared.map((d, index): DeclaredRailLine => {
    const activations = mine.filter((t) => t.workflowTrigger === d.name);
    const healths = activations.map((t) => automationHealth(t)).sort((a, b) => a.severity - b.severity);
    const worst = healths[0];
    let state: DeclaredRailLine["state"] = "inactive";
    if (worst) {
      if (worst.key === "broken") state = "broken";
      else if (worst.key === "failing") state = "failing";
      else if (activations.some((t) => t.enabled)) state = "active";
      else state = "paused";
    }
    return { declared: d, index, activations, state, health: worst };
  });
  const orphans = mine.filter((t) => !names.has(t.workflowTrigger!));
  return { lines, orphans };
}

/** A workflow's declared trigger that the caller has not activated anywhere. */
export interface InactiveDeclaredTrigger {
  /** The workflow ref an activation names (`builtin://x` for a builtin). */
  workflowRef: string;
  workflowTitle: string;
  declared: DeclaredTrigger;
}

/**
 * Declared-but-inactive triggers across `workflows` (research/INTEGRATIONS_V1_BRIEF.md
 * §3a): declarations with no activation of the caller's, in any project.
 * These are what the workflow's author meant to run, and nothing does yet.
 */
export function inactiveDeclaredTriggers(
  workflows: ReadonlyArray<{ name: string; title?: string; triggers?: readonly DeclaredTrigger[] }>,
  triggers: readonly Trigger[],
): InactiveDeclaredTrigger[] {
  const seen = new Set<string>();
  const out: InactiveDeclaredTrigger[] = [];
  for (const workflow of workflows) {
    const key = normalizeWorkflowRef(workflow.name).trim().toLowerCase();
    if (seen.has(key) || !workflow.triggers?.length) continue;
    seen.add(key);
    for (const line of declaredRailLines(workflow.triggers, triggers, workflow.name).lines) {
      if (line.state !== "inactive") continue;
      out.push({ workflowRef: workflow.name, workflowTitle: workflow.title || normalizeWorkflowRef(workflow.name), declared: line.declared });
    }
  }
  return out;
}
