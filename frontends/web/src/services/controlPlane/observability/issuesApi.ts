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
  type IssueUser as WireUser,
} from "@/gen/controlplane/controlplane/v1/issues_pb";
import {
  IssueActor_Kind as WireActorKind,
  IssueResolveKind,
  IssueSort as WireSort,
  IssueStatusChange as WireStatusChange,
  IssuesService,
  type GetIssueResponse,
  type IssueChatLink as WireChatLink,
  type IssueComment as WireComment,
  type IssueEvent as WireEvent,
  type IssueTimelineEntry as WireTimelineEntry,
} from "@/gen/controlplane/services/issues/v1/issues_pb";

import { obsClient } from "./obsClient";
import { msOf } from "./api";
import type {
  AddIssueCommentRequest,
  AssignIssueRequest,
  DeleteIssueCommentRequest,
  Issue,
  IssueActor,
  IssueChatLink,
  IssueComment,
  IssueDetail,
  IssueEvent,
  IssueEventList,
  IssueList,
  IssuePlatform,
  IssueSort,
  IssueStatus,
  IssueTimelineEntry,
  IssueTimelineList,
  IssueUser,
  LinkIssueChatRequest,
  LinkIssueChatResult,
  ListIssuesRequest,
  UpdateIssueCommentRequest,
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
    assignee: mapUser(i.assignee),
    incidentKey: i.incidentKey,
    relatedCount: i.relatedCount,
  };
}

export function mapUser(u: WireUser | undefined): IssueUser | null {
  return u ? { id: u.id, name: u.name } : null;
}

export function mapComment(c: WireComment): IssueComment {
  return { id: c.id, issueId: c.issueId, author: mapUser(c.author), body: c.body, createdMs: msOf(c.createdAt), updatedMs: msOf(c.updatedAt) };
}

export function mapChatLink(l: WireChatLink): IssueChatLink {
  return { issueId: l.issueId, chatId: l.chatId, chatTitle: l.chatTitle, linkedBy: mapUser(l.linkedBy), linkedAtMs: msOf(l.linkedAt) };
}

const ACTOR_KIND: Record<WireActorKind, IssueActor["kind"] | null> = {
  [WireActorKind.UNSPECIFIED]: null,
  [WireActorKind.USER]: "user",
  [WireActorKind.MACHINE]: "machine",
  [WireActorKind.SYSTEM]: "system",
};

/** An entry with no body (a oneof this client does not know) is dropped: the timeline shows only what it can read. */
export function mapTimelineEntry(e: WireTimelineEntry): IssueTimelineEntry | null {
  const base = {
    id: e.id,
    timeMs: msOf(e.time),
    issueId: e.issueId,
    actor: e.actor && ACTOR_KIND[e.actor.kind] ? { kind: ACTOR_KIND[e.actor.kind] as IssueActor["kind"], id: e.actor.id, name: e.actor.name } : null,
  };
  const entry = e.entry;
  switch (entry.case) {
    case "status":
      return {
        ...base,
        body: {
          kind: "status",
          from: STATUS_OF[entry.value.from] ?? null,
          to: STATUS_OF[entry.value.to] ?? null,
          version: entry.value.version,
          ignoredUntilMs: msOf(entry.value.ignoredUntil),
        },
      };
    case "comment":
      return entry.value.comment ? { ...base, body: { kind: "comment", comment: mapComment(entry.value.comment) } } : null;
    case "assignment":
      return { ...base, body: { kind: "assignment", from: mapUser(entry.value.from), to: mapUser(entry.value.to) } };
    case "chat":
      return entry.value.link ? { ...base, body: { kind: "chat", link: mapChatLink(entry.value.link) } } : null;
    default:
      return null;
  }
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
    incidentIssueIds: [...res.incidentIssueIds],
    firstRelease: release ? { version: release.version, gitCommit: release.gitCommit, commitRange: release.commitRange, promotedAtMs: msOf(release.promotedAt) } : null,
  };
}

/** One page of the scope's issues. No statuses is the server's default: the open ones. */
export async function listIssues(req: ListIssuesRequest, options: { signal?: AbortSignal } = {}): Promise<IssueList> {
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
    assignee: req.assignee || undefined,
    sort: WIRE_SORT[req.sort],
    pageSize: req.pageSize ?? 0,
    pageToken: req.pageToken ?? "",
  }, options);
  return { issues: res.issues.map(mapIssue), nextPageToken: res.nextPageToken };
}

/** One issue, with counts and trend over `window` (the server's 24 hours when absent). */
export async function getIssue(req: { issueId: string; window?: { startMs: number; endMs: number } }, options: { signal?: AbortSignal } = {}): Promise<IssueDetail> {
  const res = await client().getIssue({
    issueId: req.issueId,
    timeRange: req.window ? { start: timestamp(req.window.startMs), end: timestamp(req.window.endMs) } : undefined,
  }, options);
  return mapIssueDetail(res);
}

/** One issue's events, newest first, a page at a time (default range: its first to its last event). */
export async function listIssueEvents(req: { issueId: string; pageSize?: number; pageToken?: string }, options: { signal?: AbortSignal } = {}): Promise<IssueEventList> {
  const res = await client().listIssueEvents({ issueId: req.issueId, pageSize: req.pageSize ?? 0, pageToken: req.pageToken ?? "" }, options);
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

export async function addIssueComment(req: AddIssueCommentRequest): Promise<IssueComment> {
  const res = await client().addIssueComment({ issueId: req.issueId, body: req.body });
  if (!res.comment) throw new Error("AddIssueComment answered without a comment");
  return mapComment(res.comment);
}

export async function updateIssueComment(req: UpdateIssueCommentRequest): Promise<IssueComment> {
  const res = await client().updateIssueComment({ issueId: req.issueId, commentId: req.commentId, body: req.body });
  if (!res.comment) throw new Error("UpdateIssueComment answered without a comment");
  return mapComment(res.comment);
}

export async function deleteIssueComment(req: DeleteIssueCommentRequest): Promise<void> {
  await client().deleteIssueComment({ issueId: req.issueId, commentId: req.commentId });
}

/** Set or clear (empty `assigneeUserId`) the assignee; answers the issue as it now stands. A stale version is Aborted. */
export async function assignIssue(req: AssignIssueRequest): Promise<Issue> {
  const res = await client().assignIssue({ issueId: req.issueId, assigneeUserId: req.assigneeUserId, expectedVersion: req.expectedVersion });
  if (!res.issue) throw new Error("AssignIssue answered without an issue");
  return mapIssue(res.issue);
}

export async function linkIssueChat(req: LinkIssueChatRequest): Promise<LinkIssueChatResult> {
  const res = await client().linkIssueChat({ issueId: req.issueId, chatId: req.chatId, chatTitle: req.chatTitle ?? "" });
  if (!res.link) throw new Error("LinkIssueChat answered without a link");
  return { link: mapChatLink(res.link), created: res.created };
}

/** One page of an issue's timeline, oldest first. A merged issue answers with the issue it was merged into (`issueId`). */
export async function listIssueTimeline(req: { issueId: string; pageSize?: number; pageToken?: string }): Promise<IssueTimelineList> {
  const res = await client().listIssueTimeline({ issueId: req.issueId, pageSize: req.pageSize ?? 0, pageToken: req.pageToken ?? "" });
  const entries = res.entries.map(mapTimelineEntry).filter((e): e is IssueTimelineEntry => e !== null);
  return { entries, nextPageToken: res.nextPageToken, issueId: res.issueId || req.issueId };
}
