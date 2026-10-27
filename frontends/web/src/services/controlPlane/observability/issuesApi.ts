// Copyright (c) 2025 Reliant Labs

/**
 * control-plane `controlplane.v1.IssuesService`, mapped into the console's
 * model (./issuesTypes.ts). The only file that knows its wire shape.
 *
 * A list is org-scoped (scope.org_id, membership checked server-side); a call
 * naming one issue carries no org — the server checks the caller against the
 * org that owns the issue. A triage echoes the version it read: a stale one is
 * Aborted (errors.ts issuesErrorKind "conflict"), so a person never overwrites
 * a change they have not seen.
 */

import { create } from "@bufbuild/protobuf";
import { TimestampSchema, type Timestamp } from "@bufbuild/protobuf/wkt";

import {
  IssuePlatform as WirePlatform,
  IssueStatus as WireStatus,
  type Issue as WireIssue,
} from "@/gen/controlplane/controlplane/v1/issues_pb";
import {
  IssueResolveKind,
  IssueSort as WireSort,
  IssueStatusChange as WireStatusChange,
  IssuesService,
  type GetIssueResponse,
  type IssueEvent as WireEvent,
} from "@/gen/controlplane/services/issues/v1/issues_pb";

import { obsClient } from "./obsClient";
import { msOf } from "./api";
import type {
  Issue,
  IssueDetail,
  IssueEvent,
  IssueEventList,
  IssueList,
  IssuePlatform,
  IssueSort,
  IssueStatus,
  ListIssuesRequest,
  UpdateIssueStatusRequest,
} from "./issuesTypes";

function client() {
  return obsClient(IssuesService);
}

function timestamp(ms: number): Timestamp {
  const seconds = Math.floor(ms / 1000);
  return create(TimestampSchema, { seconds: BigInt(seconds), nanos: Math.round((ms - seconds * 1000) * 1e6) });
}

/** An int64 count as a number: a bigint from the binary codec, a string from protojson. */
export function countOf(value: bigint | number | string | undefined): number {
  if (value === undefined || value === "") return 0;
  const n = Number(value);
  return Number.isFinite(n) ? n : 0;
}

const STATUS_OF: Record<WireStatus, IssueStatus | null> = {
  [WireStatus.UNSPECIFIED]: null,
  [WireStatus.NEW]: "new",
  [WireStatus.UNRESOLVED]: "unresolved",
  [WireStatus.RESOLVED]: "resolved",
  [WireStatus.REGRESSED]: "regressed",
  [WireStatus.IGNORED]: "ignored",
};

const WIRE_STATUS: Record<IssueStatus, WireStatus> = {
  new: WireStatus.NEW,
  unresolved: WireStatus.UNRESOLVED,
  resolved: WireStatus.RESOLVED,
  regressed: WireStatus.REGRESSED,
  ignored: WireStatus.IGNORED,
};

const PLATFORM_OF: Record<WirePlatform, IssuePlatform | null> = {
  [WirePlatform.UNSPECIFIED]: null,
  [WirePlatform.BROWSER]: "browser",
  [WirePlatform.SERVER]: "server",
};

const WIRE_PLATFORM: Record<IssuePlatform, WirePlatform> = { browser: WirePlatform.BROWSER, server: WirePlatform.SERVER };

const WIRE_SORT: Record<IssueSort, WireSort> = {
  last: WireSort.LAST_SEEN_DESC,
  first: WireSort.FIRST_SEEN_DESC,
  events: WireSort.EVENTS_DESC,
  affected: WireSort.AFFECTED_DESC,
};

export function mapIssue(i: WireIssue): Issue {
  return {
    id: i.id,
    platform: PLATFORM_OF[i.platform] ?? null,
    envId: i.envId,
    envName: i.envName,
    service: i.service,
    fingerprint: i.fingerprint,
    title: i.title,
    culprit: i.culprit,
    exceptionType: i.exceptionType,
    status: STATUS_OF[i.status] ?? null,
    firstSeenMs: msOf(i.firstSeenAt),
    firstVersion: i.firstVersion,
    firstRelease: i.firstRelease,
    lastSeenMs: msOf(i.lastSeenAt),
    lastVersion: i.lastVersion,
    events: countOf(i.events),
    affected: countOf(i.affected),
    trend: i.trend.map((p) => ({ hourMs: msOf(p.hour), events: countOf(p.events) })),
    resolvedAtMs: msOf(i.resolvedAt),
    resolvedVersion: i.resolvedVersion,
    regressedAtMs: msOf(i.regressedAt),
    ignoredUntilMs: msOf(i.ignoredUntil),
    mergedInto: i.mergedInto,
    symbolicated: i.symbolicated,
    version: i.version,
  };
}

