// Copyright (c) 2025 Reliant Labs

/**
 * What one Overview tile shows when its read is slow, partial or failed.
 *
 * On 2026-11-05 the owner's Overview showed "That query took too long" in
 * place of Services, Process health AND Jobs: one ListWorkloads timeout
 * replaced the whole lower half, a failed background refresh replaced tables
 * that had data, and Retry flipped every tile to loading and back. The rules
 * here are the fix:
 *
 *  - Each tile draws its own state. A read that failed costs only the tiles
 *    it feeds, and each of those has its own Retry, which refetches that one
 *    query (joining a fetch already in flight rather than restarting it).
 *  - A tile that has data keeps drawing it through a failed refresh
 *    (stale-while-revalidate) and says the refresh failed. Only an answer
 *    that is true of the data itself (not set up, not allowed, an invalid
 *    query) replaces it.
 *  - A partial answer (the server's unavailable_sections) is drawn, and the
 *    sections it lacks are named, never shown as zero or "none".
 */

import { RefreshCw } from "lucide-react";

import { obsErrorKind } from "@/services/controlPlane/observability/errors";

/** Whether a tile keeps drawing `data` through `error` (a failed refresh) instead of the failure. */
export function keepsStaleResult(error: unknown, data: unknown): boolean {
  if (!data) return false;
  if (!error) return true;
  const kind = obsErrorKind(error);
  return kind !== "not-set-up" && kind !== "denied" && kind !== "invalid";
}

/** The user's words for a section the server could not answer in time. */
export const SECTION_LABEL: Record<string, string> = {
  usage: "CPU and memory",
  replicas: "replicas",
  restarts: "restarts",
  terminations: "last exits",
  warnings: "warnings",
  history: "health history",
  versions: "versions",
  jobs: "jobs",
  red: "request rate, errors and latency",
  logs: "error logs",
  trend: "trends",
};

export function sectionWords(sections: readonly string[]): string {
  const words = sections.map((section) => SECTION_LABEL[section] ?? section);
  if (words.length <= 1) return words.join("");
  return `${words.slice(0, -1).join(", ")} and ${words[words.length - 1]}`;
}

function RetryLink({ onRetry, retrying, testId }: { onRetry: () => void; retrying: boolean; testId: string }) {
  return (
    <button
      type="button"
      onClick={onRetry}
      disabled={retrying}
      className="ml-1 inline-flex items-center gap-1 font-medium text-ink underline-offset-2 hover:underline disabled:cursor-wait disabled:opacity-60"
      data-testid={testId}
    >
      <RefreshCw className={retrying ? "h-3 w-3 animate-spin motion-reduce:animate-none" : "h-3 w-3"} aria-hidden="true" />
      {retrying ? "Retrying" : "Retry"}
    </button>
  );
}

/** Under a tile drawn from a partial answer: which figures did not arrive, and a Retry for this tile alone. */
export function PartialNote({ sections, onRetry, retrying, testId }: { sections: readonly string[]; onRetry: () => void; retrying: boolean; testId: string }) {
  if (sections.length === 0) return null;
  return (
    <p role="status" className="border-t border-border px-4 py-2.5 text-xs text-ink-muted" data-testid={testId}>
      {sectionWords(sections).replace(/^./, (c) => c.toUpperCase())} did not arrive in time, so {sections.length === 1 ? "it is" : "they are"} not shown yet.
      <RetryLink onRetry={onRetry} retrying={retrying} testId={`${testId}-retry`} />
    </p>
  );
}

/** Under a tile still drawing its last result after a refresh failed. */
export function StaleNote({ onRetry, retrying, testId }: { onRetry: () => void; retrying: boolean; testId: string }) {
  return (
    <p role="status" className="border-t border-border px-4 py-2.5 text-xs text-ink-muted" data-testid={testId}>
      The latest refresh failed; this is the last result that loaded.
      <RetryLink onRetry={onRetry} retrying={retrying} testId={`${testId}-retry`} />
    </p>
  );
}
