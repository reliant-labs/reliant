// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import type { TimelineEntry } from "@/services/controlPlane/observability/rumTypes";

import { currentEntryIndex, filterTimeline, formatOffset, isLive, LIVE_WINDOW_MS, rumSessionIdOf, sessionLink } from "../sessionModel";

const at = (timeMs: number, kind: TimelineEntry["kind"]) => ({ timeMs, kind }) as TimelineEntry;

describe("session model", () => {
  it("reads a playback offset as a clock", () => {
    expect(formatOffset(0)).toBe("0:00");
    expect(formatOffset(65_400)).toBe("1:05");
    expect(formatOffset(3_729_000)).toBe("1:02:09");
    expect(formatOffset(-5)).toBe("0:00");
  });

  it("treats a session active within the last two minutes as live", () => {
    const now = 1_000_000_000;
    expect(isLive({ endedMs: now - 10_000 }, now)).toBe(true);
    expect(isLive({ endedMs: now - LIVE_WINDOW_MS - 1 }, now)).toBe(false);
    expect(isLive({ endedMs: 0 }, now)).toBe(false);
  });

  it("marks the entry the playhead has reached, and filters by kind", () => {
    const entries = [at(10, "navigation"), at(20, "click"), at(30, "error"), at(40, "request_failed")];
    expect(currentEntryIndex(entries, null)).toBe(-1);
    expect(currentEntryIndex(entries, 5)).toBe(-1);
    expect(currentEntryIndex(entries, 25)).toBe(1);
    expect(currentEntryIndex(entries, 99)).toBe(3);
    expect(filterTimeline(entries, "errors").map((e) => e.kind)).toEqual(["error", "request_failed"]);
    expect(filterTimeline(entries, "all")).toHaveLength(4);
  });

  it("finds a trace's browser session on a span's attributes or resource", () => {
    expect(rumSessionIdOf([{ attributes: {}, resource: {} }])).toBeUndefined();
    expect(rumSessionIdOf([{ attributes: {}, resource: {} }, { attributes: {}, resource: { "rum.sessionId": "s1" } }])).toBe("s1");
    expect(rumSessionIdOf([{ attributes: { "rum.sessionId": "s2" }, resource: {} }])).toBe("s2");
  });

  it("links into a session, dropping the other view's keys", () => {
    expect(sessionLink("s1", 1234.6)).toMatchObject({ view: "sessions", session: "s1", t: 1235, trace: undefined, issue: undefined });
    expect(sessionLink("s1").t).toBeUndefined();
  });
});
