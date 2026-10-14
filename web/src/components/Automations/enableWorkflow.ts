// Copyright (c) 2025 Reliant Labs

/**
 * The logic behind "Enable" on a workflow's page, kept pure so what the
 * dialog asks for and says is pinned by tests.
 *
 * A workflow declares WHEN it runs; the user enables it by choosing, once,
 * where the runs happen and as whom. This file decides:
 *
 *   - which questions that takes (`planEnable`): the integrations whose
 *     connected account is needed, and which triggers are webhooks;
 *   - how a trigger-by-trigger outcome reads (`summarizeEnable`), including
 *     the one that matters most: enabling is all or nothing, so a failure of
 *     one trigger is also why the others did not start, and a trigger that
 *     could not be switched back off is the one thing the user must act on.
 */

import type { EnableWorkflowResult, WorkflowTriggerOutcomeKind, WorkflowTriggerResult } from "@/api/trigger-grpc";
import { integrationOf, sourceCase, type DeclaredTrigger } from "@/lib/declaredTriggers";

export interface EnablePlan {
  /** Integrations the workflow's triggers listen through, in first-use order. */
  integrations: string[];
  /** The declared triggers that use each integration, for "used by …". */
  triggersByIntegration: Record<string, string[]>;
  /** Triggers that fire from a webhook: their URL and token are shown after enabling. */
  webhookTriggers: string[];
}

export function planEnable(declared: readonly DeclaredTrigger[]): EnablePlan {
  const integrations: string[] = [];
  const triggersByIntegration: Record<string, string[]> = {};
  const webhookTriggers: string[] = [];
  for (const trigger of declared) {
    const name = trigger.name ?? "";
    const integration = integrationOf(trigger)?.integration;
    if (integration) {
      if (!triggersByIntegration[integration]) {
        triggersByIntegration[integration] = [];
        integrations.push(integration);
      }
      triggersByIntegration[integration].push(name);
    }
    if (sourceCase(trigger) === "webhook") webhookTriggers.push(name);
  }
  return { integrations, triggersByIntegration, webhookTriggers };
}

/** Declared triggers with no prompt of their own: the user must write one for them. */
export function triggersNeedingPrompt(declared: readonly DeclaredTrigger[]): string[] {
  return declared.filter((t) => !((t as { prompt?: string }).prompt ?? "").trim()).map((t) => t.name ?? "");
}

const OUTCOME_TEXT: Record<WorkflowTriggerOutcomeKind, string> = {
  enabled: "Enabled",
  resumed: "Turned back on",
  alreadyEnabled: "Already on",
  failed: "Could not enable",
  notEnabled: "Not enabled",
  leftEnabled: "Still running",
};

export function outcomeLabel(kind: WorkflowTriggerOutcomeKind): string {
  return OUTCOME_TEXT[kind];
}

export type EnableVerdict =
  /** Every trigger is running. */
  | { kind: "enabled"; webhooks: WorkflowTriggerResult[] }
  /** Nothing new is running; `failed` says why, `held` are the ones that were fine. */
  | { kind: "refused"; failed: WorkflowTriggerResult[]; held: WorkflowTriggerResult[] }
  /** The attempt failed and could not be fully undone: something is running that the user did not ask for. */
  | { kind: "partial"; running: WorkflowTriggerResult[]; failed: WorkflowTriggerResult[]; held: WorkflowTriggerResult[] };

export function summarizeEnable(result: EnableWorkflowResult): EnableVerdict {
  if (result.enabled) {
    return { kind: "enabled", webhooks: result.results.filter((r) => r.webhook) };
  }
  const failed = result.results.filter((r) => r.outcome === "failed");
  const running = result.results.filter((r) => r.outcome === "leftEnabled");
  const held = result.results.filter((r) => r.outcome === "notEnabled");
  if (running.length > 0) return { kind: "partial", running, failed, held };
  return { kind: "refused", failed, held };
}

/** The headline for a refusal: names what to fix instead of saying "failed". */
export function refusalHeadline(failed: readonly WorkflowTriggerResult[]): string {
  if (failed.length === 0) return "Nothing was turned on.";
  if (failed.length === 1) return `Nothing was turned on, because “${failed[0].trigger}” could not be.`;
  return `Nothing was turned on, because ${failed.length} triggers could not be.`;
}

export type WorkflowEnableState =
  /** No activation yet: offer Enable. */
  | "off"
  /** Every declared trigger has a running activation. */
  | "on"
  /** Some run and some do not (paused, or one added to the workflow since). */
  | "partial"
  /** Activations exist but none run. */
  | "paused";

/**
 * Where a workflow stands, from its declared trigger names and the user's
 * activations of them: what the page's Enable/Disable button should offer.
 */
export function workflowEnableState(
  declaredNames: readonly string[],
  activations: ReadonlyArray<{ workflowTrigger?: string; enabled: boolean }>,
): WorkflowEnableState {
  const mine = activations.filter((a) => a.workflowTrigger);
  if (mine.length === 0) return "off";
  const running = declaredNames.filter((name) => mine.some((a) => a.workflowTrigger === name && a.enabled));
  if (running.length === 0) return "paused";
  return running.length === declaredNames.length ? "on" : "partial";
}
