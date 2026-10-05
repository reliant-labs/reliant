// Copyright (c) 2025 Reliant Labs

import { useMemo } from "react";
import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";

import { connectionGrpc, type Connection } from "../api/connection-grpc";
import { catalogSearchGrpc } from "../api/catalog-search-grpc";
import type { JsonSchema } from "../lib/jsonSchema";

export const connectionKeys = {
  all: ["connections"] as const,
  list: (integrationId: string) => [...connectionKeys.all, "list", integrationId] as const,
  catalogEntry: (ref: string) => ["catalogEntry", ref] as const,
};

/** The caller's connections to one integration. */
export function useConnections(integrationId: string | undefined) {
  return useQuery({
    queryKey: connectionKeys.list(integrationId ?? ""),
    queryFn: () => connectionGrpc.list(integrationId),
    enabled: !!integrationId,
    staleTime: 30_000,
  });
}

/** One catalog entry in full (schemas, connection methods). Shared with the palette's prefetch. */
export function useCatalogEntry(ref: string | undefined) {
  return useQuery({
    queryKey: connectionKeys.catalogEntry(ref ?? ""),
    queryFn: () => catalogSearchGrpc.get(ref!),
    enabled: !!ref,
    staleTime: 5 * 60_000,
  });
}

export function useCreateApiKeyConnection() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: connectionGrpc.createApiKey,
    onSuccess: (connection: Connection) => {
      void queryClient.invalidateQueries({ queryKey: connectionKeys.all });
      // A new connection changes `connected` on every search result for it.
      void queryClient.invalidateQueries({ queryKey: ["catalogSearch"] });
      void queryClient.invalidateQueries({ queryKey: ["catalogEntry"] });
      return connection;
    },
  });
}

/**
 * The output schema of each integration action, keyed by node id, from the
 * cached catalog entries (one request per distinct ref). Feeds CEL
 * completion of `nodes.<id>.data.*`.
 */
export function useActionOutputSchemas(refsByNode: ReadonlyArray<readonly [string, string]>): Record<string, JsonSchema> {
  const refs = [...new Set(refsByNode.map(([, ref]) => ref))];
  const results = useQueries({
    queries: refs.map((ref) => ({
      queryKey: connectionKeys.catalogEntry(ref),
      queryFn: () => catalogSearchGrpc.get(ref),
      staleTime: 5 * 60_000,
    })),
  });
  const byRef = new Map<string, JsonSchema | undefined>();
  refs.forEach((ref, i) => byRef.set(ref, results[i]?.data?.outputSchema));
  const signature = refsByNode.map(([id, ref]) => `${id}=${ref}:${byRef.get(ref) ? 1 : 0}`).join("|");
  return useMemo(() => {
    const out: Record<string, JsonSchema> = {};
    for (const [id, ref] of refsByNode) {
      const schema = byRef.get(ref);
      if (schema) out[id] = schema;
    }
    return out;
    // eslint-disable-next-line react-hooks/exhaustive-deps -- recomputed when a schema arrives or a ref moves
  }, [signature]);
}

/**
 * The catalog trigger type a declared integration trigger was made from, for
 * its payload schema and event list. Declarations name an integration and
 * events, not a ref, so this searches that integration's trigger types (a
 * handful) and picks the one whose events cover the declaration's.
 */
export function useDeclaredTriggerRef(integration: string | undefined, events: readonly string[], knownRef?: string): string | undefined {
  const search = useQuery({
    queryKey: ["catalogSearch", "trigger-types", integration ?? ""],
    queryFn: () => catalogSearchGrpc.search({ query: "", kinds: ["trigger"], integration, pageSize: 100 }),
    enabled: !!integration && !knownRef,
    staleTime: 5 * 60_000,
  });
  const candidates = search.data?.entries ?? [];
  const entries = useQueries({
    queries: candidates.map((candidate) => ({
      queryKey: connectionKeys.catalogEntry(candidate.ref),
      queryFn: () => catalogSearchGrpc.get(candidate.ref),
      staleTime: 5 * 60_000,
      enabled: !knownRef && events.length > 0,
    })),
  });
  if (knownRef) return knownRef;
  if (candidates.length === 1) return candidates[0]!.ref;
  for (let i = 0; i < candidates.length; i += 1) {
    const offered = (entries[i]?.data?.payloadSchema?.properties?.event?.enum ?? []) as unknown[];
    if (events.length > 0 && events.every((event) => offered.includes(event) || offered.some((o) => event.endsWith(".*") && String(o).startsWith(event.slice(0, -1))))) {
      return candidates[i]!.ref;
    }
  }
  return undefined;
}
