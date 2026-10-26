// Copyright (c) 2025 Reliant Labs

/**
 * An automation's name, derived from When + What when the person gives none
 * (report 03 §5: "Weekdays 9:00 · Summarise failed CI", "On new PR · PR
 * review"). Names are unique per user per project on the server, so a taken
 * one gets " 2", " 3", … as CreateWorkflowDraft does.
 *
 * Pure: the caller supplies the When in words.
 */

const MAX_WHAT_WORDS = 6;
const MAX_NAME_LENGTH = 80;

/** The prompt's first clause, trimmed to a few words: "Summarise failed CI runs". */
export function whatFromPrompt(prompt: string): string {
  const firstLine = prompt.trim().split(/\n/)[0] ?? "";
  const clause = firstLine.split(/(?<=[.!?;:])\s/)[0] ?? firstLine;
  const words = clause.replace(/[.!?;:,]+$/, "").split(/\s+/).filter(Boolean);
  if (words.length === 0) return "";
  const head = words.slice(0, MAX_WHAT_WORDS).join(" ");
  return words.length > MAX_WHAT_WORDS ? `${head}…` : head;
}

/**
 * "Every weekday at 9:00 AM" → "Weekdays 9:00 AM": the When as a short label.
 * Anything it does not recognise passes through.
 */
export function shortWhen(when: string): string {
  return when
    .replace(/^Every weekday at /, "Weekdays ")
    .replace(/^Every day at /, "Daily ")
    .replace(/^Every (Monday|Tuesday|Wednesday|Thursday|Friday|Saturday|Sunday) at /, "$1s ")
    .replace(/:00 AM\b/, " AM")
    .replace(/:00 PM\b/, " PM")
    .trim();
}

export interface AutoNameInput {
  /** The When in words (describeSchedule / describeDeclaredSource). */
  when: string;
  /** The workflow's display name, when one is chosen. */
  workflowName?: string;
  prompt: string;
}

export function autoName({ when, workflowName, prompt }: AutoNameInput): string {
  const what = workflowName || whatFromPrompt(prompt) || "Agent";
  const name = [shortWhen(when), what].filter(Boolean).join(" · ");
  return name.length > MAX_NAME_LENGTH ? `${name.slice(0, MAX_NAME_LENGTH - 1)}…` : name;
}

/** `name`, or `name 2`, `name 3`… — the first one not in `taken` (case-insensitive). */
export function uniqueName(name: string, taken: Iterable<string>): string {
  const used = new Set([...taken].map((t) => t.trim().toLowerCase()));
  if (!used.has(name.trim().toLowerCase())) return name;
  for (let n = 2; n < 1000; n++) {
    const candidate = `${name} ${n}`;
    if (!used.has(candidate.toLowerCase())) return candidate;
  }
  return name;
}
