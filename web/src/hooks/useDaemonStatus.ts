import { useCallback, useEffect } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { create } from "@bufbuild/protobuf";
import { grpcClient } from "../api/grpc-client";
import { DaemonStatus, ListDaemonsRequestSchema } from "../gen/reliant/v1/daemon_registry_pb";
import type { DaemonInfo } from "../gen/reliant/v1/daemon_registry_pb";
import { logger } from "../lib/logger";
const DAEMON_LIST_QUERY_KEY = ["reliant", "daemonRegistry", "list"] as const;
const POLL_INTERVAL_MS = 5_000;

/**
 * Tracks the local Reliant daemon registry's daemon list.
 *
 * Eight-plus components mount this hook simultaneously (ModernApp,
 * NewChatView, TabbedViewerPanel, DaemonStatusDot, ProjectPicker,
 * ConnectDaemonModal, WorkspacesSection, ComputeStep). Previously each
 * instance ran its OWN `setInterval(check, 5000)`, multiplying ListDaemons
 * RPC traffic by the number of mounted consumers — the user observed 4x
 * fan-out per cycle in practice.
 *
 * The implementation is now a single shared React Query keyed by
 * `reliant.daemonRegistry.list`. TanStack dedupes concurrent fetches and
 * gates polling on at least one observer being mounted, so the network
 * traffic falls back to a single 5s tick regardless of how many components
 * consume the hook.
 */
async function fetchDaemonList(): Promise<DaemonInfo[]> {
  // Let failures THROW. React Query keeps the last successful result on
  // error, so a transient RPC failure (auth-token refresh, proxy hiccup,
  // api-server restart) leaves the UI showing the last-known daemon state.
  // The old `catch { return [] }` resolved errors to an empty list, which
  // REPLACED the cache — one failed poll flipped every consumer to
  // "daemon disconnected" for at least a full 5s poll cycle even though
  // the daemon was connected the whole time.
  const resp = await grpcClient
    .daemonRegistry()
    .listDaemons(create(ListDaemonsRequestSchema));
  logger.warn("[DaemonStatus] ListDaemons returned", {
    atMs: Date.now(),
    count: resp.daemons.length,
    statuses: resp.daemons.map((d) => d.status),
    ids: resp.daemons.map((d) => d.daemonId),
  });
  return resp.daemons;
}

export function useDaemonStatus() {
  const queryClient = useQueryClient();

  // Refetch the moment the desktop app reports its daemon connected, rather
  // than waiting for the next 5s poll.
  //
  // The poll alone is not sufficient after sign-in: it sets
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
          logger.warn("[DaemonStatus] daemon already connected on mount", {
            atMs: Date.now(),
          });
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
      void queryClient.invalidateQueries({ queryKey: DAEMON_LIST_QUERY_KEY });
    });
  }, [queryClient]);

  const { data, isLoading } = useQuery<DaemonInfo[]>({
    queryKey: DAEMON_LIST_QUERY_KEY,
    queryFn: fetchDaemonList,
    refetchInterval: POLL_INTERVAL_MS,
    // Stop polling when the document is hidden (matches the old impl's
    // visibilitychange listener) and resume on focus.
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: "always",
    // Daemons rarely flap; the 5s poll is what keeps the UI fresh, and it runs
    // on its own schedule regardless of staleness. Matching staleTime to the
    // poll interval closes the mount storm without widening that bound: the
    // dozen consumers listed above mount at staggered moments as the user
    // navigates, and with staleTime 0 every one of them found the cache
    // instantly stale and issued its OWN request on mount. The data can still
    // only ever be POLL_INTERVAL_MS old, because the poll is what bounds it.
    //
    // The paths that need an answer sooner than the next tick are unaffected:
    // refetchOnWindowFocus "always" refetches irrespective of staleness, and
    // `refresh()` below invalidates, which marks the entry stale explicitly.
    staleTime: POLL_INTERVAL_MS,
    placeholderData: [],
  });

  const refresh = useCallback(() => {
    void queryClient.invalidateQueries({ queryKey: DAEMON_LIST_QUERY_KEY });
  }, [queryClient]);

  const daemons = data ?? [];
  const activeDaemon = daemons.find((d) => d.status === DaemonStatus.ACTIVE);
  return { daemons, activeDaemon, loading: isLoading, refresh };
}
