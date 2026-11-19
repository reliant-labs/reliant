// Copyright (c) 2025 Reliant Labs

/**
 * Coalesced invalidation: mark a query stale on every signal, but refetch it
 * at most once per interval, and never while this invalidator's previous
 * refetch of it is still in flight.
 *
 * Why it exists: the user-update stream marks the Inbox and the Runs list
 * stale on every chat activity change. A busy run emits those several times a
 * second, and a reconnect replays dozens in one batch; each one used to
 * refetch every observed list at once (React Query cancels the in-flight
 * fetch client-side, but the server still runs it). On 2026-10-23 one client
 * sent 363 ListInbox and 754 ListRuns calls in a minute and filled the
 * api-server's database pool.
 *
 * The contract, per query key:
 *   - `invalidate(key)` marks the matching queries stale AT ONCE (so a list
 *     mounted later fetches fresh data), and asks for a refetch.
 *   - Leading edge: when nothing ran for `intervalMs`, the refetch starts on
 *     the next macrotask — a synchronous batch of updates is one refetch.
 *   - Trailing edge: signals that arrive while a refetch is pending or in
 *     flight collapse into ONE more refetch, started when the interval since
 *     the last start has passed AND the previous refetch has settled. So the
 *     final state is always fetched, by a request that started after the last
 *     signal.
 *   - Only observed queries refetch (React Query's default `refetchType:
 *     "active"`); an unmounted list is merely stale.
 *   - A signal may narrow itself with a `predicate` (the reconnect sweep
 *     names only the reads made before the outage). Signals that collapse
 *     into one refetch cover the union of what they named; one without a
 *     predicate covers everything under the key.
 *
 * Two opt-in refinements (CoalescedInvalidatorOptions), both for lists a busy
 * stream keeps marking stale:
 *   - `visibility`: a hidden page refetches nothing. Signals still mark the
 *     queries stale at once; the refetch waits until the page is shown again,
 *     and then reads what went stale once. React Query only knows "mounted",
 *     not "seen", so every background tab and minimised window used to refetch
 *     at the full rate (2026-11-18: one user's 3–4 tabs, each ~8 ListInbox a
 *     minute, most of them hidden).
 *   - `latencyMultiple`: the interval stretches with the list's own latency —
 *     the next refetch starts no sooner than `latencyMultiple` × the last
 *     refetch's duration after the last one started (never more than
 *     `maxIntervalMs`). A fixed 2 s interval on a read the server takes 1.5–3 s
 *     to answer is back-to-back refetching; this keeps a slow list idle at
 *     least as long as it was busy.
 */

import { hashKey, notifyManager, type Query, type QueryClient, type QueryKey } from "@tanstack/react-query";

export type QueryPredicate = (query: Query) => boolean;

/** Whether the page is hidden, and a way to hear when that changes. */
export interface PageVisibility {
  hidden(): boolean;
  onChange(listener: () => void): void;
}

/**
 * The document's visibility. A hidden tab, and in Electron a minimised or
 * hidden window (or, on macOS, a fully covered one), reports "hidden"; a
 * visible window without focus stays "visible" and keeps refetching. A context
 * with no document (a worker, SSR, node tests) is always visible.
 */
export const documentVisibility: PageVisibility = {
  hidden: () => typeof document !== "undefined" && document.visibilityState === "hidden",
  onChange: (listener) => {
    if (typeof document !== "undefined") document.addEventListener("visibilitychange", listener);
  },
};

/** The default ceiling for a latency-stretched interval. */
export const DEFAULT_MAX_INTERVAL_MS = 30_000;

export interface CoalescedInvalidatorOptions {
  now?: () => number;
  /** Pause refetches while the page is hidden (see the header). Default: always visible. */
  visibility?: PageVisibility;
  /**
   * Stretch the interval to this multiple of the last refetch's duration
   * (see the header). Default 0: the interval is fixed.
   */
  latencyMultiple?: number;
  /** The ceiling of a stretched interval. Default DEFAULT_MAX_INTERVAL_MS. */
  maxIntervalMs?: number;
}

export interface CoalescedInvalidator {
  /**
   * Mark `queryKey` (a prefix) stale now and refetch it soon — only the
   * queries under it that `predicate` accepts, when one is given.
   */
  invalidate(queryKey: QueryKey, predicate?: QueryPredicate): void;
  /** Drop every pending refetch. For tests and teardown. */
  reset(): void;
}

interface KeyState {
  queryKey: QueryKey;
  /** A signal arrived that no started refetch has seen yet. */
  dirty: boolean;
  /**
   * What the unseen signals named: "all" once any signal carried no
   * predicate, otherwise every predicate they carried.
   */
  pending: QueryPredicate[] | "all";
  timer: ReturnType<typeof setTimeout> | null;
  inFlight: boolean;
  /** When the last refetch started; -Infinity before the first. */
  lastStartedAt: number;
  /** How long the last refetch took to settle (0 before the first). */
  lastDurationMs: number;
  /** The page was hidden while the refetch in flight ran: its duration is not the server's. */
  hiddenDuringFlight: boolean;
  /** A refetch came due while the page was hidden; it waits for the page to be shown. */
  parked: boolean;
  /**
   * The page was just shown and every signal since the refetch was parked
   * arrived while hidden: refetch only what is still stale, joining a read
   * already in flight (the window-focus refetch the same visibilitychange
   * started) rather than aborting it.
   */
  catchUp: boolean;
}

