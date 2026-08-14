import { useCallback } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { create } from "@bufbuild/protobuf";
import { grpcClient } from "../api/grpc-client";
import { DaemonStatus, ListDaemonsRequestSchema } from "../gen/reliant/v1/daemon_registry_pb";
import type { DaemonInfo } from "../gen/reliant/v1/daemon_registry_pb";

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
  return resp.daemons;
}

export function useDaemonStatus() {
  const queryClient = useQueryClient();
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
