// Copyright (c) 2025 Reliant Labs

/**
 * The shop pack's RumService: shop-web's real-user sessions, their timelines
 * and masked replays — shop's story (model/sessions.ts) encoded on the wire,
 * so the console's real client (services/controlPlane/observability/rumApi.ts)
 * decodes it. A replay event travels as control-plane stores it, one rrweb
 * event of JSON per ReplayEvent (uncompressed; rumApi decodes either).
 *
 * A relative window (the generated reads' "the last hour") is resolved against
 * `clock`, the pack's by default, and the list answers the absolute range
 * it read (effective_time_range), which a client sends back to page it.
 */

import { mockService, type MockHandlers } from "@reliantlabs/forge-web-runtime/mock-transport";

import { RumService, SessionTimelineKind as WireKind, type RumScope as WireRumScope } from "@/gen/controlplane/services/rum/v1/rum_pb";
import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import type { RumScope, SessionSummary, TimelineKind } from "@/services/controlPlane/observability/rumTypes";

import { createShopApi } from "../model/shopApi";
import { demoNow } from "../shopStory";
import { completed } from "./defaults";
import { ts, windowOf, int64, type PackClock } from "./wire";

const WIRE_KIND: Record<TimelineKind, WireKind> = {
  navigation: WireKind.NAVIGATION,
  click: WireKind.CLICK,
  request_failed: WireKind.REQUEST_FAILED,
  error: WireKind.ERROR,
  event: WireKind.EVENT,
};

function modelScope(scope: WireRumScope | undefined): RumScope {
  return { orgId: scope?.orgId ?? "", projectId: scope?.projectId, envIds: [...(scope?.envIds ?? [])], services: [...(scope?.services ?? [])] };
}

const wireSummary = (s: SessionSummary) => ({
  ...s,
  startedAt: ts(s.startedMs),
  endedAt: ts(s.endedMs),
  pageViews: int64(s.pageViews),
  interactions: int64(s.interactions),
  errors: int64(s.errors),
  client: { ...s.client },
});

const utf8 = new TextEncoder();

export function rumHandlers(fixtures: ObservabilityApi = createShopApi(), { clock = demoNow }: { clock?: PackClock } = {}): MockHandlers {
  return completed(RumService, mockService(RumService, {
    listSessions: async (req) => {
      const window = windowOf(req.scope?.timeRange, clock) ?? { startMs: 0, endMs: 0 };
      const res = await fixtures.listSessions({
        ...modelScope(req.scope),
        window,
        hasError: req.hasError,
        hasReplay: req.hasReplay,
        userId: req.userId,
        pathContains: req.pathContains,
        browser: req.browser,
        os: req.os,
        frontendVersion: req.frontendVersion,
        pageSize: req.pageSize,
        pageToken: req.pageToken || undefined,
      });
      return {
        sessions: res.sessions.map(wireSummary),
        nextPageToken: res.nextPageToken,
        effectiveTimeRange: { start: ts(window.startMs), end: ts(window.endMs) },
      };
    },
    getSession: async (req) => {
      const session = await fixtures.getSession({ ...modelScope(req.scope), sessionId: req.sessionId });
      return { session: wireSummary(session) };
    },
    getSessionTimeline: async (req) => {
      const res = await fixtures.getSessionTimeline({ ...modelScope(req.scope), sessionId: req.sessionId, pageSize: req.pageSize, pageToken: req.pageToken || undefined });
      return {
        entries: res.entries.map((e) => ({ ...e, time: ts(e.timeMs), kind: e.kind ? WIRE_KIND[e.kind] : WireKind.UNSPECIFIED, props: { ...e.props } })),
        nextPageToken: res.nextPageToken,
      };
    },
    getSessionReplay: async (req) => {
      const page = await fixtures.getSessionReplay({ ...modelScope(req.scope), sessionId: req.sessionId, maxBytes: req.maxBytes || undefined, pageToken: req.pageToken || undefined });
      const events = page.events.map((e) => ({ time: ts(e.timestamp), index: Number(e.index), chunks: 1, data: utf8.encode(JSON.stringify(e)) }));
      // An event the model could not read stays one the client cannot read either.
      for (let i = 0; i < page.unreadable; i++) events.push({ time: ts(0), index: events.length, chunks: 1, data: new Uint8Array([0]) });
      return { events, nextPageToken: page.nextPageToken, resumeToken: page.resumeToken };
    },
  }));
}
