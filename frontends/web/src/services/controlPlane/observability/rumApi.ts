// Copyright (c) 2025 Reliant Labs

/**
 * control-plane `controlplane.v1.RumService`, mapped into the console's model
 * (./rumTypes.ts). The only file that knows its wire shape.
 *
 * Every call carries the scope: the org is checked server-side (membership),
 * and a session read names a session id that is looked up INSIDE the scope —
 * a session of another environment or service is NotFound.
 *
 * ── REPLAY BYTES ───────────────────────────────────────────────────────────
 *
 * ReplayEvent.data is the rrweb event's JSON exactly as recorded: opaque,
 * masked at record time, never altered by the server. This transport speaks
 * Connect-JSON, where a `bytes` field travels as base64; protobuf-es decodes
 * it back to a Uint8Array, so by the time a response reaches mapReplayPage the
 * base64 is gone. base64 costs a third more on the wire, which the HTTP layer
 * wins back: the browser always sends Accept-Encoding: gzip (a header script
 * cannot set or drop), connect-go compresses a response for a client that
 * accepts it, and fetch inflates it transparently. A page is up to 16 MiB of
 * events, so that matters; nothing here has to ask for it.
 *
 * An event whose stored bytes are themselves gzip (a recorder configured to
 * pack its events) is inflated here before it is parsed. Anything that still
 * is not an rrweb event is skipped and COUNTED (ReplayPage.unreadable), never
 * guessed at: the player says the recording has gaps rather than rendering
 * something the recorder did not send.
 */

import type { Transport } from "@connectrpc/connect";
import type { MessageInitShape } from "@bufbuild/protobuf";
import { base64Decode } from "@bufbuild/protobuf/wire";

import {
  RumService,
  SessionTimelineKind as WireKind,
  type GetSessionReplayRequestSchema,
  type GetSessionReplayResponse,
  type GetSessionRequestSchema,
  type GetSessionResponse,
  type GetSessionTimelineRequestSchema,
  type GetSessionTimelineResponse,
  type ListSessionsRequestSchema,
  type ListSessionsResponse,
  type RumTimeRange,
  type SessionSummary as WireSummary,
  type SessionTimelineEntry as WireEntry,
} from "@/gen/controlplane/services/rum/v1/rum_pb";

import { getControlPlaneClient } from "../client";
import { obsCall } from "./obsInterceptors";
import { msOf, wireTimeRange, type ObsWindow, type WithObsWindow } from "./api";
import { countOf } from "./issuesApi";
import type {
  ListSessionsRequest,
  ReplayPage,
  ReplayRequest,
  RrwebEvent,
  RumScope,
  SessionList,
  SessionRequest,
  SessionSummary,
  Timeline,
  TimelineEntry,
  TimelineKind,
  TimelineRequest,
} from "./rumTypes";
import { mapClient } from "./client";

function client(transport?: Transport) {
  return getControlPlaneClient(RumService, transport);
}

/**
 * The wire scope. A session read sends no window, so the server finds the
 * session itself. A list's window is absolute, or relative to the server's
 * clock (RumTimeRange.relative), which it resolves when it answers.
 */
export function wireRumScope(scope: RumScope, window?: ObsWindow) {
  return {
    orgId: scope.orgId,
    projectId: scope.projectId || undefined,
    envIds: scope.envIds,
    services: scope.services ?? [],
    timeRange: window ? wireTimeRange(window) : undefined,
  };
}

/** A ListSessionsRequest whose window may be relative: what the generated read sends. */
export type SessionsQuery = WithObsWindow<ListSessionsRequest>;

/**
 * `range`, when given, is sent instead of the window: the absolute range an
 * earlier page read (its effective_time_range), which is how a client pages a
 * relative window so every page reads the same range.
 */
export function listSessionsRequest(req: SessionsQuery, range?: RumTimeRange): MessageInitShape<typeof ListSessionsRequestSchema> {
  return {
    scope: range ? { ...wireRumScope(req), timeRange: range } : wireRumScope(req, req.window),
    hasError: req.hasError,
    hasReplay: req.hasReplay,
    userId: req.userId,
    pathContains: req.pathContains,
    browser: req.browser || undefined,
    os: req.os || undefined,
    frontendVersion: req.frontendVersion || undefined,
    pageSize: req.pageSize ?? 0,
    pageToken: req.pageToken ?? "",
  };
}

export function toSessionList(res: ListSessionsResponse): SessionList {
  return { sessions: res.sessions.map(mapSessionSummary), nextPageToken: res.nextPageToken };
}

export function sessionRequest(req: SessionRequest): MessageInitShape<typeof GetSessionRequestSchema> {
  return { scope: wireRumScope(req), sessionId: req.sessionId };
}

export function toSession(res: GetSessionResponse): SessionSummary {
  if (!res.session) throw new Error("GetSession answered without a session");
  return mapSessionSummary(res.session);
}

export function timelineRequest(req: TimelineRequest): MessageInitShape<typeof GetSessionTimelineRequestSchema> {
  return { scope: wireRumScope(req), sessionId: req.sessionId, pageSize: req.pageSize ?? 0, pageToken: req.pageToken ?? "" };
}

export function toTimeline(res: GetSessionTimelineResponse): Timeline {
  return { entries: res.entries.map(mapTimelineEntry), nextPageToken: res.nextPageToken };
}

