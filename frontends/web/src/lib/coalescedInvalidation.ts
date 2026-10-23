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
 */

import { hashKey, type QueryClient, type QueryKey } from "@tanstack/react-query";

export interface CoalescedInvalidator {
  /** Mark `queryKey` (a prefix) stale now and refetch it soon. */
  invalidate(queryKey: QueryKey): void;
  /** Drop every pending refetch. For tests and teardown. */
  reset(): void;
}

interface KeyState {
  queryKey: QueryKey;
  /** A signal arrived that no started refetch has seen yet. */
  dirty: boolean;
  timer: ReturnType<typeof setTimeout> | null;
  inFlight: boolean;
  /** When the last refetch started; -Infinity before the first. */
  lastStartedAt: number;
}

export function createCoalescedInvalidator(
  client: QueryClient,
  intervalMs: number,
  now: () => number = () => Date.now(),
): CoalescedInvalidator {
  const states = new Map<string, KeyState>();

  const schedule = (state: KeyState) => {
    const wait = Math.max(0, state.lastStartedAt + intervalMs - now());
    state.timer = setTimeout(() => void run(state), wait);
  };

  const run = async (state: KeyState) => {
    state.timer = null;
    if (!state.dirty) return;
    state.dirty = false;
    state.inFlight = true;
    state.lastStartedAt = now();
    try {
      // Marks stale again (a fetch that STARTED before the latest signal may
      // have cleared the flag on success) and refetches the observed queries.
      await client.invalidateQueries({ queryKey: state.queryKey });
    } catch {
      // A failed refetch is the query's own error state; nothing to do here.
    } finally {
      state.inFlight = false;
      if (state.dirty && states.get(hashKey(state.queryKey)) === state) schedule(state);
    }
  };

  return {
    invalidate(queryKey) {
      void client.invalidateQueries({ queryKey, refetchType: "none" });
      const hash = hashKey(queryKey);
      let state = states.get(hash);
      if (!state) {
        state = { queryKey, dirty: false, timer: null, inFlight: false, lastStartedAt: -Infinity };
        states.set(hash, state);
      }
      state.dirty = true;
      if (state.timer !== null || state.inFlight) return;
      schedule(state);
    },
    reset() {
      for (const state of states.values()) if (state.timer !== null) clearTimeout(state.timer);
      states.clear();
    },
  };
}
