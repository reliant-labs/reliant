// Copyright (c) 2025 Reliant Labs

/**
 * THE daemon list: DaemonRegistryService.ListDaemons({}) on forge's generated
 * keys, one cache entry for every reader.
 *
 * useDaemonStatus, useDaemonList, useDaemonWait, the onboarding gate, the
 * Machines page (list and detail), the connect modal and the onboarding
 * commits all read this one entry, so a screen full of readers costs one
 * request, and one refetch reaches every one of them. It used to sit under five
 * hand-written keys for the same RPC, which meant a refetch one reader caused
 * (a restart's 2s poll, a push) left the others showing an older answer.
 *
 * The cache holds the proto response; readers map it with `selectDaemons`.
 * The stream only INVALIDATES this cache — the gateway's `daemons` /
 * `daemon_local_models` refetch, heartbeat contradiction, the desktop app's
 * daemon-connected event and the reconnect sweep all go through
 * `invalidateDaemonList` below — and every generated
 * DaemonRegistryService write invalidates `daemonRegistryServiceKeys.all`
 * itself. Nothing patches it in place.
 */

import { create } from "@bufbuild/protobuf";
import type { Query, QueryClient } from "@tanstack/react-query";
import type { ServiceQueryInput } from "@reliantlabs/forge-web-runtime/service-hooks";

import {
  ListDaemonsResponseSchema,
  type DaemonInfo,
  type ListDaemonsResponse,
} from "../gen/reliant/v1/daemon_registry_pb";
import { daemonRegistryServiceKeys, daemonRegistryServiceQueries } from "./daemon-registry-service-hooks_gen";

/** ListDaemons takes no filter: the caller's machines, all of them. */
const LIST_DAEMONS_REQUEST = {} as const;

/** The response a reader sees before the registry has answered. */
export const EMPTY_DAEMON_LIST: ListDaemonsResponse = create(ListDaemonsResponseSchema);

/** Hoisted, so React Query re-runs the mapping only when the response changes. */
export function selectDaemons(response: ListDaemonsResponse): DaemonInfo[] {
  return response.daemons;
}

/**
 * The daemon list's query options, mapped to `DaemonInfo[]`. Every reader's
 * cadence (`refetchInterval`, `staleTime`, `enabled`) is its own; the key and
 * the fetch are the generated ones.
 */
export function daemonListQuery(options: ServiceQueryInput<ListDaemonsResponse, DaemonInfo[]> = {}) {
  return daemonRegistryServiceQueries.listDaemons(LIST_DAEMONS_REQUEST, { select: selectDaemons, ...options });
}

/** The daemon list's cache key (data-tagged as the response). */
export const DAEMON_LIST_QUERY_KEY = daemonRegistryServiceQueries.listDaemons(LIST_DAEMONS_REQUEST).queryKey;

/** What the cache holds now, without fetching. Undefined until a read answered. */
export function cachedDaemonList(queryClient: QueryClient): DaemonInfo[] | undefined {
  return queryClient.getQueryData(DAEMON_LIST_QUERY_KEY)?.daemons;
}

/**
 * Read the list now, through the shared entry — for code that asks a question
 * of the registry at one moment (a commit deciding whether to create or resume
 * a machine, a restart waiting for a pod to stop). Always a fresh read (an
 * in-flight one is joined), and its answer refreshes every mounted reader.
 *
 * Failures THROW. React Query keeps the last successful result on error, so a
 * transient RPC failure leaves the UI on the last-known daemon state rather
 * than flipping every consumer to "no machine".
 */
export async function fetchDaemonList(queryClient: QueryClient): Promise<DaemonInfo[]> {
  const response = await queryClient.fetchQuery(daemonRegistryServiceQueries.listDaemons(LIST_DAEMONS_REQUEST, { staleTime: 0 }));
  return response.daemons;
}

/**
 * Mark the daemon list (every DaemonRegistryService read) stale; observed
 * readers refetch at once. One entry serves them all, the onboarding gate
 * included, so one invalidation reaches every one.
 */
export function invalidateDaemonList(
  queryClient: QueryClient,
  options: { predicate?: (query: Query) => boolean } = {},
): void {
  const { predicate } = options;
  void queryClient.invalidateQueries({ queryKey: daemonRegistryServiceKeys.all, predicate });
}
