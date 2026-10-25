// Copyright (c) 2025 Reliant Labs

/**
 * The RumService wire mapping: scope and filters → request (a session read
 * sends no window, so the server finds the session itself), summaries and
 * timeline entries → model (int64 counts whether bigint or protojson string,
 * enum names → tokens), and the replay bytes: base64 in Connect-JSON decoded
 * back to the recorded JSON, gzip-packed events inflated, and anything that is
 * not an rrweb event skipped and counted, never guessed at.
 */

import { gzipSync } from "node:zlib";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { create, fromJson } from "@bufbuild/protobuf";
import { TimestampSchema } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";

import {
  GetSessionReplayResponseSchema,
  SessionSummarySchema,
  SessionTimelineEntrySchema,
  SessionTimelineKind,
} from "@/gen/controlplane/services/rum/v1/rum_pb";

import { rumErrorKind } from "../errors";
import {
  decodeReplayData,
  getSession,
  getSessionReplay,
  getSessionTimeline,
  listSessions,
  mapReplayPage,
  mapSessionSummary,
  mapTimelineEntry,
} from "../rumApi";

const client = { listSessions: vi.fn(), getSession: vi.fn(), getSessionTimeline: vi.fn(), getSessionReplay: vi.fn() };
vi.mock("../../client", () => ({ getControlPlaneClient: () => client }));

const T = Date.UTC(2026, 9, 24, 12);
const ts = (ms: number) => create(TimestampSchema, { seconds: BigInt(Math.floor(ms / 1000)), nanos: (ms % 1000) * 1e6 });
const SCOPE = { orgId: "org-1", projectId: "shop", envIds: ["env-prod"] };

beforeEach(() => vi.clearAllMocks());

const b64 = (bytes: Uint8Array | string) => Buffer.from(bytes).toString("base64");

/** A session as protojson writes it: int64s are strings. */
const SUMMARY_JSON = {
  sessionId: "sess-1",
  service: "shop-web",
  serviceVersion: "v1.8.3",
  envId: "env-prod",
  envName: "prod",
  userId: "u_1",
  entryPath: "/checkout",
  startedAt: new Date(T).toISOString(),
  endedAt: new Date(T + 61_000).toISOString(),
  pageViews: "3",
  interactions: "12",
  errors: "1",
  hasReplay: true,
};

describe("sessions", () => {
  it("maps a summary, counts as numbers whether protojson strings or codec bigints", () => {
    const fromProtojson = mapSessionSummary(fromJson(SessionSummarySchema, SUMMARY_JSON));
    expect(fromProtojson).toEqual({
      sessionId: "sess-1",
      service: "shop-web",
      serviceVersion: "v1.8.3",
      envId: "env-prod",
      envName: "prod",
      userId: "u_1",
      entryPath: "/checkout",
      startedMs: T,
      endedMs: T + 61_000,
      pageViews: 3,
      interactions: 12,
      errors: 1,
      hasReplay: true,
    });
    const fromBinary = mapSessionSummary(create(SessionSummarySchema, { sessionId: "s", pageViews: 4n, errors: 0n }));
    expect(fromBinary).toMatchObject({ pageViews: 4, errors: 0, startedMs: 0, hasReplay: false });
  });

  it("sends the filters and the window with the scope, and pages by the server's token", async () => {
    client.listSessions.mockResolvedValue({ sessions: [fromJson(SessionSummarySchema, SUMMARY_JSON)], nextPageToken: "tok-2" });
    const res = await listSessions({
      ...SCOPE,
      services: ["shop-web"],
      window: { startMs: T - 3_600_000, endMs: T },
      hasError: true,
      hasReplay: false,
      userId: "u_1",
      pathContains: "check",
      pageSize: 25,
      pageToken: "tok-1",
    });
    const [req] = client.listSessions.mock.calls[0];
    expect(req).toMatchObject({
      scope: { orgId: "org-1", projectId: "shop", envIds: ["env-prod"], services: ["shop-web"] },
      hasError: true,
      hasReplay: false,
      userId: "u_1",
      pathContains: "check",
      pageSize: 25,
      pageToken: "tok-1",
    });
    expect(Number(req.scope.timeRange.start.seconds) * 1000).toBe(T - 3_600_000);
    expect(res).toEqual({ sessions: [expect.objectContaining({ sessionId: "sess-1" })], nextPageToken: "tok-2" });
  });

  it("reads one session with no window, so the server finds it in the last 30 days", async () => {
    client.getSession.mockResolvedValue({ session: fromJson(SessionSummarySchema, SUMMARY_JSON) });
    await getSession({ ...SCOPE, sessionId: "sess-1" });
    const [req] = client.getSession.mock.calls[0];
    expect(req.sessionId).toBe("sess-1");
    expect(req.scope.timeRange).toBeUndefined();
    expect(req.scope.projectId).toBe("shop");
  });

  it("says what each refusal means", () => {
    expect(rumErrorKind(new ConnectError("no", Code.NotFound))).toBe("not-found");
    expect(rumErrorKind(new ConnectError("procedure not implemented", Code.Unimplemented))).toBe("not-configured");
    expect(rumErrorKind(new ConnectError("sessions are not configured on this server", Code.Unavailable))).toBe("not-configured");
    expect(rumErrorKind(new ConnectError("observability is not set up", Code.FailedPrecondition))).toBe("not-set-up");
    expect(rumErrorKind(new ConnectError("not a member", Code.PermissionDenied))).toBe("denied");
  });
});

