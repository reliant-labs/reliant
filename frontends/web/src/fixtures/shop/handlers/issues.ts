// Copyright (c) 2025 Reliant Labs

/**
 * The shop pack's IssuesService reads: the error inbox, one issue, its events
 * and its timeline, and who an issue can be assigned to — shop's story
 * (model/issues.ts, through model/shopApi.ts) encoded on the wire, so the
 * console's real client (services/controlPlane/observability/issuesApi.ts,
 * membersApi.ts) decodes it as it decodes control-plane's answers.
 *
 * Triage (status, comments, assignment, chat links) is refused by the
 * transport's read-only guard before it reaches a handler. The handlers below
 * still decode each write into the model and ask `fixtures`, so a test's fake
 * answers the generated writes the console sends (test/observabilityFixtures.tsx).
 *
 * A relative window resolves against `clock` (the pack's by default), and
 * ListIssues echoes the range it read as effective_time_range, as the server
 * does, so the console pages "the last hour" on one range.
 */

import { mockService, type MockHandlers } from "@reliantlabs/forge-web-runtime/mock-transport";

import { IssuePlatform as WirePlatform, IssueStatus as WireStatus } from "@/gen/controlplane/controlplane/v1/issues_pb";
import {
  IssueActor_Kind as WireActorKind,
  IssueResolveKind as WireResolveKind,
  IssueSort as WireSort,
  IssueStatusChange as WireStatusChange,
  IssuesService,
  type UpdateIssueStatusRequest as WireStatusUpdate,
} from "@/gen/controlplane/services/issues/v1/issues_pb";
import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import type {
  Issue,
  IssueChatLink,
  IssueComment,
  IssueEvent,
  IssuePlatform,
  IssueSort,
  IssueStatus,
  IssueStatusChange,
  IssueTimelineEntry,
  IssueUser,
} from "@/services/controlPlane/observability/issuesTypes";

import { createShopApi } from "../model/shopApi";
import { demoNow } from "../shopStory";
import { completed } from "./defaults";
import { int64, msOf, ts, tsOpt, windowOf, type PackClock } from "./wire";

const WIRE_STATUS: Record<IssueStatus, WireStatus> = {
  new: WireStatus.NEW,
  unresolved: WireStatus.UNRESOLVED,
  resolved: WireStatus.RESOLVED,
  regressed: WireStatus.REGRESSED,
  ignored: WireStatus.IGNORED,
};
const STATUS_OF = Object.fromEntries(Object.entries(WIRE_STATUS).map(([k, v]) => [v, k])) as Record<WireStatus, IssueStatus | undefined>;
const WIRE_PLATFORM: Record<IssuePlatform, WirePlatform> = { browser: WirePlatform.BROWSER, server: WirePlatform.SERVER };
const PLATFORM_OF: Partial<Record<WirePlatform, IssuePlatform>> = { [WirePlatform.BROWSER]: "browser", [WirePlatform.SERVER]: "server" };
const SORT_OF: Partial<Record<WireSort, IssueSort>> = {
  [WireSort.LAST_SEEN_DESC]: "last",
  [WireSort.FIRST_SEEN_DESC]: "first",
  [WireSort.EVENTS_DESC]: "events",
  [WireSort.AFFECTED_DESC]: "affected",
};
const WIRE_ACTOR = { user: WireActorKind.USER, machine: WireActorKind.MACHINE, system: WireActorKind.SYSTEM } as const;

const wireStatus = (s: IssueStatus | null) => (s ? WIRE_STATUS[s] : WireStatus.UNSPECIFIED);
const wireUser = (u: IssueUser | null) => (u ? { id: u.id, name: u.name } : undefined);

