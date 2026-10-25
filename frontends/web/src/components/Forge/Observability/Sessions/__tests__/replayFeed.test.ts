// Copyright (c) 2025 Reliant Labs

/**
 * The replay feed against a scripted GetSessionReplay: it buffers only as far
 * ahead of the playhead as it is asked to, follows a live session with
 * resume_token (keeping the old token through an empty poll, giving up after
 * enough of them), and stops on a failed page until retry — from that page.
 */

import { describe, expect, it, vi } from "vitest";

import type { ReplayPage, RrwebEvent } from "@/services/controlPlane/observability/rumTypes";

import { ReplayFeed, type FeedTimers, type ReplayFeedState } from "../replayFeed";

const T = Date.UTC(2026, 9, 24, 12);

/** `count` events, one a second from `fromSec`; the first page's first event is a full snapshot. */
function events(fromSec: number, count: number): RrwebEvent[] {
  return Array.from({ length: count }, (_, i) => ({ type: fromSec === 0 && i === 0 ? 2 : 3, timestamp: T + (fromSec + i) * 1000 }));
}

/** Ten pages of ten seconds each, paged by index token. */
function pagedSource() {
  const pages = Array.from({ length: 10 }, (_, p) => events(p * 10, 10));
  const fetchPage = vi.fn(async (token: string | undefined): Promise<ReplayPage> => {
    const p = token ? Number(token) : 0;
    return { events: pages[p], unreadable: 0, nextPageToken: p + 1 < pages.length ? String(p + 1) : "", resumeToken: `r${p}` };
  });
  return { fetchPage };
}

function manualTimers() {
  const queue: { fn: () => void; ms: number }[] = [];
  const timers: FeedTimers = {
    set: (fn, ms) => {
      const entry = { fn, ms };
      queue.push(entry);
      return entry;
    },
    clear: (handle) => {
      const i = queue.indexOf(handle as (typeof queue)[number]);
      if (i >= 0) queue.splice(i, 1);
    },
  };
  return { timers, queue, fire: () => queue.shift()?.fn() };
}

const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

describe("buffering ahead", () => {
  it("reads page one, then only as far ahead of the playhead as aheadMs", async () => {
    const { fetchPage } = pagedSource();
    const delivered: RrwebEvent[] = [];
    const states: ReplayFeedState[] = [];
    const feed = new ReplayFeed({ fetchPage, onEvents: (e) => delivered.push(...e), onChange: (s) => states.push(s), live: false, aheadMs: 25_000 });
    feed.start();
    await vi.waitFor(() => expect(fetchPage).toHaveBeenCalledTimes(3));
    await settle();
    // 0–9s, 10–19s, 20–29s: the buffer ends 29s past the first event, beyond 25s ahead.
    expect(fetchPage.mock.calls.map(([token]) => token)).toEqual([undefined, "1", "2"]);
    expect(delivered).toHaveLength(30);
    expect(feed.snapshot).toMatchObject({ phase: "streaming", hasMore: true, firstMs: T, lastMs: T + 29_000, hasFullSnapshot: true });

    // The reader moves on: the feed reads until it is 25s ahead of them again.
    feed.setPlayhead(T + 20_000);
    await vi.waitFor(() => expect(fetchPage).toHaveBeenCalledTimes(5));
    await settle();
    expect(fetchPage).toHaveBeenCalledTimes(5);

    // A seek far past the buffer reads all the way there.
    feed.setPlayhead(T + 95_000);
    await vi.waitFor(() => expect(feed.snapshot.phase).toBe("complete"));
    expect(fetchPage).toHaveBeenCalledTimes(10);
    expect(delivered.map((e) => e.timestamp)).toEqual(Array.from({ length: 100 }, (_, i) => T + i * 1000));
    expect(states[0].phase).toBe("loading");
  });
});

describe("following a live session", () => {
  it("polls with resume_token, keeps it through an empty poll, and stops after idlePolls", async () => {
    const { timers, queue, fire } = manualTimers();
    const replies: ReplayPage[] = [
      { events: events(0, 3), unreadable: 0, nextPageToken: "", resumeToken: "after-3" },
      // An event whose last chunk is still in flight is withheld: nothing yet.
      { events: [], unreadable: 0, nextPageToken: "", resumeToken: "" },
      { events: events(3, 2), unreadable: 0, nextPageToken: "", resumeToken: "after-5" },
      { events: [], unreadable: 0, nextPageToken: "", resumeToken: "" },
      { events: [], unreadable: 0, nextPageToken: "", resumeToken: "" },
    ];
    const fetchPage = vi.fn(async () => replies.shift()!);
    const delivered: RrwebEvent[] = [];
    const feed = new ReplayFeed({ fetchPage, onEvents: (e) => delivered.push(...e), onChange: () => {}, live: true, pollMs: 5000, idlePolls: 2, timers });
    feed.start();
    await vi.waitFor(() => expect(feed.snapshot.phase).toBe("live"));
    expect(queue).toHaveLength(1);
    expect(queue[0].ms).toBe(5000);

    fire();
    await vi.waitFor(() => expect(fetchPage).toHaveBeenCalledTimes(2));
    await settle();
    fire();
    await vi.waitFor(() => expect(fetchPage).toHaveBeenCalledTimes(3));
    await settle();
    // The empty poll kept the token it had; the next one carried it.
    expect(fetchPage.mock.calls.map(([token]) => token)).toEqual([undefined, "after-3", "after-3"]);
    expect(delivered).toHaveLength(5);

    fire();
    await vi.waitFor(() => expect(fetchPage).toHaveBeenCalledTimes(4));
    await settle();
    expect(feed.snapshot.phase).toBe("live");
    fire();
    await vi.waitFor(() => expect(feed.snapshot.phase).toBe("complete"));
    expect(fetchPage.mock.calls.at(-1)?.[0]).toBe("after-5");
    expect(queue).toHaveLength(0);
  });

  it("stops polling when stopped", async () => {
    const { timers, queue } = manualTimers();
    const fetchPage = vi.fn(async (): Promise<ReplayPage> => ({ events: events(0, 2), unreadable: 0, nextPageToken: "", resumeToken: "r" }));
    const feed = new ReplayFeed({ fetchPage, onEvents: () => {}, onChange: () => {}, live: true, timers });
    feed.start();
    await vi.waitFor(() => expect(queue).toHaveLength(1));
    feed.stop();
    expect(queue).toHaveLength(0);
  });
});

describe("a failed page", () => {
  it("stops where it is and resumes from that page on retry", async () => {
    let fail = true;
    const fetchPage = vi.fn(async (token: string | undefined): Promise<ReplayPage> => {
      if (token === "2" && fail) throw new Error("network");
      const p = token ? Number(token) : 0;
      return { events: events(p * 10, 10), unreadable: p === 0 ? 1 : 0, nextPageToken: p < 2 ? String(p + 1) : "", resumeToken: `r${p}` };
    });
    const delivered: RrwebEvent[] = [];
    const feed = new ReplayFeed({ fetchPage, onEvents: (e) => delivered.push(...e), onChange: () => {}, live: false });
    feed.start();
    await vi.waitFor(() => expect(feed.snapshot.phase).toBe("error"));
    expect(delivered).toHaveLength(20);
    expect(feed.snapshot.unreadable).toBe(1);
    fail = false;
    feed.retry();
    await vi.waitFor(() => expect(feed.snapshot.phase).toBe("complete"));
    expect(fetchPage.mock.calls.map(([token]) => token)).toEqual([undefined, "1", "2", "2"]);
    expect(delivered).toHaveLength(30);
  });
});
