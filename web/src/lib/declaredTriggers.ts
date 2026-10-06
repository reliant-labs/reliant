// Copyright (c) 2025 Reliant Labs

/**
 * A workflow's declared `triggers:` — its WHEN (research/INTEGRATIONS_V1_BRIEF.md
 * §3a). Each entry is a reliant.v1.WorkflowTrigger in the proto's own shape,
 * the same message the YAML codec reads and writes, so editing one here and
 * saving produces the canonical YAML the agent tools produce.
 *
 *   triggers:
 *     - name: new-issue
 *       integration: { integration: github, events: [issues.opened], match: { repository: acme/app } }
 *       filter: "trigger.payload.data.issue.user.login != 'dependabot[bot]'"
 *       inputs: { issue_number: "{{ trigger.payload.data.issue.number }}" }
 *
 * Pure functions over that shape. The activation (whose run, which project,
 * machine and connection) is a trigger ROW; see lib/triggerRail.ts.
 */

import type { Workflow } from "../types/workflow";

/** One declared trigger, as the builder holds it (the proto init shape). */
export type DeclaredTrigger = NonNullable<Workflow["triggers"]>[number];
export type DeclaredSource = NonNullable<DeclaredTrigger["source"]>;
export type DeclaredSourceCase = NonNullable<DeclaredSource["case"]>;

export interface DeclaredSchedule {
  cron: string[];
  interval?: string;
  timezone?: string;
}

export interface DeclaredIntegration {
  integration: string;
  events: string[];
  match: Record<string, string>;
  pollInterval?: string;
}

export interface DeclaredWorkflowEvent {
  workflows: string[];
  outcomes: string[];
}

/** The same alphabet the server's triggerNamePattern accepts. */
export const TRIGGER_NAME_PATTERN = /^[a-z0-9]([a-z0-9_-]{0,62}[a-z0-9])?$/;

export function declaredTriggers(workflow: Pick<Workflow, "triggers">): DeclaredTrigger[] {
  return [...(workflow.triggers ?? [])];
}

export function sourceCase(trigger: DeclaredTrigger): DeclaredSourceCase | undefined {
  return trigger.source?.case;
}

function sourceValue<T>(trigger: DeclaredTrigger, kind: DeclaredSourceCase): T | undefined {
  return trigger.source?.case === kind ? (trigger.source.value as T) : undefined;
}

export function scheduleOf(trigger: DeclaredTrigger): DeclaredSchedule | undefined {
  const value = sourceValue<{ cron?: string[]; interval?: string; timezone?: string }>(trigger, "schedule");
  if (!value) return undefined;
  return { cron: [...(value.cron ?? [])], interval: value.interval || undefined, timezone: value.timezone || undefined };
}

export function integrationOf(trigger: DeclaredTrigger): DeclaredIntegration | undefined {
  const value = sourceValue<{ integration?: string; events?: string[]; match?: Record<string, string>; pollInterval?: string }>(trigger, "integration");
  if (!value) return undefined;
  return {
    integration: value.integration ?? "",
    events: [...(value.events ?? [])],
    match: { ...(value.match ?? {}) },
    pollInterval: value.pollInterval || undefined,
  };
}

export function workflowEventOf(trigger: DeclaredTrigger): DeclaredWorkflowEvent | undefined {
  const value = sourceValue<{ workflows?: string[]; outcomes?: string[] }>(trigger, "workflowEvent");
  if (!value) return undefined;
  return { workflows: [...(value.workflows ?? [])], outcomes: [...(value.outcomes ?? [])] };
}

/** Replace a trigger's source arm, keeping every other arm field (the server decides defaults). */
export function withSource(trigger: DeclaredTrigger, source: DeclaredSource): DeclaredTrigger {
  return { ...trigger, source } as DeclaredTrigger;
}

export function withSchedule(trigger: DeclaredTrigger, schedule: DeclaredSchedule): DeclaredTrigger {
  const previous = sourceValue<Record<string, unknown>>(trigger, "schedule") ?? {};
  const value: Record<string, unknown> = { ...previous, cron: schedule.cron.filter((c) => c.trim() !== "") };
  if (schedule.interval) value.interval = schedule.interval;
  else delete value.interval;
  value.timezone = schedule.timezone ?? "";
  return withSource(trigger, { case: "schedule", value } as DeclaredSource);
}

