// Copyright (c) 2025 Reliant Labs

/**
 * The IssuesService and ActivityService wire mapping: the scope and filters →
 * request, the answer → model (int64 counts as numbers whether the codec gave
 * a bigint or protojson a string; unset timestamps as 0; enums as tokens), the
 * triage request with its version echo, and what each refusal means.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { create, fromJson } from "@bufbuild/protobuf";
import { TimestampSchema } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";

import { ActivityActorKind, ActivityEventSchema } from "@/gen/controlplane/services/activity/v1/activity_pb";
import { IssuePlatform, IssueSchema, IssueStatus } from "@/gen/controlplane/controlplane/v1/issues_pb";
import { GetIssueResponseSchema, IssueResolveKind, IssueSort, IssueStatusChange } from "@/gen/controlplane/services/issues/v1/issues_pb";

import { listActivity, mapActivityEvent } from "../activityApi";
import { msOf } from "../api";
import { issuesErrorKind, releaseHealthErrorKind } from "../errors";
import { countOf, getIssue, listIssues, mapIssue, mapIssueDetail, updateIssueStatus, wireStatusUpdate } from "../issuesApi";

const client = { listIssues: vi.fn(), getIssue: vi.fn(), listIssueEvents: vi.fn(), updateIssueStatus: vi.fn(), listActivity: vi.fn() };
vi.mock("../../client", () => ({ getControlPlaneClient: () => client }));

const T = Date.UTC(2026, 9, 22, 12);
const ts = (ms: number) => create(TimestampSchema, { seconds: BigInt(Math.floor(ms / 1000)), nanos: (ms % 1000) * 1e6 });

beforeEach(() => vi.clearAllMocks());

/** An issue as protojson writes it: int64s are strings, enums are names. */
const ISSUE_JSON = {
  id: "iss-1",
  platform: "ISSUE_PLATFORM_BROWSER",
  envId: "env-prod",
  envName: "prod",
  service: "web",
  fingerprint: "fp",
  fpVersion: 2,
  title: "TypeError: x is undefined",
  culprit: "render in Summary.tsx",
  exceptionType: "TypeError",
  status: "ISSUE_STATUS_REGRESSED",
  firstSeenAt: new Date(T - 86_400_000).toISOString(),
  firstVersion: "v1.8.1",
  firstRelease: "v1.8.1",
  lastSeenAt: new Date(T).toISOString(),
  lastVersion: "v1.8.3",
  events: "9007199254740",
  affected: "61",
  trend: [{ hour: new Date(T - 3_600_000).toISOString(), events: "12" }],
  resolvedVersion: "v1.8.2",
  symbolicated: true,
  version: 7,
};

describe("issues → model", () => {
  it("maps protojson int64 strings and enum names, and leaves unset times at 0", () => {
    const issue = mapIssue(fromJson(IssueSchema, ISSUE_JSON));
    expect(issue).toMatchObject({
      id: "iss-1",
      platform: "browser",
      status: "regressed",
      events: 9007199254740,
      affected: 61,
      firstSeenMs: T - 86_400_000,
      lastSeenMs: T,
      resolvedAtMs: 0,
      ignoredUntilMs: 0,
      version: 7,
    });
    expect(issue.trend).toEqual([{ hourMs: T - 3_600_000, events: 12 }]);
  });

  it("maps a bigint from the binary codec the same way", () => {
    const issue = mapIssue(create(IssueSchema, { id: "i", events: 42n, affected: 3n, platform: IssuePlatform.SERVER, status: IssueStatus.NEW }));
    expect(issue).toMatchObject({ events: 42, affected: 3, platform: "server", status: "new" });
    expect(countOf("")).toBe(0);
    expect(countOf(undefined)).toBe(0);
  });

  it("maps a detail: latest event, frames, versions, co-occurring, first release — and a missing event or release as null", () => {
    const full = mapIssueDetail(
      fromJson(GetIssueResponseSchema, {
        issue: ISSUE_JSON,
        latestEvent: { timestamp: new Date(T).toISOString(), traceId: "abc", spanId: "def", serviceVersion: "v1.8.3", message: "boom", attributes: { "url.path": "/checkout" }, sessionId: "s1", urlPath: "/checkout" },
        frames: [{ function: "render", file: "Summary.tsx", line: 42, column: 7, inApp: true }],
        byVersion: [{ version: "v1.8.3", events: "200", affected: "50", firstSeenAt: new Date(T - 60_000).toISOString(), lastSeenAt: new Date(T).toISOString() }],
        coOccurringIssueIds: ["iss-2"],
        firstRelease: { version: "v1.8.1", gitCommit: "abc123", commitRange: "a..b", promotedAt: new Date(T - 86_400_000).toISOString() },
      })
    );
    expect(full.latestEvent).toMatchObject({ timeMs: T, traceId: "abc", spanId: "def", attributes: { "url.path": "/checkout" }, sessionId: "s1" });
    expect(full.frames).toEqual([{ function: "render", file: "Summary.tsx", line: 42, column: 7, inApp: true }]);
    expect(full.byVersion).toEqual([{ version: "v1.8.3", events: 200, affected: 50, firstSeenMs: T - 60_000, lastSeenMs: T }]);
    expect(full.coOccurringIssueIds).toEqual(["iss-2"]);
    expect(full.firstRelease).toEqual({ version: "v1.8.1", gitCommit: "abc123", commitRange: "a..b", promotedAtMs: T - 86_400_000 });

    const bare = mapIssueDetail(fromJson(GetIssueResponseSchema, { issue: ISSUE_JSON }));
    expect(bare.latestEvent).toBeNull();
    expect(bare.firstRelease).toBeNull();
  });
});