describe("the timeline", () => {
  it("maps each kind to its token, and an unknown kind to null", () => {
    const entry = mapTimelineEntry(
      fromJson(SessionTimelineEntrySchema, {
        time: new Date(T + 8_450).toISOString(),
        kind: "SESSION_TIMELINE_KIND_ERROR",
        name: "onerror",
        path: "/checkout",
        errorType: "TypeError",
        message: "Cannot read properties of undefined (reading 'total')",
        fingerprint: "fp-1",
        issueId: "iss-checkout-total",
        traceId: "4bf92f3577b34da6a3ce929d0e0e4736",
        spanId: "b7ad6b7169203331",
        props: { from: "/cart" },
        service: "shop-web",
      })
    );
    expect(entry).toMatchObject({ timeMs: T + 8_450, kind: "error", issueId: "iss-checkout-total", fingerprint: "fp-1", props: { from: "/cart" } });
    const kinds = [
      SessionTimelineKind.NAVIGATION,
      SessionTimelineKind.CLICK,
      SessionTimelineKind.REQUEST_FAILED,
      SessionTimelineKind.EVENT,
      SessionTimelineKind.UNSPECIFIED,
    ].map((kind) => mapTimelineEntry(create(SessionTimelineEntrySchema, { kind, time: ts(T) })).kind);
    expect(kinds).toEqual(["navigation", "click", "request_failed", "event", null]);
  });

  it("pages the timeline with no window", async () => {
    client.getSessionTimeline.mockResolvedValue({ entries: [], nextPageToken: "more" });
    const res = await getSessionTimeline({ ...SCOPE, sessionId: "sess-1", pageSize: 500, pageToken: "p1" });
    const [req] = client.getSessionTimeline.mock.calls[0];
    expect(req).toMatchObject({ sessionId: "sess-1", pageSize: 500, pageToken: "p1" });
    expect(req.scope.timeRange).toBeUndefined();
    expect(res.nextPageToken).toBe("more");
  });
});

describe("replay bytes", () => {
  const META = { type: 4, timestamp: T, data: { href: "https://shop.example/", width: 1280, height: 720 } };
  const SNAPSHOT = { type: 2, timestamp: T + 10, data: { node: { type: 0, id: 1, childNodes: [] }, initialOffset: { left: 0, top: 0 } } };

  it("decodes Connect-JSON's base64 `data` back to the recorded event, byte for byte", async () => {
    // What admin-server writes: protojson renders `bytes` as base64.
    const res = fromJson(GetSessionReplayResponseSchema, {
      events: [
        { time: new Date(T).toISOString(), index: 1, chunks: 1, data: b64(JSON.stringify(META)) },
        { time: new Date(T + 10).toISOString(), index: 2, chunks: 3, data: b64(JSON.stringify(SNAPSHOT)) },
      ],
      nextPageToken: "next",
      resumeToken: "resume",
    });
    expect(res.events[0].data).toBeInstanceOf(Uint8Array);
    const page = await mapReplayPage(res);
    expect(page).toEqual({ events: [META, SNAPSHOT], unreadable: 0, nextPageToken: "next", resumeToken: "resume" });
  });

  it("keeps masked text exactly as recorded — the client never alters an event", async () => {
    const maskedEvent = { type: 3, timestamp: T + 20, data: { source: 0, texts: [{ id: 7, value: "**** ****" }], adds: [], removes: [], attributes: [] } };
    const decoded = await decodeReplayData(new TextEncoder().encode(JSON.stringify(maskedEvent)));
    expect(decoded).toEqual(maskedEvent);
  });

  it("inflates an event stored gzip-packed, and accepts the raw base64 text too", async () => {
    const gz = gzipSync(Buffer.from(JSON.stringify(SNAPSHOT)));
    expect(await decodeReplayData(new Uint8Array(gz))).toEqual(SNAPSHOT);
    expect(await decodeReplayData(b64(JSON.stringify(META)))).toEqual(META);
    expect(await decodeReplayData(b64(gz))).toEqual(SNAPSHOT);
  });

  it("skips and counts what is not an rrweb event", async () => {
    const bad = [
      "not json",
      JSON.stringify([1, 2]),
      JSON.stringify({ type: 2 }), // no timestamp
      JSON.stringify({ timestamp: T }), // no type
    ].map((text) => ({ time: new Date(T).toISOString(), index: 9, chunks: 1, data: b64(text) }));
    const truncatedGzip = gzipSync(Buffer.from(JSON.stringify(META))).subarray(0, 12);
    const res = fromJson(GetSessionReplayResponseSchema, {
      events: [{ time: new Date(T).toISOString(), index: 1, chunks: 1, data: b64(JSON.stringify(META)) }, ...bad, { index: 10, chunks: 1, data: b64(truncatedGzip) }],
    });
    const page = await mapReplayPage(res);
    expect(page.events).toEqual([META]);
    expect(page.unreadable).toBe(5);
    expect(page.resumeToken).toBe("");
  });

  it("asks for a byte-bounded page with no window and the token it was given", async () => {
    client.getSessionReplay.mockResolvedValue(
      create(GetSessionReplayResponseSchema, {
        events: [{ time: ts(T), index: 1, chunks: 1, data: new TextEncoder().encode(JSON.stringify(META)) }],
        resumeToken: "r1",
      })
    );
    const page = await getSessionReplay({ ...SCOPE, sessionId: "sess-1", maxBytes: 4 * 1024 * 1024, pageToken: "r0" });
    const [req] = client.getSessionReplay.mock.calls[0];
    expect(req).toMatchObject({ sessionId: "sess-1", maxBytes: 4 * 1024 * 1024, pageToken: "r0" });
    expect(req.scope.timeRange).toBeUndefined();
    expect(page.events).toEqual([META]);
    expect(page.resumeToken).toBe("r1");
  });
});
