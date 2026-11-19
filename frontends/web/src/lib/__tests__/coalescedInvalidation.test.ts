// @vitest-environment node
// Copyright (c) 2025 Reliant Labs

/**
 * The coalesced invalidator's contract: stale at once, a burst is one
 * refetch, never more than one refetch per interval or in flight, and the
 * final state is always fetched by a request that started after the last
 * signal.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryObserver, focusManager } from "@tanstack/react-query";

import { createCoalescedInvalidator, type CoalescedInvalidatorOptions, type PageVisibility } from "../coalescedInvalidation";

const INTERVAL = 2_000;
const KEY = ["inbox"] as const;

/** A page whose visibility the test sets. */
function fakeVisibility() {
  let hidden = false;
  const listeners: Array<() => void> = [];
  const visibility: PageVisibility = {
    hidden: () => hidden,
    onChange: (listener) => listeners.push(listener),
  };
  const set = (next: "hidden" | "visible") => {
    hidden = next === "hidden";
    for (const listener of listeners) listener();
  };
  return { visibility, set };
}

/** A query under KEY whose fetches resolve when the test says so. */
function setup(options?: CoalescedInvalidatorOptions) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  const starts: number[] = [];
  const resolvers: Array<() => void> = [];
  let version = 0;
  const queryFn = vi.fn(
    () =>
      new Promise<number>((resolve) => {
        starts.push(Date.now());
        const v = ++version;
        resolvers.push(() => resolve(v));
      }),
  );
  // An observer makes the query active, as a mounted list would.
  const observer = new QueryObserver(client, { queryKey: [...KEY, "list"], queryFn });
  const unsubscribe = observer.subscribe(() => {});
  const invalidator = createCoalescedInvalidator(client, INTERVAL, options);
  // Let any fetch that is about to start start, answer every pending one,
  // and let what follows (a trailing refetch due now) start.
  const settle = async () => {
    await vi.advanceTimersByTimeAsync(0);
    while (resolvers.length) resolvers.shift()!();
    await vi.advanceTimersByTimeAsync(0);
  };
  return { client, queryFn, starts, resolvers, invalidator, settle, unsubscribe };
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(0);
});

afterEach(() => {
  vi.useRealTimers();
});