describe("issues requests", () => {
  it("sends the scope with its window, the filters as enums, and the page token", async () => {
    client.listIssues.mockResolvedValue({ issues: [create(IssueSchema, { id: "i" })], nextPageToken: "p2" });
    const out = await listIssues({
      orgId: "org-1",
      projectId: "shop",
      envIds: ["env-prod"],
      services: ["web"],
      window: { startMs: T - 3_600_000, endMs: T },
      statuses: ["resolved", "ignored"],
      platform: "browser",
      text: "TypeError",
      sort: "events",
      pageSize: 50,
      pageToken: "p1",
    });
    const req = client.listIssues.mock.calls[0][0];
    expect(req.scope).toMatchObject({ orgId: "org-1", projectId: "shop", envIds: ["env-prod"], services: ["web"] });
    expect(msOf(req.scope.timeRange.start)).toBe(T - 3_600_000);
    expect(req.statuses).toEqual([IssueStatus.RESOLVED, IssueStatus.IGNORED]);
    expect(req).toMatchObject({ platform: IssuePlatform.BROWSER, text: "TypeError", sort: IssueSort.EVENTS_DESC, pageSize: 50, pageToken: "p1" });
    expect(out.nextPageToken).toBe("p2");
  });

  it("leaves the open default, the platform and the project to the server when unset", async () => {
    client.listIssues.mockResolvedValue({ issues: [], nextPageToken: "" });
    await listIssues({ orgId: "org-1", envIds: [], window: { startMs: 0, endMs: T }, statuses: [], sort: "last" });
    const req = client.listIssues.mock.calls[0][0];
    expect(req.statuses).toEqual([]);
    expect(req.platform).toBeUndefined();
    expect(req.scope.projectId).toBeUndefined();
    expect(req.sort).toBe(IssueSort.LAST_SEEN_DESC);
  });

  it("asks for one issue over a window", async () => {
    client.getIssue.mockResolvedValue(fromJson(GetIssueResponseSchema, { issue: ISSUE_JSON }));
    await getIssue({ issueId: "iss-1", window: { startMs: T - 1000, endMs: T } });
    expect(client.getIssue.mock.calls[0][0].issueId).toBe("iss-1");
    expect(msOf(client.getIssue.mock.calls[0][0].timeRange.end)).toBe(T);
  });
});

/** The resolve half of a status update; only a resolve carries one. */
function resolveInOf(update: ReturnType<typeof wireStatusUpdate>) {
  return "resolveIn" in update ? update.resolveIn : undefined;
}

