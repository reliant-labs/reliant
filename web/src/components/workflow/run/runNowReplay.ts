// Copyright (c) 2025 Reliant Labs

/**
 * The replay half of Run now, as pure functions: reading the payload a person
 * edited, and naming a past event in a list.
 *
 * A replay is an event the workflow's declared trigger received, run again by
 * hand. The payload is shown as JSON because that is what the trigger's inputs
 * and prompt read (`trigger.payload.…`), so editing it is editing the event.
 */

import type { JsonObject } from "@bufbuild/protobuf";
import type { ReplayCandidate } from "@/api/trigger-grpc";
import { formatRelativeTime } from "@/lib/relativeTime";

export type PayloadParse = { ok: true; value: JsonObject } | { ok: false; error: string };

/** A payload is a JSON object. Anything else cannot be an event's body. */
export function parsePayload(text: string): PayloadParse {
  const trimmed = text.trim();
  if (!trimmed) return { ok: true, value: {} };
  let parsed: unknown;
  try {
    parsed = JSON.parse(trimmed);
  } catch (error) {
    return { ok: false, error: `Not valid JSON: ${error instanceof Error ? error.message : String(error)}` };
  }
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
    return { ok: false, error: "The payload must be a JSON object, like {\"issue\": 7}." };
  }
  return { ok: true, value: parsed as JsonObject };
}

export function formatPayload(payload: JsonObject): string {
  return Object.keys(payload).length === 0 ? "{}" : JSON.stringify(payload, null, 2);
}

const KIND_WORDS: Record<string, string> = {
  schedule: "Schedule",
  webhook: "Webhook",
  integration: "Integration",
  workflow_event: "Workflow event",
};

/** "new-issue · Integration · octocat · 3 hours ago", for the event picker. */
export function candidateLabel(candidate: ReplayCandidate, now: number = Date.now()): string {
  const parts = [candidate.triggerName, KIND_WORDS[candidate.kind] ?? candidate.kind];
  const sender = candidate.event.sender?.displayName;
  if (sender) parts.push(sender);
  const when = formatRelativeTime(candidate.event.occurredAt, now);
  if (when) parts.push(when);
  return parts.filter(Boolean).join(" · ");
}

/** Kinds a replay can be recorded as, in the order the hand-written form offers them. */
export const REPLAY_KINDS = ["integration", "webhook", "schedule", "workflow_event"] as const;