export function withIntegration(trigger: DeclaredTrigger, integration: DeclaredIntegration): DeclaredTrigger {
  const value = {
    integration: integration.integration,
    events: integration.events.filter(Boolean),
    match: Object.fromEntries(Object.entries(integration.match).filter(([k, v]) => k.trim() !== "" && v !== "")),
    pollInterval: integration.pollInterval ?? "",
  };
  return withSource(trigger, { case: "integration", value } as DeclaredSource);
}

export function withWorkflowEvent(trigger: DeclaredTrigger, event: DeclaredWorkflowEvent): DeclaredTrigger {
  return withSource(trigger, { case: "workflowEvent", value: { workflows: event.workflows.filter(Boolean), outcomes: event.outcomes } } as DeclaredSource);
}

export function withFilter(trigger: DeclaredTrigger, filter: string): DeclaredTrigger {
  return { ...trigger, filter } as DeclaredTrigger;
}

/** The declaration's prompt template ("" when activations write their own). */
export function promptOf(trigger: DeclaredTrigger): string {
  return (trigger as { prompt?: string }).prompt ?? "";
}

export function withPrompt(trigger: DeclaredTrigger, prompt: string): DeclaredTrigger {
  return { ...trigger, prompt } as DeclaredTrigger;
}

/** Set (or with "" remove) the mapping for one workflow input. */
export function withInput(trigger: DeclaredTrigger, input: string, template: string): DeclaredTrigger {
  const inputs = { ...(trigger.inputs ?? {}) } as Record<string, string>;
  if (template.trim() === "") delete inputs[input];
  else inputs[input] = template;
  return { ...trigger, inputs } as DeclaredTrigger;
}

/** A slug from free text, as the name must be: "New issue!" → "new-issue". */
export function slugifyTriggerName(text: string): string {
  const slug = text
    .toLowerCase()
    .replace(/[^a-z0-9_-]+/g, "-")
    .replace(/-{2,}/g, "-")
    .replace(/^[-_]+|[-_]+$/g, "")
    .slice(0, 64)
    .replace(/[-_]+$/g, "");
  return slug || "trigger";
}

/** The first `base`, `base-2`, … not already a declared name. */
export function uniqueTriggerName(base: string, triggers: readonly DeclaredTrigger[]): string {
  const taken = new Set(triggers.map((t) => t.name));
  const slug = slugifyTriggerName(base);
  if (!taken.has(slug)) return slug;
  for (let n = 2; ; n += 1) {
    const candidate = `${slug.slice(0, 60)}-${n}`;
    if (!taken.has(candidate)) return candidate;
  }
}

/** Why a name cannot be used, or undefined when it can. */
export function triggerNameError(name: string, triggers: readonly DeclaredTrigger[], index: number): string | undefined {
  if (!name) return "Give the trigger a name; activations refer to it by name.";
  if (!TRIGGER_NAME_PATTERN.test(name)) return "Use lowercase letters, digits, hyphens and underscores, e.g. new-issue.";
  if (triggers.some((t, i) => i !== index && t.name === name)) return "Another trigger already has this name.";
  return undefined;
}

export interface NewDeclaredTriggerSpec {
  name: string;
  description?: string;
  source: DeclaredSource;
}

export function newDeclaredTrigger(spec: NewDeclaredTriggerSpec): DeclaredTrigger {
  return { name: spec.name, description: spec.description ?? "", filter: "", inputs: {}, prompt: "", source: spec.source } as DeclaredTrigger;
}

/** A trigger for a built-in kind picked from the palette, named uniquely among `existing`. */
export function triggerFromBuiltin(kind: "schedule" | "webhook" | "workflow_event", existing: readonly DeclaredTrigger[]): DeclaredTrigger {
  const base = kind === "workflow_event" ? "after-workflow" : kind;
  return newDeclaredTrigger({ name: uniqueTriggerName(base, existing), source: defaultSource(kind) });
}

/**
 * A trigger for a catalog trigger type picked from the palette. Its events
 * are the type's event list (the payload schema's `event` enum).
 */
