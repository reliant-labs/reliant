// Copyright (c) 2025 Reliant Labs

/**
 * The console's model of control-plane IssuesService: the error inbox, browser
 * and server issues alike, with the triage lifecycle on top.
 *
 * Like ./types.ts these are not the wire types: ./issuesApi.ts is the one file
 * that maps the wire into these shapes. Times are epoch milliseconds (0 when
 * the server left one unset), counts are numbers (the wire's int64s), and the
 * enums are lower_snake strings a view can switch on and put in a URL.
 */

/** new → unresolved → resolved → regressed, with ignored reachable from any state. */
export const ISSUE_STATUSES = ["new", "unresolved", "regressed", "resolved", "ignored"] as const;
export type IssueStatus = (typeof ISSUE_STATUSES)[number];

/** What a list with no status filter shows: the open ones (the server's own default). */
export const OPEN_ISSUE_STATUSES: readonly IssueStatus[] = ["new", "unresolved", "regressed"];

/** Where the errors were emitted. `affected` counts sessions for a browser issue, traces for a server one. */
export const ISSUE_PLATFORMS = ["server", "browser"] as const;
export type IssuePlatform = (typeof ISSUE_PLATFORMS)[number];

export const ISSUE_SORTS = ["last", "first", "events", "affected"] as const;
export type IssueSort = (typeof ISSUE_SORTS)[number];

export interface IssueTrendPoint {
  hourMs: number;
  events: number;
}

/** A group of errors sharing a fingerprint, in one environment of one service. */
export interface Issue {
  id: string;
  /** Null when the server sent an unspecified platform (never, today). */
  platform: IssuePlatform | null;
  envId: string;
  envName: string;
  /** service.name for a server issue; the frontend name for a browser one. */
  service: string;
  fingerprint: string;
  title: string;
  /** Where it was raised: "<function> in <file>". */
  culprit: string;
  exceptionType: string;
  status: IssueStatus | null;
  firstSeenMs: number;
  firstVersion: string;
  /** The release the first version belongs to, when the ledger knows it. */
  firstRelease: string;
  lastSeenMs: number;
  lastVersion: string;
  /** Events in the queried window. */
  events: number;
  /** Distinct sessions (browser) or traces (server) in the queried window. */
  affected: number;
  /** Hourly event counts across the queried window, oldest first. */
  trend: IssueTrendPoint[];
  resolvedAtMs: number;
  resolvedVersion: string;
  regressedAtMs: number;
  /** 0: ignored until someone un-ignores it (or not ignored). */
  ignoredUntilMs: number;
  mergedInto: string;
  /** False when a browser stack could not be mapped back to source. */
  symbolicated: boolean;
  /** The row version, echoed back as `expectedVersion` so a triage only applies to the state read. */
  version: number;
}

export interface IssueEvent {
  timeMs: number;
  traceId: string;
  spanId: string;
  serviceVersion: string;
  message: string;
  /** An allowlisted, bounded subset of the event's attributes. */
  attributes: Record<string, string>;
  /** Browser issues only. */
  sessionId: string;
  urlPath: string;
}

export interface IssueFrame {
  function: string;
  file: string;
  line: number;
  column: number;
  /** False for a library or runtime frame. */
  inApp: boolean;
}

export interface IssueVersionBreakdown {
  version: string;
  events: number;
  affected: number;
  firstSeenMs: number;
  lastSeenMs: number;
}

export interface IssueReleaseRef {
  version: string;
  gitCommit: string;
  /** "<from>..<to>", when recorded. */
  commitRange: string;
  promotedAtMs: number;
}

export interface IssueDetail {
  issue: Issue;
  /** Null when the newest event has aged out of retention. */
  latestEvent: IssueEvent | null;
  /** The latest event's stack, innermost frame first. */
  frames: IssueFrame[];
  /** Newest version first. */
  byVersion: IssueVersionBreakdown[];
  /** Issues that fired in the same traces or sessions, most frequent first. */
  coOccurringIssueIds: string[];
  /** Null when the first version is not a release the ledger knows. */
  firstRelease: IssueReleaseRef | null;
}

export interface IssueList {
  issues: Issue[];
  /** Empty on the last page. */
  nextPageToken: string;
}

export interface IssueEventList {
  /** Newest first. */
  events: IssueEvent[];
  nextPageToken: string;
}

/** The issue scope: the telemetry scope's org, project and environments (filters mean nothing to issues). */
export interface IssueScope {
  orgId: string;
  projectId?: string;
  envIds: string[];
  services?: string[];
}

export interface ListIssuesRequest extends IssueScope {
  /** The window the counts and the trend are over. */
  window: { startMs: number; endMs: number };
  /** Empty: the open ones (new, unresolved, regressed). */
  statuses: IssueStatus[];
  platform?: IssuePlatform;
  text?: string;
  sort: IssueSort;
  pageSize?: number;
  pageToken?: string;
}

/** When a resolve takes effect, and so which later events reopen the issue. */
export type ResolveIn = { kind: "now" } | { kind: "next_release" } | { kind: "version"; version: string };

/** A triage: what a person may set. new and regressed are reached by the worker, never by a request. */
export type IssueStatusChange =
  | { status: "resolved"; resolveIn: ResolveIn }
  | { status: "ignored"; untilMs?: number }
  | { status: "unresolved" };

export interface UpdateIssueStatusRequest {
  issueId: string;
  envId: string;
  change: IssueStatusChange;
  /** The Issue.version the caller read. A stale one is refused (Aborted), never silently overwritten. */
  expectedVersion: number;
}