/** An Issue on the wire (issuesApi.ts mapIssue, run backwards). Release health reports issues in the same shape. */
export function wireIssue(i: Issue) {
  return {
    id: i.id,
    platform: i.platform ? WIRE_PLATFORM[i.platform] : WirePlatform.UNSPECIFIED,
    envId: i.envId,
    envName: i.envName,
    service: i.service,
    fingerprint: i.fingerprint,
    title: i.title,
    culprit: i.culprit,
    exceptionType: i.exceptionType,
    status: wireStatus(i.status),
    firstSeenAt: tsOpt(i.firstSeenMs),
    firstVersion: i.firstVersion,
    firstRelease: i.firstRelease,
    lastSeenAt: tsOpt(i.lastSeenMs),
    lastVersion: i.lastVersion,
    events: int64(i.events),
    affected: int64(i.affected),
    trend: i.trend.map((p) => ({ hour: ts(p.hourMs), events: int64(p.events) })),
    resolvedAt: tsOpt(i.resolvedAtMs),
    resolvedVersion: i.resolvedVersion,
    regressedAt: tsOpt(i.regressedAtMs),
    ignoredUntil: tsOpt(i.ignoredUntilMs),
    mergedInto: i.mergedInto,
    symbolicated: i.symbolicated,
    version: i.version,
    assignee: wireUser(i.assignee),
    incidentKey: i.incidentKey,
    relatedCount: i.relatedCount,
  };
}

const wireComment = (c: IssueComment) => ({ id: c.id, issueId: c.issueId, author: wireUser(c.author), body: c.body, createdAt: ts(c.createdMs), updatedAt: ts(c.updatedMs) });
const wireChatLink = (l: IssueChatLink) => ({ issueId: l.issueId, chatId: l.chatId, chatTitle: l.chatTitle, linkedBy: wireUser(l.linkedBy), linkedAt: ts(l.linkedAtMs) });

function wireIssueEvent(e: IssueEvent) {
  return { ...e, timestamp: ts(e.timeMs), attributes: { ...e.attributes }, client: { ...e.client } };
}

function wireTimelineEntry(e: IssueTimelineEntry) {
  const body = e.body;
  const entry =
    body.kind === "status"
      ? { case: "status" as const, value: { from: wireStatus(body.from), to: wireStatus(body.to), version: body.version, ignoredUntil: tsOpt(body.ignoredUntilMs) } }
      : body.kind === "comment"
        ? { case: "comment" as const, value: { comment: wireComment(body.comment) } }
        : body.kind === "assignment"
          ? { case: "assignment" as const, value: { from: wireUser(body.from), to: wireUser(body.to) } }
          : { case: "chat" as const, value: { link: wireChatLink(body.link) } };
  return { id: e.id, time: ts(e.timeMs), issueId: e.issueId, actor: e.actor ? { kind: WIRE_ACTOR[e.actor.kind], id: e.actor.id, name: e.actor.name } : undefined, entry };
}

/** A triage on the wire (issuesApi.ts wireStatusUpdate, run backwards). */
function statusChangeOf(req: WireStatusUpdate): IssueStatusChange {
  switch (req.status) {
    case WireStatusChange.RESOLVED: {
      const r = req.resolveIn;
      const resolveIn = r?.kind === WireResolveKind.VERSION ? { kind: "version" as const, version: r.version } : { kind: r?.kind === WireResolveKind.NEXT_RELEASE ? ("next_release" as const) : ("now" as const) };
      return { status: "resolved", resolveIn };
    }
    case WireStatusChange.IGNORED:
      return { status: "ignored", untilMs: req.ignoredUntil ? msOf(req.ignoredUntil) : undefined };
    default:
      return { status: "unresolved" };
  }
}

