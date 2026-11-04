// Copyright (c) 2025 Reliant Labs

/**
 * The words and rules of the Errors view: how a status, a platform and a sort
 * read, which triage a status offers, and how the "resolve in" choice becomes
 * a request. Pure, so the components stay layout and the rules stay tested.
 */

import type { Issue, IssueDetail, IssuePlatform, IssueSort, IssueStatus, IssueStatusChange } from "@/services/controlPlane/observability/issuesTypes";

import type { AskSubject } from "../AskReliant/askReliantPrompt";

/**
 * What a chat about an issue is handed: references only (askReliantPrompt.ts).
 * A list row knows the issue; the detail also knows its latest event's trace
 * and the release the ledger matched its first version to.
 */
export function issueAskSubject(issue: Issue, detail?: Pick<IssueDetail, "latestEvent" | "firstRelease">): Extract<AskSubject, { kind: "issue" }> {
  return {
    kind: "issue",
    issueId: issue.id,
    env: issue.envName || null,
    latestTraceId: detail?.latestEvent?.traceId || undefined,
    firstVersion: detail?.firstRelease?.version || issue.firstRelease || issue.firstVersion || undefined,
    lastVersion: issue.lastVersion || undefined,
  };
}

export const STATUS_LABEL: Record<IssueStatus, string> = {
  new: "New",
  unresolved: "Unresolved",
  regressed: "Regressed",
  resolved: "Resolved",
  ignored: "Ignored",
};

/** forge-ui Badge variants: a regression is the loudest, a new issue next; closed ones are quiet. */
export const STATUS_TONE: Record<IssueStatus, "error" | "warning" | "info" | "success" | "neutral"> = {
  regressed: "error",
  new: "warning",
  unresolved: "info",
  resolved: "success",
  ignored: "neutral",
};

export const PLATFORM_LABEL: Record<IssuePlatform, string> = { server: "Server", browser: "Browser" };

export const SORT_LABEL: Record<IssueSort, string> = {
  last: "Last seen",
  first: "First seen",
  events: "Most events",
  affected: "Most affected",
};

/** What `affected` counts: sessions for a browser issue, traces for a server one. */
export function affectedNoun(platform: IssuePlatform | null, count: number): string {
  const noun = platform === "browser" ? "session" : "trace";
  return count === 1 ? noun : `${noun}s`;
}

/** The triage actions an issue in `status` offers. */
export function triageActionsOf(status: IssueStatus | null): ("resolve" | "ignore" | "unresolve")[] {
  if (status === "resolved" || status === "ignored") return ["unresolve"];
  return ["resolve", "ignore"];
}

/** How long an ignore lasts, as offered. */
export const IGNORE_FOR = [
  { id: "1h", label: "for an hour", ms: 3_600_000 },
  { id: "24h", label: "for a day", ms: 24 * 3_600_000 },
  { id: "7d", label: "for a week", ms: 7 * 24 * 3_600_000 },
  { id: "forever", label: "until I unignore it", ms: 0 },
] as const;
export type IgnoreFor = (typeof IGNORE_FOR)[number]["id"];

export type ResolveChoice = "now" | "next_release" | "version";

/** The request a resolve choice makes; null while "in version" names none. */
export function resolveChange(choice: ResolveChoice, version: string): IssueStatusChange | null {
  if (choice === "version") {
    const v = version.trim();
    return v ? { status: "resolved", resolveIn: { kind: "version", version: v } } : null;
  }
  return { status: "resolved", resolveIn: { kind: choice } };
}

export function ignoreChange(choice: IgnoreFor, now: number = Date.now()): IssueStatusChange {
  const ms = IGNORE_FOR.find((option) => option.id === choice)?.ms ?? 0;
  return ms ? { status: "ignored", untilMs: now + ms } : { status: "ignored" };
}

/**
 * The fields of an issue a triage conflict is about: what someone else
 * changed between the reader's read and their click. Compared field by field
 * so the conflict note can say "it was resolved in v1.8.4 by someone else",
 * not merely "it changed".
 */
export function conflictSummary(read: Issue, now: Issue): string {
  if (read.status !== now.status && now.status) {
    const by = now.status === "resolved" && now.resolvedVersion ? ` in ${now.resolvedVersion}` : "";
    return `It is ${STATUS_LABEL[now.status].toLowerCase()}${by} now, not ${read.status ? STATUS_LABEL[read.status].toLowerCase() : "what you read"}.`;
  }
  if ((read.assignee?.id ?? "") !== (now.assignee?.id ?? "")) {
    const was = read.assignee ? `assigned to ${read.assignee.name}` : "unassigned";
    return now.assignee ? `It is assigned to ${now.assignee.name} now, not ${was}.` : `It is unassigned now, not ${was}.`;
  }
  return "It changed since you opened it (new events, or another triage).";
}

/** "<function> in <file>:<line>" for a frame, the bundle location when unsymbolicated. */
export function frameText(frame: { function: string; file: string; line: number; column: number }): string {
  const where = frame.file ? `${frame.file}${frame.line ? `:${frame.line}${frame.column ? `:${frame.column}` : ""}` : ""}` : "";
  if (frame.function && where) return `${frame.function} in ${where}`;
  return frame.function || where || "(unknown frame)";
}
