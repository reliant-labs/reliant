// Copyright (c) 2025 Reliant Labs

/**
 * Ready-made automations: the built-in workflows that exist to run by
 * themselves (they declare triggers, and a chat cannot start them — the
 * Autopilot pack today), offered above the Workflows table until each is on
 * in scope. Pure, so what the strip offers is pinned by tests.
 *
 * What each needs is read from the definition, not from a list kept beside
 * it: the integrations its declared triggers listen through and its actions
 * call, and whether it needs a machine.
 */

import type { WorkflowResponse } from "@/hooks/workflow-queries";
import { integrationOf, type DeclaredTrigger } from "@/lib/declaredTriggers";
import { integrationDisplayName } from "../../workflow/inputs/inputTypeNouns";
import type { WorkflowListRow } from "./workflowsView";

/** Integrations that are not a connection a person makes (`http/request@1`, Reliant's own). */
const NOT_A_CONNECTION = new Set(["http", "reliant", "core"]);

/** "github/pr.get@1" -> "github". */
function integrationOfUses(uses: string): string | undefined {
  const match = /^([a-z][a-z0-9_-]*)\/[a-z]/.exec(uses.trim());
  return match ? match[1] : undefined;
}

/** A CelString as the list serves it (a literal, or a plain string). */
function literal(value: unknown): string | undefined {
  if (typeof value === "string") return value;
  if (value && typeof value === "object") {
    const inner = (value as { value?: unknown }).value;
    if (typeof inner === "string") return inner;
    if (inner && typeof inner === "object") {
      const v = inner as { case?: string; value?: unknown };
      if (v.case === "literal" && typeof v.value === "string") return v.value;
    }
  }
  return undefined;
}

/** Every integration a workflow's nodes call, at any depth (loops, branches, failure handlers). */
function actionIntegrations(nodes: unknown, out: Set<string>, depth = 0): void {
  if (depth > 12 || nodes == null || typeof nodes !== "object") return;
  if (Array.isArray(nodes)) {
    for (const node of nodes) actionIntegrations(node, out, depth + 1);
    return;
  }
  for (const [key, value] of Object.entries(nodes as Record<string, unknown>)) {
    if (key === "uses") {
      const uses = literal(value);
      const integration = uses ? integrationOfUses(uses) : undefined;
      if (integration) out.add(integration);
      continue;
    }
    if (value && typeof value === "object") actionIntegrations(value, out, depth + 1);
  }
}

/** The integrations a workflow needs a connection to, by display name ("GitHub"). */
export function integrationsNeeded(workflow: Pick<WorkflowResponse, "triggers" | "nodes">): string[] {
  const ids = new Set<string>();
  for (const trigger of (workflow.triggers ?? []) as DeclaredTrigger[]) {
    const integration = integrationOf(trigger)?.integration;
    if (integration) ids.add(integration);
  }
  actionIntegrations(workflow.nodes, ids);
  return [...ids]
    .filter((id) => !NOT_A_CONNECTION.has(id))
    .map(integrationDisplayName)
    .sort((a, b) => a.localeCompare(b));
}

/** A built-in made to run by itself: it declares triggers and is not started from a chat. */
export function isReadyMadeAutomation(workflow: Pick<WorkflowResponse, "source" | "isHidden" | "automationOnly" | "triggers">): boolean {
  return workflow.source === "builtin" && !workflow.isHidden && !!workflow.automationOnly && (workflow.triggers?.length ?? 0) > 0;
}

/** The ready-made automations not yet on in scope, in name order. */
export function readyMadeToOffer(rows: readonly WorkflowListRow[]): WorkflowListRow[] {
  return rows.filter((row) => isReadyMadeAutomation(row.workflow) && row.state !== "on" && row.state !== "partly" && !row.missing);
}
