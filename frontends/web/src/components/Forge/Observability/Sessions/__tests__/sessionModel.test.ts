// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import type { TimelineEntry } from "@/services/controlPlane/observability/rumTypes";

import { currentEntryIndex, filterTimeline, formatOffset, formatSessionWhen, isLive, LIVE_WINDOW_MS, rumSessionIdOf, sessionLink, sessionTitle } from "../sessionModel";

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

describe("a session's name", () => {
  // 2025-10-25 16:00 UTC: a Saturday afternoon in London (BST, UTC+1), the same morning in Los Angeles (PDT, UTC-7).
  const now = Date.UTC(2025, 9, 25, 16, 0);
  const en = { now, locale: "en-US" };

  it("is the frontend and when it started, never the id", () => {
    expect(sessionTitle("reliant-web", Date.UTC(2025, 9, 25, 13, 32), { ...en, timeZone: "UTC" })).toBe("reliant-web · Today 13:32");
  });

  it("says Today and Yesterday by the viewer's calendar, then the date", () => {
    const utc = { ...en, timeZone: "UTC" };
    expect(formatSessionWhen(Date.UTC(2025, 9, 25, 0, 5), utc)).toBe("Today 00:05");
    expect(formatSessionWhen(Date.UTC(2025, 9, 24, 23, 59), utc)).toBe("Yesterday 23:59");
    expect(formatSessionWhen(Date.UTC(2025, 9, 24, 9, 10), utc)).toBe("Yesterday 09:10");
    expect(formatSessionWhen(Date.UTC(2025, 9, 23, 14, 32), utc)).toBe("Oct 23, 14:32");
    expect(formatSessionWhen(Date.UTC(2024, 9, 25, 14, 32), utc)).toBe("Oct 25, 2024, 14:32");
  });

  it("judges the day and the clock in the viewer's timezone", () => {
    // 23:30 UTC on the 24th is already the 25th in London and still the 24th in Los Angeles.
    const late = Date.UTC(2025, 9, 24, 23, 30);
    expect(formatSessionWhen(late, { ...en, timeZone: "Europe/London" })).toBe("Today 00:30");
    expect(formatSessionWhen(late, { ...en, timeZone: "America/Los_Angeles" })).toBe("Yesterday 16:30");
    // ...and in Tokyo `now` is already 01:00 on the 26th, so the 25th's morning is yesterday.
    expect(formatSessionWhen(late, { ...en, timeZone: "Asia/Tokyo" })).toBe("Yesterday 08:30");
  });

  it("counts calendar days, not 24-hour spans, across a DST change", () => {
    // London enters BST at 01:00 UTC on 2025-03-30, so the 30th is 23 hours long:
    // 00:10 on the 30th is yesterday at 00:30 on the 31st, though more than 24 hours earlier.
    const opts = { now: Date.UTC(2025, 2, 30, 23, 30), locale: "en-US", timeZone: "Europe/London" };
    expect(formatSessionWhen(Date.UTC(2025, 2, 30, 0, 10), opts)).toBe("Yesterday 00:10");
    expect(formatSessionWhen(Date.UTC(2025, 2, 29, 23, 50), opts)).toBe("Mar 29, 23:50");
  });

  it("falls back gracefully when the frontend or the start is unknown", () => {
    const utc = { ...en, timeZone: "UTC" };
    expect(sessionTitle("", Date.UTC(2025, 9, 25, 13, 32), utc)).toBe("Unknown app · Today 13:32");
    expect(sessionTitle(undefined, Date.UTC(2025, 9, 25, 13, 32), utc)).toBe("Unknown app · Today 13:32");
    expect(sessionTitle("  ", 0, utc)).toBe("Unknown app · unknown time");
    expect(sessionTitle("shop-web", Number.NaN, utc)).toBe("shop-web · unknown time");
  });
});