export function issuesHandlers(fixtures: ObservabilityApi = createShopApi(), { clock = demoNow }: { clock?: PackClock } = {}): MockHandlers {
  return completed(IssuesService, mockService(IssuesService, {
    listIssues: async (req) => {
      const scope = req.scope;
      const window = windowOf(scope?.timeRange, clock) ?? { startMs: 0, endMs: 0 };
      const res = await fixtures.listIssues({
        orgId: scope?.orgId ?? "",
        projectId: scope?.projectId,
        envIds: [...(scope?.envIds ?? [])],
        services: [...(scope?.services ?? [])],
        window,
        statuses: req.statuses.flatMap((s) => STATUS_OF[s] ?? []),
        platform: req.platform === undefined ? undefined : PLATFORM_OF[req.platform],
        text: req.text,
        assignee: req.assignee,
        userId: req.userId,
        browser: req.browser,
        os: req.os,
        serviceVersion: req.serviceVersion,
        sort: SORT_OF[req.sort] ?? "last",
        pageSize: req.pageSize,
        pageToken: req.pageToken,
      });
      return { issues: res.issues.map(wireIssue), nextPageToken: res.nextPageToken, effectiveTimeRange: { start: ts(window.startMs), end: ts(window.endMs) } };
    },
    getIssue: async (req) => {
      const d = await fixtures.getIssue({ issueId: req.issueId, window: windowOf(req.timeRange, clock) });
      return {
        issue: wireIssue(d.issue),
        latestEvent: d.latestEvent ? wireIssueEvent(d.latestEvent) : undefined,
        frames: d.frames.map((f) => ({ ...f })),
        byVersion: d.byVersion.map((v) => ({ version: v.version, events: int64(v.events), affected: int64(v.affected), firstSeenAt: ts(v.firstSeenMs), lastSeenAt: ts(v.lastSeenMs) })),
        coOccurringIssueIds: [...d.coOccurringIssueIds],
        incidentIssueIds: [...d.incidentIssueIds],
        firstRelease: d.firstRelease ? { version: d.firstRelease.version, gitCommit: d.firstRelease.gitCommit, commitRange: d.firstRelease.commitRange, promotedAt: ts(d.firstRelease.promotedAtMs) } : undefined,
      };
    },
    listIssueEvents: async (req) => {
      const res = await fixtures.listIssueEvents({ issueId: req.issueId, pageSize: req.pageSize, pageToken: req.pageToken, userId: req.userId });
      return { events: res.events.map(wireIssueEvent), nextPageToken: res.nextPageToken };
    },
    listIssueTimeline: async (req) => {
      const res = await fixtures.listIssueTimeline({ issueId: req.issueId, pageSize: req.pageSize, pageToken: req.pageToken });
      return { entries: res.entries.map(wireTimelineEntry), nextPageToken: res.nextPageToken, issueId: res.issueId };
    },
    listAssignableUsers: async (req) => {
      // The story's roster is one page: the cast.
      const { users } = await fixtures.listOrgMembers(req.orgId);
      return { users: users.map((u) => ({ id: u.id, name: u.name })), nextPageToken: "" };
    },
    updateIssueStatus: async (req) => ({
      issue: wireIssue(await fixtures.updateIssueStatus({ issueId: req.issueId, envId: req.envId, change: statusChangeOf(req), expectedVersion: req.expectedVersion })),
    }),
    addIssueComment: async (req) => ({ comment: wireComment(await fixtures.addIssueComment({ issueId: req.issueId, body: req.body })) }),
    updateIssueComment: async (req) => ({ comment: wireComment(await fixtures.updateIssueComment({ issueId: req.issueId, commentId: req.commentId, body: req.body })) }),
    deleteIssueComment: async (req) => {
      await fixtures.deleteIssueComment({ issueId: req.issueId, commentId: req.commentId });
      return {};
    },
    assignIssue: async (req) => ({ issue: wireIssue(await fixtures.assignIssue({ issueId: req.issueId, assigneeUserId: req.assigneeUserId, expectedVersion: req.expectedVersion })) }),
    linkIssueChat: async (req) => {
      const { link, created } = await fixtures.linkIssueChat({ issueId: req.issueId, chatId: req.chatId, chatTitle: req.chatTitle || undefined });
      return { link: wireChatLink(link), created };
    },
  }));
}
