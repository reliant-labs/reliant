// Copyright (c) 2025 Reliant Labs

/**
 * The automation dialog's WHEN, and its mapping to and from the wire source.
 *
 * A schedule is edited as the sentence form (scheduleForm.ts) plus a zone; any
 * other kind — webhook, an app event, another workflow's run — is edited as a
 * declaration-shaped trigger (lib/declaredTriggers.ts), the same shape the
 * builder palette builds and TriggerSourceFields edits. So the dialog, the
 * builder and a draft from the agent all describe a WHEN the same way, and
 * there is one catalog of kinds.
 *
 * Pure: no React, no RPCs.
 */

import { inlineTriggerSource, type OverlapPolicy, type TriggerSchedule, type TriggerSource } from "@/hooks/trigger-model";
import {
  defaultSource,
  newDeclaredTrigger,
  sourceCase,
  type DeclaredSource,
  type DeclaredTrigger,
} from "@/lib/declaredTriggers";
import { DEFAULT_SCHEDULE_FORM, formFromSchedule, scheduleFromForm, type ScheduleFormState } from "../scheduleForm";

export type WhenKind = "schedule" | "webhook" | "integration" | "workflowEvent";

export interface WhenState {
  kind: WhenKind;
  /** The schedule sentence (kind schedule). */
  schedule: ScheduleFormState;
  /** IANA zone; empty reads as UTC on the server. */
  timezone: string;
  overlap: OverlapPolicy;
  /** Kept from a stored schedule and sent back; not editable here (YAML/API only). */
  catchupWindow?: string;
  /** The event source (every other kind), declaration-shaped. */
  event?: DeclaredTrigger;
  /** For an integration event: the catalog trigger type, for its events and connections. */
  catalogRef?: string;
}

export function browserTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

/** Every weekday at 09:00 in the browser's zone: the commonest automation there is. */
export function defaultWhen(): WhenState {
  return { kind: "schedule", schedule: DEFAULT_SCHEDULE_FORM, timezone: browserTimezone(), overlap: "skip" };
}

/** A declaration-shaped event trigger for a built-in kind. */
export function eventTriggerFor(kind: "webhook" | "workflow_event"): DeclaredTrigger {
  return newDeclaredTrigger({ name: kind === "webhook" ? "webhook" : "after-workflow", source: defaultSource(kind) });
}

function kindOfCase(c: string | undefined): WhenKind | undefined {
  switch (c) {
    case "schedule":
    case "webhook":
    case "integration":
    case "workflowEvent":
      return c;
    default:
      return undefined;
  }
}

/**
 * The WHEN a stored (or drafted) source opens as. An unknown arm or an
 * activation has no inline WHEN, so it returns undefined.
 */
export function whenFromSource(source: TriggerSource, opts: { filter?: string; catalogRef?: string } = {}): WhenState | undefined {
  if (source.kind === "schedule") {
    const s = source.schedule;
    return {
      kind: "schedule",
      schedule: s.cron.length > 0 || s.interval ? formFromSchedule({ cron: s.cron, interval: s.interval }) : DEFAULT_SCHEDULE_FORM,
      // An empty zone in a draft means "the person's": the browser's.
      timezone: s.timezone || browserTimezone(),
      overlap: s.overlap,
      catchupWindow: s.catchupWindow,
    };
  }
  if (source.kind !== "passthrough") return undefined;
  const kind = kindOfCase(source.arm.case);
  if (!kind || kind === "schedule") return undefined;
  const event = newDeclaredTrigger({ name: kind, source: source.arm as unknown as DeclaredSource });
  return {
    ...defaultWhen(),
    kind,
    event: { ...event, filter: opts.filter ?? "" } as DeclaredTrigger,
    catalogRef: opts.catalogRef,
  };
}

/** The WHEN of a declaration-shaped trigger (a palette pick, a builder declaration). */
export function whenFromDeclared(trigger: DeclaredTrigger, catalogRef?: string): WhenState | undefined {
  const source = inlineTriggerSource(trigger.source);
  if (source.kind === "unknown") return undefined;
  return whenFromSource(source, { filter: trigger.filter, catalogRef });
}

/** The wire source the WHEN compiles to. Call validateScheduleForm first for a schedule. */
export function sourceFromWhen(when: WhenState): TriggerSource {
  if (when.kind === "schedule") {
    const wire = scheduleFromForm(when.schedule);
    const schedule: TriggerSchedule = {
      cron: wire.cron,
      interval: wire.interval,
      timezone: when.timezone.trim() || "UTC",
      overlap: when.overlap,
      catchupWindow: when.catchupWindow || undefined,
    };
    return { kind: "schedule", schedule };
  }
  return inlineTriggerSource(when.event?.source);
}

/** The event's CEL filter, for a kind that has one. */
export function filterOfWhen(when: WhenState): string | undefined {
  if (when.kind === "schedule") return undefined;
  return (when.event?.filter ?? "").trim();
}

/** Whether the WHEN names something that fires: an integration needs its events. */
export function whenError(when: WhenState): string | null {
  if (when.kind === "integration") {
    const value = when.event?.source?.value as { integration?: string; events?: string[] } | undefined;
    if (!value?.integration) return "Choose the app and the event that starts it.";
    if (!(value.events ?? []).some((e) => e.trim())) return "Choose at least one event.";
  }
  return null;
}

/** Switch the WHEN to another kind, keeping the schedule sentence for a later switch back. */
export function withKind(when: WhenState, kind: WhenKind, event?: DeclaredTrigger, catalogRef?: string): WhenState {
  if (kind === "schedule") return { ...when, kind, event: undefined, catalogRef: undefined };
  const next = event ?? (kind === "webhook" ? eventTriggerFor("webhook") : kind === "workflowEvent" ? eventTriggerFor("workflow_event") : undefined);
  return { ...when, kind, event: next, catalogRef: kind === "integration" ? catalogRef : undefined };
}

/** The declared trigger's kind, as the dialog names kinds. */
export function whenKindOf(trigger: DeclaredTrigger): WhenKind | undefined {
  return kindOfCase(sourceCase(trigger));
}