export function mapIssueEvent(e: WireEvent): IssueEvent {
  return {
    timeMs: msOf(e.timestamp),
    traceId: e.traceId,
    spanId: e.spanId,
    serviceVersion: e.serviceVersion,
    message: e.message,
    attributes: { ...e.attributes },
    sessionId: e.sessionId,
    urlPath: e.urlPath,
  };
}

export function mapIssueDetail(res: GetIssueResponse): IssueDetail {
  if (!res.issue) throw new Error("GetIssue answered without an issue");
  const release = res.firstRelease;
  return {
    issue: mapIssue(res.issue),
    latestEvent: res.latestEvent ? mapIssueEvent(res.latestEvent) : null,
    frames: res.frames.map((f) => ({ function: f.function, file: f.file, line: f.line, column: f.column, inApp: f.inApp })),
    byVersion: res.byVersion.map((v) => ({
      version: v.version,
      events: countOf(v.events),
      affected: countOf(v.affected),
      firstSeenMs: msOf(v.firstSeenAt),
      lastSeenMs: msOf(v.lastSeenAt),
    })),
    coOccurringIssueIds: [...res.coOccurringIssueIds],
    firstRelease: release ? { version: release.version, gitCommit: release.gitCommit, commitRange: release.commitRange, promotedAtMs: msOf(release.promotedAt) } : null,
  };
}

/** One page of the scope's issues. No statuses is the server's default: the open ones. */
export async function listIssues(req: ListIssuesRequest): Promise<IssueList> {
  const res = await client().listIssues({
    scope: {
      orgId: req.orgId,
      projectId: req.projectId || undefined,
      envIds: req.envIds,
      services: req.services ?? [],
      timeRange: { start: timestamp(req.window.startMs), end: timestamp(req.window.endMs) },
    },
    statuses: req.statuses.map((s) => WIRE_STATUS[s]),
    platform: req.platform ? WIRE_PLATFORM[req.platform] : undefined,
    text: req.text || undefined,
    sort: WIRE_SORT[req.sort],
    pageSize: req.pageSize ?? 0,
    pageToken: req.pageToken ?? "",
  });
  return { issues: res.issues.map(mapIssue), nextPageToken: res.nextPageToken };
}

/** One issue, with counts and trend over `window` (the server's 24 hours when absent). */
export async function getIssue(req: { issueId: string; window?: { startMs: number; endMs: number } }): Promise<IssueDetail> {
  const res = await client().getIssue({
    issueId: req.issueId,
    timeRange: req.window ? { start: timestamp(req.window.startMs), end: timestamp(req.window.endMs) } : undefined,
  });
  return mapIssueDetail(res);
}

/** One issue's events, newest first, a page at a time (default range: its first to its last event). */
export async function listIssueEvents(req: { issueId: string; pageSize?: number; pageToken?: string }): Promise<IssueEventList> {
  const res = await client().listIssueEvents({ issueId: req.issueId, pageSize: req.pageSize ?? 0, pageToken: req.pageToken ?? "" });
  return { events: res.events.map(mapIssueEvent), nextPageToken: res.nextPageToken };
}

/** The wire request of a triage, separate so the mapping is testable without a client. */
export function wireStatusUpdate(req: UpdateIssueStatusRequest) {
  const base = { issueId: req.issueId, envId: req.envId, expectedVersion: req.expectedVersion };
  const change = req.change;
  switch (change.status) {
    case "resolved": {
      const r = change.resolveIn;
      return {
        ...base,
        status: WireStatusChange.RESOLVED,
        resolveIn:
          r.kind === "version"
            ? { kind: IssueResolveKind.VERSION, version: r.version }
            : { kind: r.kind === "next_release" ? IssueResolveKind.NEXT_RELEASE : IssueResolveKind.NOW, version: "" },
      };
    }
    case "ignored":
      return { ...base, status: WireStatusChange.IGNORED, ignoredUntil: change.untilMs ? timestamp(change.untilMs) : undefined };
    case "unresolved":
      return { ...base, status: WireStatusChange.UNRESOLVED };
  }
}

/** Resolve, ignore or reopen; answers the issue as it now stands. */
export async function updateIssueStatus(req: UpdateIssueStatusRequest): Promise<Issue> {
  const res = await client().updateIssueStatus(wireStatusUpdate(req));
  if (!res.issue) throw new Error("UpdateIssueStatus answered without an issue");
  return mapIssue(res.issue);
}
