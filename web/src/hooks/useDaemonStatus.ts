import { useCallback, useEffect } from "react";
import { useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { create } from "@bufbuild/protobuf";
import { grpcClient } from "../api/grpc-client";
import { DaemonStatus, ListDaemonsRequestSchema } from "../gen/reliant/v1/daemon_registry_pb";
import type { DaemonInfo } from "../gen/reliant/v1/daemon_registry_pb";
import { logger } from "../lib/logger";

/**
 * THE daemon list. Every reader of ListDaemons — this hook, useDaemonList, the
 * project picker's no-machine panel — shares this one cache entry, so a screen
 * full of readers costs one request, not one per hook.
 */
export const DAEMON_LIST_QUERY_KEY = ["reliant", "daemonRegistry", "list"] as const;

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
 * Mark every daemon-list cache stale; observed ones refetch at once.
 *
 * Also covers the onboarding gate's attempt-scoped list
 * (["onboarding", "daemons", "gate", …]), which waits on a daemon appearing
 * and has to hear about it the moment it does.
 */
export function invalidateDaemonList(queryClient: QueryClient): void {
  void queryClient.invalidateQueries({ queryKey: DAEMON_LIST_QUERY_KEY });
  void queryClient.invalidateQueries({ queryKey: ["onboarding", "daemons"] });
}

/**
 * Tracks the user's daemon list (the registry's ListDaemons).
 *
 * Eight-plus components mount this hook simultaneously (ModernApp,
 * NewChatView, TabbedViewerPanel, DaemonStatusDot, ProjectPicker,
 * ConnectDaemonModal, WorkspacesSection, ComputeStep). Previously each
 * instance ran its OWN `setInterval(check, 5000)`, multiplying ListDaemons
 * RPC traffic by the number of mounted consumers — the user observed 4x
 * fan-out per cycle in practice.
 *
 * The implementation is now a single shared React Query keyed by
 * DAEMON_LIST_QUERY_KEY, refreshed by push and backstopped by a slow poll —
 * see DAEMON_LIST_FALLBACK_POLL_MS.
 */
export async function fetchDaemonList(): Promise<DaemonInfo[]> {
  // Let failures THROW. React Query keeps the last successful result on
  // error, so a transient RPC failure (auth-token refresh, proxy hiccup,
  // api-server restart) leaves the UI showing the last-known daemon state.
  // The old `catch { return [] }` resolved errors to an empty list, which
  // REPLACED the cache — one failed poll flipped every consumer to
  // "daemon disconnected" for at least a full poll cycle even though the
  // daemon was connected the whole time.
  const resp = await grpcClient
    .daemonRegistry()
    .listDaemons(create(ListDaemonsRequestSchema));
  return resp.daemons;
}

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
          void queryClient.invalidateQueries({ queryKey: DAEMON_LIST_QUERY_KEY });
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

  const { data, isLoading } = useQuery<DaemonInfo[]>({
    queryKey: DAEMON_LIST_QUERY_KEY,
    queryFn: fetchDaemonList,
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
    placeholderData: [],
  });

  const refresh = useCallback(() => {
    invalidateDaemonList(queryClient);
  }, [queryClient]);

  const daemons = data ?? [];
  const activeDaemon = daemons.find((d) => d.status === DaemonStatus.ACTIVE);
  return { daemons, activeDaemon, loading: isLoading, refresh };
}