describe("createCoalescedInvalidator", () => {
  it("marks the query stale synchronously and refetches a burst once, on the leading edge", async () => {
    const { client, queryFn, invalidator, settle } = setup();
    await settle(); // the observer's initial fetch
    expect(queryFn).toHaveBeenCalledTimes(1);

    for (let i = 0; i < 50; i++) invalidator.invalidate(KEY);
    expect(client.getQueryState([...KEY, "list"])?.isInvalidated).toBe(true);
    expect(queryFn).toHaveBeenCalledTimes(1); // nothing yet: same tick

    await vi.advanceTimersByTimeAsync(0);
    expect(queryFn).toHaveBeenCalledTimes(2);
    await settle();
    await vi.advanceTimersByTimeAsync(INTERVAL * 5);
    // The burst all arrived before the refetch started: no trailing fetch.
    expect(queryFn).toHaveBeenCalledTimes(2);
  });

  it("never refetches more than once per interval, and fetches the final state", async () => {
    const { queryFn, starts, invalidator, settle } = setup();
    await settle();
    const before = queryFn.mock.calls.length;

    // A signal every 100 ms for 10 s; each fetch answers at once.
    for (let t = 0; t < 10_000; t += 100) {
      invalidator.invalidate(KEY);
      await vi.advanceTimersByTimeAsync(100);
      await settle();
    }
    const lastSignalAt = Date.now() - 100;
    await vi.advanceTimersByTimeAsync(INTERVAL);
    await settle();

    const refetches = starts.slice(before);
    // 10 s at one per 2 s, plus the leading edge.
    expect(refetches.length).toBeLessThanOrEqual(6);
    expect(refetches.length).toBeGreaterThanOrEqual(5);
    for (let i = 1; i < refetches.length; i++) {
      expect(refetches[i]! - refetches[i - 1]!).toBeGreaterThanOrEqual(INTERVAL);
    }
    // Trailing edge: the last refetch started after the last signal.
    expect(refetches.at(-1)!).toBeGreaterThanOrEqual(lastSignalAt);
  });

  it("waits for a slow refetch to settle before starting the next one", async () => {
    const { queryFn, invalidator, settle } = setup();
    await settle();

    invalidator.invalidate(KEY);
    await vi.advanceTimersByTimeAsync(0);
    expect(queryFn).toHaveBeenCalledTimes(2); // in flight, unresolved

    // Signals keep coming while the server takes 7 s to answer.
    for (let i = 0; i < 7; i++) {
      invalidator.invalidate(KEY);
      await vi.advanceTimersByTimeAsync(1_000);
    }
    expect(queryFn).toHaveBeenCalledTimes(2); // no overlapping request

    await settle(); // the slow one answers; the interval has long passed
    expect(queryFn).toHaveBeenCalledTimes(3); // exactly one trailing refetch
    await settle();
    await vi.advanceTimersByTimeAsync(INTERVAL * 3);
    expect(queryFn).toHaveBeenCalledTimes(3);
  });

  it("only marks an unobserved query stale", async () => {
    const { client, queryFn, invalidator, settle, unsubscribe } = setup();
    await settle();
    unsubscribe();

    invalidator.invalidate(KEY);
    await vi.advanceTimersByTimeAsync(INTERVAL);
    expect(queryFn).toHaveBeenCalledTimes(1);
    expect(client.getQueryState([...KEY, "list"])?.isInvalidated).toBe(true);
  });

  it("keeps separate budgets per key", async () => {
    const client = new QueryClient();
    const spy = vi.spyOn(client, "invalidateQueries");
    const invalidator = createCoalescedInvalidator(client, INTERVAL);
    invalidator.invalidate(["inbox"]);
    invalidator.invalidate(["runs", "list"]);
    await vi.advanceTimersByTimeAsync(0);
    const refetched = spy.mock.calls
      .filter(([filters]) => filters?.refetchType === undefined)
      .map(([filters]) => filters?.queryKey);
    expect(refetched).toEqual([["inbox"], ["runs", "list"]]);
  });

  it("a predicate narrows the refetch, and a collapsed burst covers the union", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
    const fns = ["a", "b", "c"].map((tag) => {
      const queryFn = vi.fn(async () => tag);
      new QueryObserver(client, { queryKey: [...KEY, tag], queryFn }).subscribe(() => {});
      return queryFn;
    });
    await vi.advanceTimersByTimeAsync(0);
    const invalidator = createCoalescedInvalidator(client, INTERVAL);
    const tagIs = (tag: string) => (query: { queryKey: readonly unknown[] }) => query.queryKey[1] === tag;

    invalidator.invalidate(KEY, tagIs("a"));
    invalidator.invalidate(KEY, tagIs("b"));
    expect(client.getQueryState([...KEY, "c"])?.isInvalidated).toBe(false);
    await vi.advanceTimersByTimeAsync(0);
    expect(fns.map((fn) => fn.mock.calls.length)).toEqual([2, 2, 1]);

    // A signal without a predicate joins as "everything under the key".
    await vi.advanceTimersByTimeAsync(INTERVAL);
    invalidator.invalidate(KEY, tagIs("a"));
    invalidator.invalidate(KEY);
    await vi.advanceTimersByTimeAsync(0);
    expect(fns.map((fn) => fn.mock.calls.length)).toEqual([3, 3, 2]);
  });

  it("reset drops a pending trailing refetch", async () => {
    const { queryFn, invalidator, settle } = setup();
    await settle();
    invalidator.invalidate(KEY);
    await vi.advanceTimersByTimeAsync(0);
    await settle();
    invalidator.invalidate(KEY); // trailing, due in INTERVAL
    invalidator.reset();
    await vi.advanceTimersByTimeAsync(INTERVAL * 2);
    expect(queryFn).toHaveBeenCalledTimes(2);
  });

  describe("a hidden page", () => {
    it("marks stale but refetches nothing while hidden, then reads what went stale once when shown", async () => {
      const page = fakeVisibility();
      const { client, queryFn, invalidator, settle } = setup({ visibility: page.visibility });
      await settle();
      expect(queryFn).toHaveBeenCalledTimes(1);

      page.set("hidden");
      // A busy stream for a minute in a background tab.
      for (let t = 0; t < 60_000; t += 500) {
        invalidator.invalidate(KEY);
        await vi.advanceTimersByTimeAsync(500);
        await settle();
      }
      expect(queryFn).toHaveBeenCalledTimes(1);
      // A list mounted meanwhile would still fetch: the data is stale.
      expect(client.getQueryState([...KEY, "list"])?.isInvalidated).toBe(true);

      page.set("visible");
      await settle();
      expect(queryFn).toHaveBeenCalledTimes(2);
      await vi.advanceTimersByTimeAsync(INTERVAL * 5);
      await settle();
      expect(queryFn).toHaveBeenCalledTimes(2);
    });

    it("joins the window-focus refetch the same visibility change started, instead of sending a second read", async () => {
      const page = fakeVisibility();
      const { client, queryFn, invalidator, settle } = setup({ visibility: page.visibility });
      client.mount(); // React Query's own refetch on focus
      try {
        await settle();
        page.set("hidden");
        invalidator.invalidate(KEY);
        await settle();
        expect(queryFn).toHaveBeenCalledTimes(1);

        // The browser fires one visibilitychange; React Query's focus
        // listener refetches the stale query, and so does the catch-up.
        focusManager.setFocused(false);
        page.set("visible");
        focusManager.setFocused(true);
        await vi.advanceTimersByTimeAsync(0);
        expect(queryFn).toHaveBeenCalledTimes(2);
        await settle();
        await vi.advanceTimersByTimeAsync(INTERVAL * 3);
        expect(queryFn).toHaveBeenCalledTimes(2);
      } finally {
        client.unmount();
        focusManager.setFocused(undefined);
      }
    });

    it("refetches in full for a signal that arrives after the page is shown", async () => {
      const page = fakeVisibility();
      const { queryFn, invalidator, settle } = setup({ visibility: page.visibility });
      await settle();
      page.set("hidden");
      invalidator.invalidate(KEY);
      await settle();
      page.set("visible");
      await settle();
      expect(queryFn).toHaveBeenCalledTimes(2);

      invalidator.invalidate(KEY);
      await vi.advanceTimersByTimeAsync(INTERVAL);
      await settle();
      expect(queryFn).toHaveBeenCalledTimes(3);
    });

    it("holds the trailing refetch of a read that was in flight when the page was hidden", async () => {
      const page = fakeVisibility();
      const { queryFn, invalidator, settle, resolvers } = setup({ visibility: page.visibility });
      await settle();
      invalidator.invalidate(KEY);
      await vi.advanceTimersByTimeAsync(0);
      expect(queryFn).toHaveBeenCalledTimes(2); // in flight

      page.set("hidden");
      invalidator.invalidate(KEY);
      while (resolvers.length) resolvers.shift()!();
      await vi.advanceTimersByTimeAsync(INTERVAL * 10);
      expect(queryFn).toHaveBeenCalledTimes(2);

      page.set("visible");
      await settle();
      expect(queryFn).toHaveBeenCalledTimes(3);
    });
  });

  describe("latencyMultiple", () => {
    it("spaces refetches of a slow list by that multiple of its last duration", async () => {
      const { queryFn, starts, invalidator, resolvers } = setup({ latencyMultiple: 2 });
      await vi.advanceTimersByTimeAsync(0);
      resolvers.shift()!();
      await vi.advanceTimersByTimeAsync(0);
      const before = queryFn.mock.calls.length;

      // The server takes 3 s per read; the stream signals every 100 ms.
      const SERVER_MS = 3_000;
      for (let t = 0; t < 30_000; t += 100) {
        invalidator.invalidate(KEY);
        await vi.advanceTimersByTimeAsync(100);
        if (resolvers.length && Date.now() - starts.at(-1)! >= SERVER_MS) {
          resolvers.shift()!();
          await vi.advanceTimersByTimeAsync(0);
        }
      }

      const refetches = starts.slice(before);
      // Every 2 × 3 s = 6 s over 30 s, not back to back (which is every 3 s).
      expect(refetches.length).toBeGreaterThanOrEqual(4);
      expect(refetches.length).toBeLessThanOrEqual(6);
      for (let i = 1; i < refetches.length; i++) {
        expect(refetches[i]! - refetches[i - 1]!).toBeGreaterThanOrEqual(2 * SERVER_MS);
      }
    });

    it("keeps the interval as the floor for a fast list", async () => {
      const { queryFn, starts, invalidator, settle } = setup({ latencyMultiple: 2 });
      await settle();
      const before = queryFn.mock.calls.length;
      for (let t = 0; t < 10_000; t += 100) {
        invalidator.invalidate(KEY);
        await vi.advanceTimersByTimeAsync(100);
        await settle();
      }
      const refetches = starts.slice(before);
      expect(refetches.length).toBeGreaterThanOrEqual(5);
      for (let i = 1; i < refetches.length; i++) {
        expect(refetches[i]! - refetches[i - 1]!).toBeGreaterThanOrEqual(INTERVAL);
        expect(refetches[i]! - refetches[i - 1]!).toBeLessThan(INTERVAL + 200);
      }
    });

    it("never stretches past maxIntervalMs", async () => {
      const { starts, invalidator, resolvers } = setup({ latencyMultiple: 2, maxIntervalMs: 5_000 });
      await vi.advanceTimersByTimeAsync(0);
      resolvers.shift()!();
      await vi.advanceTimersByTimeAsync(0);

      invalidator.invalidate(KEY);
      await vi.advanceTimersByTimeAsync(0);
      const slowStart = starts.at(-1)!;
      // A 4 s read: 2 × 4 s would hold the next one until 8 s after it started.
      await vi.advanceTimersByTimeAsync(4_000);
      resolvers.shift()!();
      await vi.advanceTimersByTimeAsync(0);

      invalidator.invalidate(KEY);
      await vi.advanceTimersByTimeAsync(900);
      expect(starts.at(-1)!).toBe(slowStart);
      await vi.advanceTimersByTimeAsync(200);
      expect(starts.at(-1)! - slowStart).toBe(5_000);
    });
  });
});
