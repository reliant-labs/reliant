import { useCallback, useEffect } from "react";
import { useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { DaemonStatus } from "../gen/reliant/v1/daemon_registry_pb";
import { logger } from "../lib/logger";
import {
  cachedDaemonList,
  daemonListQuery,
  EMPTY_DAEMON_LIST,
  invalidateDaemonList,
} from "./daemon-registry-queries";

/**
 * Safety net, not the freshness mechanism.
 *
 * The list is kept current by push: the gateway announces every attach and
 * detach, and every control-plane lifecycle transition it applies, as a
 * `daemons` refetch on the user stream (see globalUpdatesStore), and the
 * desktop app reports its own daemon connecting. A 5s poll used to do this job
 * and cost ~12 requests a minute per open window to learn, almost always,
 * nothing. What is left for the poll is whatever slips past every signal — an
 * event lost while the stream was down for less than a reconnect — and a
 * minute bounds that without spending a request every few seconds.
 *
 * One timer per query, however many readers are mounted: React Query re-arms
 * every observer's interval when the query fetches, so they share a phase.
 */
export const DAEMON_LIST_FALLBACK_POLL_MS = 60_000;

/**
 * How often, at most, a heartbeat that contradicts the cached list re-reads it
 * (per daemon). See reconcileDaemonHeartbeat.
 */
export const HEARTBEAT_CONTRADICTION_REFETCH_MS = 30_000;
const lastContradictionRead = new Map<string, number>();

/**
 * A heartbeat from a daemon the cached list does not show as connected means
 * the list is stale: only the gateway holding the daemon's stream publishes
 * one, and it renews the registry lease first. So re-read it — the UI then
 * recovers by itself, without a reload, once the registry knows the truth.
 *
 * The cache can disagree because an announcement was lost while the user
 * stream was down, or because the registry lost the lease while the stream
 * lived (prod 2026-10-11: a serving machine read DISCONNECTED for hours).
 * Throttled per daemon, so a registry that keeps disagreeing costs one read
 * per window rather than one per 15s heartbeat. A list nobody has loaded is
 * left alone: its first reader's fetch answers.
 */
export function reconcileDaemonHeartbeat(queryClient: QueryClient, daemonId: string, now = Date.now()): void {
  const list = cachedDaemonList(queryClient);
  if (!list) return;
  const daemon = list.find((d) => d.daemonId === daemonId);
  if (daemon && (daemon.status === DaemonStatus.ACTIVE || daemon.status === DaemonStatus.IDLE)) return;
  const last = lastContradictionRead.get(daemonId);
  if (last !== undefined && now - last < HEARTBEAT_CONTRADICTION_REFETCH_MS) return;
  lastContradictionRead.set(daemonId, now);
  invalidateDaemonList(queryClient);
}

/** Forget the per-daemon throttle. Exported for tests. */
export function resetDaemonHeartbeatReconcile(): void {
  lastContradictionRead.clear();
}

/**
 * Tracks the user's daemon list (the registry's ListDaemons).
 *
 * Eight-plus components mount this hook simultaneously (ModernApp,
 * NewChatView, TabbedViewerPanel, DaemonStatusDot, AddProjectDialog,
 * ConnectDaemonModal, WorkspacesSection, ComputeStep). Previously each
 * instance ran its OWN `setInterval(check, 5000)`, multiplying ListDaemons
 * RPC traffic by the number of mounted consumers — the user observed 4x
 * fan-out per cycle in practice.
 *
 * The implementation is now the single shared daemon-list entry
 * (daemon-registry-queries), refreshed by push and backstopped by a slow poll —
 * see DAEMON_LIST_FALLBACK_POLL_MS.
 */
export function useDaemonStatus() {
  const queryClient = useQueryClient();

  // Refetch the moment the desktop app reports its daemon connected, rather
  // than waiting for the gateway's announcement or the fallback poll.
  //
  // The poll alone was not sufficient after sign-in even at 5s: it sets
  // `refetchIntervalInBackground: false`, and OAuth backgrounds the window by
  // design when consent goes to the system browser. Measured on a real prod
  // sign-in, the daemon connected ~1.2s after the restart while the UI sat on
  // the onboarding daemon step for roughly a minute, because the poll had
  // stopped and nothing woke it.
  //
  // The event is a trigger, not data — see electron/src/preload.js's
  // onDaemonConnected. Registration (which makes the daemon listable) was
  // measured 1.1s BEFORE the connected event, so we refetch rather than
  // synthesising a daemon from the payload.
  useEffect(() => {
    const api = (window as unknown as {
      electronAPI?: {
        onDaemonConnected?: (cb: (p: unknown) => void) => () => void;
        isDaemonConnected?: () => Promise<boolean>;
      };
    }).electronAPI;
    // Ask once on mount, because the event may already have fired.
    //
    // The renderer RELOADS after the post-sign-in daemon restart, and the
    // main-process watcher de-duplicates on the stream value — so a renderer
    // that mounts after "connected" was published receives no event at all.
    // Measured: the daemon was listable at 22:20:11.2 while the UI only
    // learned at 22:20:15.3, on the next poll tick, with zero events
    // delivered. Asking removes the dependence on having been listening.
    void (async () => {
      try {
        if (!api?.isDaemonConnected) return;
        if (await api.isDaemonConnected()) {
          invalidateDaemonList(queryClient);
        }
      } catch {
        // A failed probe just falls back to the event + poll.
      }
    })();

    if (!api?.onDaemonConnected) return;
    return api.onDaemonConnected(() => {
      logger.warn("[DaemonStatus] daemon-connected event -> invalidating", {
        atMs: Date.now(),
      });
      invalidateDaemonList(queryClient);
    });
  }, [queryClient]);

  const { data, dataUpdatedAt, isError, failureCount } = useQuery(daemonListQuery({
    refetchInterval: DAEMON_LIST_FALLBACK_POLL_MS,
    // A hidden window has no one reading the list; focus catches it up.
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: "always",
    // Push is what keeps this fresh, and an invalidation marks the entry stale
    // explicitly, so cached data is trustworthy until then. Matching staleTime
    // to the fallback poll closes the mount storm: the dozen consumers listed
    // above mount at staggered moments as the user navigates, and with
    // staleTime 0 every one of them found the cache instantly stale and issued
    // its OWN request on mount.
    //
    // The paths that need an answer sooner are unaffected: refetchOnWindowFocus
    // "always" refetches irrespective of staleness, and `refresh()` and every
    // push signal invalidate.
    staleTime: DAEMON_LIST_FALLBACK_POLL_MS,
    placeholderData: EMPTY_DAEMON_LIST,
  }));

  const refresh = useCallback(() => {
    invalidateDaemonList(queryClient);
  }, [queryClient]);

  const daemons = data ?? [];
  const activeDaemon = daemons.find((d) => d.status === DaemonStatus.ACTIVE);
  // dataUpdatedAt is set only by a real answer; the placeholder never sets it.
  const answered = dataUpdatedAt > 0;
  return {
    daemons,
    activeDaemon,
    // The first read is in flight. NOT `isLoading`: the placeholder puts the
    // query in `success` before anything has been read, so isLoading was
    // false the whole time, and callers took the empty placeholder for "this
    // user has no machine" — a new chat defaulted to the one-way No machine
    // until the list arrived.
    loading: !answered && !isError,
    // Whether the registry has answered at all. A first read that FAILED is
    // neither loading nor an answer: nothing is known about the machines.
    answered,
    // The latest read failed (or is failing and being retried): `daemons` is
    // the last answer, not the machines' current state.
    unreachable: isError || failureCount > 0,
    refresh,
  };
}