export function triggerFromCatalog(
  entry: { id: string; summary: string; integration: { id: string } },
  events: string[],
  existing: readonly DeclaredTrigger[],
): DeclaredTrigger {
  return newDeclaredTrigger({
    name: uniqueTriggerName(entry.id.replace(/\./g, "-"), existing),
    description: entry.summary,
    source: { case: "integration", value: { integration: entry.integration.id, events, match: {}, pollInterval: "" } } as DeclaredSource,
  });
}

/** The default source for a built-in kind. */
export function defaultSource(kind: "schedule" | "webhook" | "workflow_event"): DeclaredSource {
  switch (kind) {
    case "schedule":
      return { case: "schedule", value: { cron: ["0 9 * * 1-5"], timezone: browserTimezone() } } as DeclaredSource;
    case "webhook":
      return { case: "webhook", value: {} } as DeclaredSource;
    case "workflow_event":
      return { case: "workflowEvent", value: { workflows: [], outcomes: ["failed"] } } as DeclaredSource;
  }
}

function browserTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

/** A filter can't apply to a schedule, which fires on time rather than on an event. */
export function canFilter(trigger: DeclaredTrigger): boolean {
  return sourceCase(trigger) !== "schedule";
}

/**
 * Validation errors for this trigger out of the server's findings, which name
 * a declared trigger as `<workflow>.triggers[<i>](<name>).<field>: <message>`
 * (internal/workflow/validation/triggers.go). Matched by index AND name, so a
 * finding from before a rename or reorder does not land on the wrong row.
 */
export interface TriggerFinding {
  /** "schedule.cron", "filter", "inputs.issue_number", or "" for the trigger itself. */
  field: string;
  message: string;
  suggestion?: string;
}

const FINDING = /(?:^|\.)triggers\[(\d+)\](?:\(([^)]*)\))?(?:\.([^:\s]+))?:\s*([\s\S]*)$/;

export function findingsForTrigger(
  errors: ReadonlyArray<{ message: string; suggestion?: string }>,
  index: number,
  name: string,
): TriggerFinding[] {
  const out: TriggerFinding[] = [];
  for (const error of errors) {
    const match = FINDING.exec(error.message);
    if (!match) continue;
    const [, at, findingName, field, message] = match;
    if (Number(at) !== index) continue;
    if (findingName !== undefined && findingName !== "" && findingName !== name) continue;
    // The message repeats the suggestion as a "(…)" suffix; it is shown on its own.
    let text = message.trim();
    const suffix = error.suggestion ? ` (${error.suggestion})` : "";
    if (suffix && text.endsWith(suffix)) text = text.slice(0, -suffix.length);
    out.push({ field: field ?? "", message: text, suggestion: error.suggestion || undefined });
  }
  return out;
}

/** A finding's field in words, for a line in the rail: "schedule.cron" → "Schedule cron". */
export function findingFieldLabel(field: string): string {
  if (!field) return "";
  const words = field.replace(/[._]+/g, " ").trim();
  return words.charAt(0).toUpperCase() + words.slice(1);
}

/**
 * A declared source in words, for a rail line: "Weekdays at 09:00",
 * "GitHub: issues.opened", "Webhook", "When deploy fails".
 */
export function describeDeclaredSource(
  trigger: DeclaredTrigger,
  describeSchedule: (schedule: { cron: string[]; interval?: string; timezone: string }) => string,
  integrationName: (id: string) => string = (id) => id,
): string {
  switch (sourceCase(trigger)) {
    case "schedule": {
      const schedule = scheduleOf(trigger)!;
      return describeSchedule({ cron: schedule.cron, interval: schedule.interval, timezone: schedule.timezone || "UTC" });
    }
    case "webhook":
      return "Webhook";
    case "integration": {
      const source = integrationOf(trigger)!;
      const events = source.events.length > 2 ? `${source.events.slice(0, 2).join(", ")} +${source.events.length - 2}` : source.events.join(", ");
      return `${integrationName(source.integration) || "Integration"}${events ? `: ${events}` : ""}`;
    }
    case "workflowEvent": {
      const event = workflowEventOf(trigger)!;
      const outcomes = event.outcomes.length ? event.outcomes.join(" or ") : "finishes, fails or blocks";
      const which = event.workflows.length ? event.workflows.join(", ") : "any workflow";
      return `When ${which} ${outcomes}`;
    }
    default:
      return "No source";
  }
}