export function replayRequest(req: ReplayRequest): MessageInitShape<typeof GetSessionReplayRequestSchema> {
  return { scope: wireRumScope(req), sessionId: req.sessionId, maxBytes: req.maxBytes ?? 0, pageToken: req.pageToken ?? "" };
}

export function mapSessionSummary(s: WireSummary): SessionSummary {
  return {
    sessionId: s.sessionId,
    service: s.service,
    serviceVersion: s.serviceVersion,
    envId: s.envId,
    envName: s.envName,
    userId: s.userId,
    entryPath: s.entryPath,
    startedMs: msOf(s.startedAt),
    endedMs: msOf(s.endedAt),
    pageViews: countOf(s.pageViews),
    interactions: countOf(s.interactions),
    errors: countOf(s.errors),
    hasReplay: s.hasReplay,
    country: s.country,
    region: s.region,
    city: s.city,
    clientAddress: s.clientAddress,
    client: mapClient(s.client),
    release: s.release,
  };
}

const KIND_OF: Record<WireKind, TimelineKind | null> = {
  [WireKind.UNSPECIFIED]: null,
  [WireKind.NAVIGATION]: "navigation",
  [WireKind.CLICK]: "click",
  [WireKind.REQUEST_FAILED]: "request_failed",
  [WireKind.ERROR]: "error",
  [WireKind.EVENT]: "event",
};

export function mapTimelineEntry(e: WireEntry): TimelineEntry {
  return {
    timeMs: msOf(e.time),
    kind: KIND_OF[e.kind] ?? null,
    name: e.name,
    path: e.path,
    method: e.method,
    status: e.status,
    selector: e.selector,
    element: e.element,
    errorType: e.errorType,
    message: e.message,
    fingerprint: e.fingerprint,
    issueId: e.issueId,
    traceId: e.traceId,
    spanId: e.spanId,
    durationMs: e.durationMs,
    props: { ...e.props },
    service: e.service,
  };
}

// ── Replay event bytes ───────────────────────────────────────────────────────

function isGzip(bytes: Uint8Array): boolean {
  return bytes.length > 2 && bytes[0] === 0x1f && bytes[1] === 0x8b;
}

async function gunzip(bytes: Uint8Array): Promise<Uint8Array> {
  // BufferSource is what DecompressionStream's writable side takes; decoded
  // protobuf bytes are never SharedArrayBuffer-backed, the case it refuses.
  const stream = new ReadableStream<BufferSource>({
    start(controller) {
      controller.enqueue(bytes as Uint8Array<ArrayBuffer>);
      controller.close();
    },
  }).pipeThrough(new DecompressionStream("gzip"));
  const reader = stream.getReader();
  const parts: Uint8Array[] = [];
  let size = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    parts.push(value);
    size += value.length;
  }
  const out = new Uint8Array(size);
  let at = 0;
  for (const part of parts) {
    out.set(part, at);
    at += part.length;
  }
  return out;
}

const utf8 = new TextDecoder("utf-8", { fatal: true });

/**
 * One stored event's bytes → the rrweb event, or null when it is not one.
 * Accepts the base64 text too, for a caller holding the raw protojson.
 */
export async function decodeReplayData(data: Uint8Array | string): Promise<RrwebEvent | null> {
  try {
    let bytes = typeof data === "string" ? base64Decode(data) : data;
    if (isGzip(bytes)) {
      if (typeof DecompressionStream === "undefined") return null;
      bytes = await gunzip(bytes);
    }
    const parsed: unknown = JSON.parse(utf8.decode(bytes));
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return null;
    const event = parsed as Record<string, unknown>;
    if (typeof event.type !== "number" || typeof event.timestamp !== "number") return null;
    return event as RrwebEvent;
  } catch {
    return null;
  }
}

export async function mapReplayPage(res: GetSessionReplayResponse): Promise<ReplayPage> {
  const decoded = await Promise.all(res.events.map((e) => decodeReplayData(e.data)));
  const events = decoded.filter((e): e is RrwebEvent => e !== null);
  return { events, unreadable: decoded.length - events.length, nextPageToken: res.nextPageToken, resumeToken: res.resumeToken };
}

// ── RPCs ─────────────────────────────────────────────────────────────────────

/** One page of the sessions that started in the window, newest first. */
export async function listSessions(req: ListSessionsRequest, options: { signal?: AbortSignal } = {}, transport?: Transport): Promise<SessionList> {
  return toSessionList(await client(transport).listSessions(listSessionsRequest(req), obsCall(options)));
}

/** One session's summary. NotFound when the scope cannot see it. */
export async function getSession(req: SessionRequest, options: { signal?: AbortSignal } = {}, transport?: Transport): Promise<SessionSummary> {
  return toSession(await client(transport).getSession(sessionRequest(req), obsCall(options)));
}

/** One page of a session's timeline, oldest first. */
export async function getSessionTimeline(req: TimelineRequest, options: { signal?: AbortSignal } = {}, transport?: Transport): Promise<Timeline> {
  return toTimeline(await client(transport).getSessionTimeline(timelineRequest(req), obsCall(options)));
}

/** One byte-bounded page of a session's rrweb events, parsed. */
export async function getSessionReplay(req: ReplayRequest, options: { signal?: AbortSignal } = {}, transport?: Transport): Promise<ReplayPage> {
  return mapReplayPage(await client(transport).getSessionReplay(replayRequest(req), obsCall(options)));
}
