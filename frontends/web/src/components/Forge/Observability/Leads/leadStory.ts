// Copyright (c) 2026 Reliant Labs

/**
 * The sentences the led console (Console/ledConsole.tsx) puts above a list
 * or a detail page, each built from the data the page shows and nothing else:
 * no sentence names a particular app, route or span, so it reads the same for
 * any project. Where the data cannot carry a sentence (no root span, nothing
 * recorded) a function returns null and the page shows its facts alone.
 *
 * Pure, so every edge (nothing at all, one of a thing, a truncated page, long
 * names) is tested without rendering (__tests__/leadStory.test.ts).
 */

import type { Issue } from "@/services/controlPlane/observability/issuesTypes";
import type { SessionSummary } from "@/services/controlPlane/observability/rumTypes";
import type { Span, TraceSummary } from "@/services/controlPlane/observability/types";

import { formatAgo, formatCount, formatDuration } from "../format";

/** "1 issue", "12 issues", "1.2k issues". */
export const plural = (n: number, one: string, many = `${one}s`): string => `${formatCount(n)} ${n === 1 ? one : many}`;

/** Where the reader is looking: one environment by name, or every environment. */
export const scopeWords = (env: string | null): string => (env ? `in ${env}` : "across every environment");

export type Tone = "good" | "warn" | "bad";

export interface Summary {
  tone: Tone;
  text: string;
}

// ── Errors ──────────────────────────────────────────────────────────────────

/** The open issues and those resolved in the same window, as the Errors lead says them. */
export function errorsSummary({ open, resolved, env, more }: { open: readonly Issue[]; resolved: readonly Issue[]; env: string | null; more: boolean }): Summary {
  const fresh = open.filter((issue) => issue.status === "new").length;
  const regressed = open.filter((issue) => issue.status === "regressed").length;
  const closed = resolved.length ? ` ${plural(resolved.length, "issue")} resolved in the last 7 days.` : "";
  if (!open.length) return { tone: "good", text: `No open issues ${scopeWords(env)}.${closed}` };
  const counted = more ? `At least ${plural(open.length, "open issue")}` : plural(open.length, "open issue");
  const notes = [fresh ? `${formatCount(fresh)} new` : "", regressed ? `${formatCount(regressed)} back after a fix` : ""].filter(Boolean);
  return { tone: "bad", text: `${counted} ${scopeWords(env)}${notes.length ? `, ${notes.join(" and ")}` : ""}.${closed}` };
}

/** The issue a reader should look at first: the newest to appear, else the most recent to recur. */
export function leadIssue(open: readonly Issue[]): Issue | null {
  if (!open.length) return null;
  return [...open].sort((a, b) => b.firstSeenMs - a.firstSeenMs || b.lastSeenMs - a.lastSeenMs)[0];
}

/**
 * One issue in a sentence: what was raised, where, and since when. Null when
 * the issue names neither an exception type nor a place.
 */
export function issueStory(issue: Issue, now: number = Date.now()): string | null {
  const what = issue.exceptionType || (issue.culprit ? "An error" : "");
  if (!what) return null;
  const where = issue.culprit ? ` in ${issue.culprit}` : issue.service ? ` in ${issue.service}` : "";
  const since = issue.firstSeenMs ? ` It first happened ${formatAgo(issue.firstSeenMs, now)}${issue.firstVersion ? `, in ${issue.firstVersion}` : ""}.` : "";
  const back = issue.status === "regressed" ? ` It came back after it was marked resolved${issue.resolvedVersion ? ` in ${issue.resolvedVersion}` : ""}.` : "";
  return `${what} raised${where}.${since}${back}`;
}

// ── Traces ──────────────────────────────────────────────────────────────────

/** The middle duration of a list (0 for none). */
export function median(values: readonly number[]): number {
  if (!values.length) return 0;
  const sorted = [...values].sort((a, b) => a - b);
  const mid = Math.floor(sorted.length / 2);
  return sorted.length % 2 ? sorted[mid] : (sorted[mid - 1] + sorted[mid]) / 2;
}

/** A request is called out as slow from a second. */
export const SLOW_MS = 1000;

export function tracesSummary({ traces, window, more }: { traces: readonly TraceSummary[]; window: string; more: boolean }): Summary {
  if (!traces.length) return { tone: "good", text: `No requests recorded in the ${window}.` };
  const slowest = traces.reduce((a, b) => (b.durationMs > a.durationMs ? b : a));
  const failed = traces.filter((trace) => trace.errorCount > 0).length;
  const counted = more ? `The newest ${plural(traces.length, "request")}` : plural(traces.length, "request");
  const typical = traces.length > 1 ? ` Half answered in under ${formatDuration(median(traces.map((trace) => trace.durationMs)))};` : "";
  const slow = traces.length > 1 ? ` the slowest, ${slowest.rootOperation || "one with no name"}, took ${formatDuration(slowest.durationMs)}.` : ` It took ${formatDuration(slowest.durationMs)}.`;
  const errors = failed ? ` ${plural(failed, "request")} ended in an error.` : "";
  return { tone: failed ? "bad" : slowest.durationMs >= SLOW_MS ? "warn" : "good", text: `${counted} in the ${window}.${typical}${slow}${errors}` };
}

export interface Step {
  span: Span;
  depth: number;
  /** Time in this span not spent in a child. */
  self: number;
}

