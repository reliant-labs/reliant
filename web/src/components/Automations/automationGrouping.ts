// Copyright (c) 2025 Reliant Labs

/**
 * How the Automations list is grouped and ordered (research/WORKFLOW_UI.md
 * §7.2), kept pure so the order a user sees is pinned by tests.
 *
 *   - Group by Workflow (the default) or by Project.
 *   - A "Needs attention" pseudo-group is pinned on top, holding every
 *     Failing / Waiting for machine / Skipping / Degraded row across groups.
 *     It exists only when non-empty. A row that needs attention is listed
 *     there INSTEAD of in its group, never in both: two switches for one
 *     automation on one screen is a trap. A group left empty by that is
 *     dropped.
 *   - Within a group: worst health first, then soonest next fire, then name.
 *   - Groups are ordered by their label.
 */

import type { Trigger } from "@/api/trigger-grpc";
import { automationHealth, type AutomationHealthDisplay } from "@/lib/automationHealth";
import { getWorkflowDisplayName } from "../workflow/useWorkflowInputs";

export type AutomationGroupBy = "workflow" | "project";

export interface AutomationListEntry {
  trigger: Trigger;
  health: AutomationHealthDisplay;
}

export interface AutomationGroup {
  key: string;
  label: string;
  entries: AutomationListEntry[];
}

export interface GroupedAutomations {
  /** Absent when nothing needs attention. */
  attention?: AutomationGroup;
  groups: AutomationGroup[];
}

export const NEEDS_ATTENTION_KEY = "needs-attention";

/** An empty workflow means the owner's default, resolved at fire time. */
export function workflowGroupLabel(workflow: string): string {
  return workflow ? getWorkflowDisplayName(workflow, true) : "Default workflow";
}

function groupKeyAndLabel(trigger: Trigger, by: AutomationGroupBy): [string, string] {
  if (by === "project") {
    return [`project:${trigger.projectId}`, trigger.projectName ?? "Deleted project"];
  }
  return [`workflow:${trigger.workflow}`, workflowGroupLabel(trigger.workflow)];
}

function nextFireMs(trigger: Trigger): number {
  if (!trigger.enabled || !trigger.nextFireAt) return Number.POSITIVE_INFINITY;
  const time = Date.parse(trigger.nextFireAt);
  return Number.isNaN(time) ? Number.POSITIVE_INFINITY : time;
}

export function compareEntries(a: AutomationListEntry, b: AutomationListEntry): number {
  return (
    a.health.severity - b.health.severity ||
    nextFireMs(a.trigger) - nextFireMs(b.trigger) ||
    a.trigger.name.localeCompare(b.trigger.name) ||
    a.trigger.id.localeCompare(b.trigger.id)
  );
}

export function groupAutomations(triggers: Trigger[], by: AutomationGroupBy): GroupedAutomations {
  const attention: AutomationListEntry[] = [];
  const groups = new Map<string, AutomationGroup>();

  for (const trigger of triggers) {
    const entry = { trigger, health: automationHealth(trigger) };
    if (entry.health.needsAttention) {
      attention.push(entry);
      continue;
    }
    const [key, label] = groupKeyAndLabel(trigger, by);
    const group = groups.get(key) ?? { key, label, entries: [] };
    group.entries.push(entry);
    groups.set(key, group);
  }

  const sorted = [...groups.values()]
    .map((group) => ({ ...group, entries: [...group.entries].sort(compareEntries) }))
    .sort((a, b) => a.label.localeCompare(b.label) || a.key.localeCompare(b.key));

  return {
    attention:
      attention.length > 0
        ? { key: NEEDS_ATTENTION_KEY, label: "Needs attention", entries: attention.sort(compareEntries) }
        : undefined,
    groups: sorted,
  };
}

/** "3 automations · 2 on". */
export function groupSummary(entries: AutomationListEntry[]): string {
  const on = entries.filter((entry) => entry.trigger.enabled).length;
  const noun = entries.length === 1 ? "automation" : "automations";
  return `${entries.length} ${noun} · ${on} on`;
}
