// Copyright (c) 2025 Reliant Labs

/**
 * What the builder's trigger rail lists (research/WORKFLOW_UI.md §3.2): the
 * caller's automations in the current project whose workflow is the one on
 * the canvas. A projection of ListTriggers — nothing here is saved with the
 * workflow.
 */

import type { Trigger } from "../api/trigger-grpc";
import { normalizeWorkflowRef } from "../components/workflow/useWorkflowInputs";
import { automationHealth, type AutomationHealthDisplay } from "./automationHealth";
import { describeSchedule } from "./cronText";

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
): TriggerRailLine[] {
  return triggers
    .filter((trigger) => trigger.projectId === projectId && triggerRunsWorkflow(trigger, workflowRef))
    .map((trigger) => {
      const health = automationHealth(trigger);
      return {
        trigger,
        scheduleText: trigger.schedule ? describeSchedule(trigger.schedule) : "Custom trigger",
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