/** A trace's spans in timeline order, each with its depth and its own time. A span whose parent was not fetched is a root. */
export function stepsOf(spans: readonly Span[]): Step[] {
  const ids = new Set(spans.map((span) => span.spanId));
  const children = new Map<string, Span[]>();
  for (const span of spans) {
    const parent = span.parentSpanId && ids.has(span.parentSpanId) ? span.parentSpanId : "";
    children.set(parent, [...(children.get(parent) ?? []), span]);
  }
  const out: Step[] = [];
  const seen = new Set<string>();
  const walk = (span: Span, depth: number) => {
    if (seen.has(span.spanId)) return; // a malformed trace may name itself an ancestor
    seen.add(span.spanId);
    const kids = [...(children.get(span.spanId) ?? [])].sort((a, b) => a.startMs - b.startMs);
    out.push({ span, depth, self: Math.max(0, span.durationMs - kids.reduce((total, kid) => total + kid.durationMs, 0)) });
    for (const kid of kids) walk(kid, depth + 1);
  };
  for (const root of [...(children.get("") ?? [])].sort((a, b) => a.startMs - b.startMs)) walk(root, 0);
  // A malformed trace's cycle (a span its own ancestor) reaches no root: each of its spans not yet walked is one.
  for (const span of [...spans].sort((a, b) => a.startMs - b.startMs)) walk(span, 0);
  return out;
}

/** The step that held the request longest on its own, when it held at least a fifth of it. */
export function slowStepOf(steps: readonly Step[]): Step | null {
  const root = steps[0]?.span;
  if (!root || !root.durationMs) return null;
  const slow = steps.slice(1).reduce<Step | null>((a, b) => (!a || b.self > a.self ? b : a), null);
  return slow && slow.self / root.durationMs >= 0.2 ? slow : null;
}

/** One sentence: how long the request took, where most of its time went, and where it failed. Null for no spans. */
export function traceStory(spans: readonly Span[]): string | null {
  const steps = stepsOf(spans);
  const root = steps[0]?.span;
  if (!root) return null;
  const slow = slowStepOf(steps);
  // Where the failure started: the deepest failed step (its callers fail because it did).
  const failed = [...steps].reverse().find((step) => step.span.status === "error")?.span;
  const where =
    steps.length < 2
      ? ""
      : slow
        ? ` The longest step, ${Math.round((slow.self / root.durationMs) * 100)}% of it, was ${slow.span.name} in ${slow.span.service}.`
        : " No single step stands out.";
  const fail = failed ? ` It failed in ${failed.service}${failed.statusMessage ? `: “${failed.statusMessage}”` : ""}.` : "";
  return `${root.name} took ${formatDuration(root.durationMs)}.${where}${fail}`;
}

// ── Sessions ────────────────────────────────────────────────────────────────

export const sessionLength = (session: Pick<SessionSummary, "startedMs" | "endedMs">): number => Math.max(0, session.endedMs - session.startedMs);

/** The visits that hit an error first (their replays are the ones worth watching), then the newest. */
export function sessionsByInterest(sessions: readonly SessionSummary[]): SessionSummary[] {
  return [...sessions].sort((a, b) => Number(b.errors > 0) - Number(a.errors > 0) || b.startedMs - a.startedMs);
}

export function sessionsSummary({ sessions, window, more }: { sessions: readonly SessionSummary[]; window: string; more: boolean }): Summary {
  if (!sessions.length) return { tone: "good", text: `No visits recorded in the ${window}.` };
  const withErrors = sessions.filter((session) => session.errors > 0).length;
  const counted = more ? `The newest ${plural(sessions.length, "visit")}` : plural(sessions.length, "visit");
  const errors = withErrors
    ? ` ${withErrors === sessions.length && sessions.length > 1 ? "Every one" : plural(withErrors, "visit")} hit an error; ${withErrors === 1 ? "its replay is" : "their replays are"} marked.`
    : " None hit an error.";
  return { tone: withErrors ? "warn" : "good", text: `${counted} recorded in the ${window}.${errors}` };
}

/** One visit in a sentence: who, where they started, how far they went, and whether it went wrong. */
export function sessionStory(session: SessionSummary): string {
  const who = session.userId ? `User ${session.userId}` : "A signed-out visitor";
  const pages = session.pageViews > 0 ? `, viewed ${plural(session.pageViews, "page")}` : "";
  const length = sessionLength(session) > 0 ? ` over ${formatDuration(sessionLength(session))}` : "";
  const errors = session.errors ? ` and hit ${plural(session.errors, "error")}.` : ", with no errors.";
  return `${who} started on ${session.entryPath || "/"}${pages}${length}${errors}`;
}

// ── Overview ────────────────────────────────────────────────────────────────

/** What the Overview's "Needs attention" lists: an alert rule firing, a workload or a job in trouble. */
export interface AttentionLike {
  kind: "alert" | "workload" | "job";
  subject: string;
  what: string;
  envName: string;
}

/**
 * The Overview's one sentence, from its "Needs attention" items: nothing, the
 * one thing by name, or how many and the first by name. Null while they are
 * unknown (a failed read says so in the panel itself).
 */
export function overviewSummary(items: readonly AttentionLike[] | null, env: string | null): Summary | null {
  if (!items) return null;
  if (!items.length) return { tone: "good", text: `Nothing needs attention ${scopeWords(env)}.` };
  const first = items[0];
  const named = `${first.subject} ${first.what}${env ? "" : ` in ${first.envName}`}`;
  const tone: Tone = items.some((item) => item.kind === "alert") ? "bad" : "warn";
  if (items.length === 1) return { tone, text: `One thing needs attention ${scopeWords(env)}: ${named}.` };
  return { tone, text: `${formatCount(items.length)} things need attention ${scopeWords(env)}, first ${named}.` };
}
