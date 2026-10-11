import { create } from "@bufbuild/protobuf";
import { grpcClient } from "../api/grpc-client";
import { ListDaemonsRequestSchema } from "../gen/reliant/v1/daemon_registry_pb";
import type { DaemonInfo } from "../gen/reliant/v1/daemon_registry_pb";

/**
 * THE daemon list. Every reader of ListDaemons — useDaemonStatus,
 * useDaemonList, useDaemonWait, the project picker's no-machine panel — shares
 * this one cache entry, so a screen full of readers costs one request, not one
 * per hook, and the gateway's `daemons` push (invalidateDaemonList) reaches
 * every one of them.
 */
export const DAEMON_LIST_QUERY_KEY = ["reliant", "daemonRegistry", "list"] as const;

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