function unionOf(pending: QueryPredicate[] | "all"): QueryPredicate | undefined {
  if (pending === "all") return undefined;
  if (pending.length === 1) return pending[0];
  return (query) => pending.some((predicate) => predicate(query));
}

export function createCoalescedInvalidator(
  client: QueryClient,
  intervalMs: number,
  options: CoalescedInvalidatorOptions = {},
): CoalescedInvalidator {
  const now = options.now ?? (() => Date.now());
  const { visibility, latencyMultiple = 0, maxIntervalMs = DEFAULT_MAX_INTERVAL_MS } = options;
  const states = new Map<string, KeyState>();

  const gapAfterStart = (state: KeyState) =>
    Math.max(intervalMs, Math.min(maxIntervalMs, latencyMultiple * state.lastDurationMs));

  const schedule = (state: KeyState) => {
    const wait = Math.max(0, state.lastStartedAt + gapAfterStart(state) - now());
    state.timer = setTimeout(() => void run(state), wait);
  };

  const run = async (state: KeyState) => {
    state.timer = null;
    if (!state.dirty) return;
    if (visibility?.hidden()) {
      // Stays dirty: the queries are already stale, and the refetch is made
      // when the page is shown (the visibility listener below).
      state.parked = true;
      return;
    }
    const catchUp = state.catchUp;
    state.catchUp = false;
    state.dirty = false;
    const predicate = unionOf(state.pending);
    state.pending = [];
    state.inFlight = true;
    state.hiddenDuringFlight = false;
    const startedAt = now();
    state.lastStartedAt = startedAt;
    try {
      if (catchUp) {
        // Every signal arrived before the page was shown, so a read that
        // started since (the focus refetch) already covers them: join it.
        await client.refetchQueries(
          {
            queryKey: state.queryKey,
            type: "active",
            predicate: (query) => query.state.isInvalidated && (!predicate || predicate(query)),
          },
          { cancelRefetch: false },
        );
      } else {
        // Marks stale again (a fetch that STARTED before the latest signal may
        // have cleared the flag on success) and refetches the observed queries.
        await client.invalidateQueries({ queryKey: state.queryKey, predicate });
      }
    } catch {
      // A failed refetch is the query's own error state; nothing to do here.
    } finally {
      // A page frozen mid-request measured its sleep, not the server.
      if (!state.hiddenDuringFlight) state.lastDurationMs = now() - startedAt;
      state.inFlight = false;
      if (state.dirty && states.get(hashKey(state.queryKey)) === state) schedule(state);
    }
  };

  // Marks the matching queries stale, synchronously: the refetch `run`
  // schedules must never be overtaken by stale-marking still in progress,
  // which would leave its fresh result stale and fetch again. This is what
  // invalidateQueries({ refetchType: "none" }) does before returning a
  // resolved promise, without leaning on that promise being already settled.
  const markStale = (queryKey: QueryKey, predicate?: QueryPredicate) => {
    notifyManager.batch(() => {
      for (const query of client.getQueryCache().findAll({ queryKey, predicate })) query.invalidate();
    });
  };

  visibility?.onChange(() => {
    const hidden = visibility.hidden();
    for (const state of states.values()) {
      if (hidden) {
        if (state.inFlight) state.hiddenDuringFlight = true;
        continue;
      }
      if (!state.parked) continue;
      state.parked = false;
      state.catchUp = true;
      // Stale again: a read that started before a hidden-time signal and
      // finished since cleared the flag without seeing it. The focus refetch
      // this same event starts then reads it too.
      markStale(state.queryKey, unionOf(state.pending));
      if (state.timer === null && !state.inFlight) schedule(state);
    }
  });

  return {
    invalidate(queryKey, predicate) {
      markStale(queryKey, predicate);
      const hash = hashKey(queryKey);
      let state = states.get(hash);
      if (!state) {
        state = {
          queryKey,
          dirty: false,
          pending: [],
          timer: null,
          inFlight: false,
          lastStartedAt: -Infinity,
          lastDurationMs: 0,
          hiddenDuringFlight: false,
          parked: false,
          catchUp: false,
        };
        states.set(hash, state);
      }
      state.dirty = true;
      if (!predicate) state.pending = "all";
      else if (state.pending !== "all") state.pending.push(predicate);
      // Hidden: the refetch is already waiting for the page to be shown.
      if (state.parked) return;
      // A signal after the page was shown may postdate a read in flight, so
      // the next refetch is a full one again.
      state.catchUp = false;
      if (state.timer !== null || state.inFlight) return;
      schedule(state);
    },
    reset() {
      for (const state of states.values()) if (state.timer !== null) clearTimeout(state.timer);
      states.clear();
    },
  };
}