describe("triage", () => {
  const base = { issueId: "iss-1", envId: "env-prod", expectedVersion: 7 };

  it("echoes the version read, and says when a resolve takes effect", () => {
    expect(wireStatusUpdate({ ...base, change: { status: "resolved", resolveIn: { kind: "now" } } })).toMatchObject({
      ...base,
      status: IssueStatusChange.RESOLVED,
      resolveIn: { kind: IssueResolveKind.NOW },
    });
    expect(resolveInOf(wireStatusUpdate({ ...base, change: { status: "resolved", resolveIn: { kind: "next_release" } } }))).toMatchObject({ kind: IssueResolveKind.NEXT_RELEASE });
    expect(resolveInOf(wireStatusUpdate({ ...base, change: { status: "resolved", resolveIn: { kind: "version", version: "v1.8.4" } } }))).toEqual({
      kind: IssueResolveKind.VERSION,
      version: "v1.8.4",
    });
  });

  it("sends an ignore's end as a timestamp, or none for until-unignored; a reopen carries neither", () => {
    const until = wireStatusUpdate({ ...base, change: { status: "ignored", untilMs: T } });
    expect(until.status).toBe(IssueStatusChange.IGNORED);
    expect(msOf((until as { ignoredUntil: ReturnType<typeof ts> }).ignoredUntil)).toBe(T);
    expect((wireStatusUpdate({ ...base, change: { status: "ignored" } }) as { ignoredUntil?: unknown }).ignoredUntil).toBeUndefined();
    expect(wireStatusUpdate({ ...base, change: { status: "unresolved" } })).toEqual({ ...base, status: IssueStatusChange.UNRESOLVED });
  });

  it("returns the issue as it now stands", async () => {
    client.updateIssueStatus.mockResolvedValue({ issue: create(IssueSchema, { id: "iss-1", status: IssueStatus.RESOLVED, version: 8 }) });
    const out = await updateIssueStatus({ ...base, change: { status: "unresolved" } });
    expect(out).toMatchObject({ status: "resolved", version: 8 });
  });

  it("reads Aborted as a conflict, NotFound as gone, and an older control plane as not configured", () => {
    expect(issuesErrorKind(new ConnectError("stale", Code.Aborted))).toBe("conflict");
    expect(issuesErrorKind(new ConnectError("no", Code.NotFound))).toBe("not-found");
    expect(issuesErrorKind(new ConnectError("x", Code.Unimplemented))).toBe("not-configured");
    expect(issuesErrorKind(new ConnectError("issues are not configured on this server", Code.Unavailable))).toBe("not-configured");
    expect(issuesErrorKind(new ConnectError("down", Code.Unavailable))).toBe("unavailable");
    expect(issuesErrorKind(new ConnectError("x", Code.FailedPrecondition))).toBe("not-set-up");
    expect(releaseHealthErrorKind(new ConnectError("x", Code.Unimplemented))).toBe("not-available");
    expect(releaseHealthErrorKind(new ConnectError("release health is not configured on this server", Code.Unavailable))).toBe("not-available");
    expect(releaseHealthErrorKind(new ConnectError("promotion", Code.NotFound))).toBe("not-found");
  });
});

describe("activity", () => {
  it("maps an event, falling back to ids for empty names and labels", () => {
    const e = mapActivityEvent(
      create(ActivityEventSchema, {
        id: "a1",
        at: ts(T),
        actor: { id: "u1", name: "", kind: ActivityActorKind.USER },
        verb: "query_database",
        target: { kind: "database", id: "shop", label: "" },
        env: { id: "env-prod", name: "prod" },
        project: "shop",
        via: "cli",
        detail: { reason: "why", row_count: "3" },
      })
    );
    expect(e).toEqual({
      id: "a1",
      atMs: T,
      actor: { id: "u1", name: "u1", kind: "user" },
      verb: "query_database",
      target: { kind: "database", id: "shop", label: "shop" },
      env: { id: "env-prod", name: "prod" },
      project: "shop",
      via: "cli",
      detail: { reason: "why", row_count: "3" },
    });
    expect(mapActivityEvent(create(ActivityEventSchema, { id: "a2" })).env).toBeNull();
  });

  it("narrows to one object's history", async () => {
    client.listActivity.mockResolvedValue({ events: [], nextPageToken: "" });
    await listActivity({ orgId: "org-1", projectId: "shop", envIds: ["env-prod"], targetKind: "issue", targetId: "iss-1" });
    expect(client.listActivity.mock.calls[0][0]).toMatchObject({
      scope: { orgId: "org-1", projectId: "shop", envIds: ["env-prod"] },
      targetKind: "issue",
      targetId: "iss-1",
      verb: undefined,
    });
  });
});
