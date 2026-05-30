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
  try {
    const resp = await grpcClient
      .daemonRegistry()
      .listDaemons(create(ListDaemonsRequestSchema));
    return resp.daemons;
  } catch {
    return [];
  }
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
    // Daemons rarely flap; the 5s poll is what keeps the UI fresh.
    staleTime: 0,
    placeholderData: [],
  });

  const refresh = useCallback(() => {
    void queryClient.invalidateQueries({ queryKey: DAEMON_LIST_QUERY_KEY });
  }, [queryClient]);

  const daemons = data ?? [];
  const activeDaemon = daemons.find((d) => d.status === DaemonStatus.ACTIVE);
  return { daemons, activeDaemon, loading: isLoading, refresh };
}
