// Copyright (c) 2025 Reliant Labs

/**
 * The coalesced invalidator's contract: stale at once, a burst is one
 * refetch, never more than one refetch per interval or in flight, and the
 * final state is always fetched by a request that started after the last
 * signal.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryObserver } from "@tanstack/react-query";

import { createCoalescedInvalidator } from "../coalescedInvalidation";

const INTERVAL = 2_000;
const KEY = ["inbox"] as const;

/** A query under KEY whose fetches resolve when the test says so. */
function setup() {
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
  const invalidator = createCoalescedInvalidator(client, INTERVAL);
  // Let any fetch that is about to start start, answer every pending one,
  // and let what follows (a trailing refetch due now) start.
  const settle = async () => {
    await vi.advanceTimersByTimeAsync(0);
    while (resolvers.length) resolvers.shift()!();
    await vi.advanceTimersByTimeAsync(0);
  };
  return { client, queryFn, starts, invalidator, settle, unsubscribe };
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
});
