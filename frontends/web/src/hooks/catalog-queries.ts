// Copyright (c) 2025 Reliant Labs

/**
 * The integration catalog — CatalogService's SearchCatalog, GetCatalogEntry
 * and ListCatalogIntegrations — on the generated hooks and keys. It is
 * searched server-side, so the builder never ships the whole catalog to the
 * browser (research/INTEGRATIONS_V1_BRIEF.md §3a).
 *
 * The cache holds what the generated hooks fetch (the proto responses) under
 * `catalogServiceKeys`, so a hook here, the palette's prefetch and an
 * imperative `fetchCatalogEntry` share one entry, and invalidating
 * `catalogServiceKeys.all` (a connection changing `connected`) reaches every
 * one of them. Components read plain frontend types mapped from that entry,
 * so they never touch `$typeName` or proto enums.
 */

import { useMemo } from "react";
import { useQueries, type QueryClient } from "@tanstack/react-query";

import {
  CatalogEntryKind,
  type CatalogEntry as ProtoCatalogEntry,
  type CatalogEntrySummary as ProtoCatalogEntrySummary,
  type CatalogFacet as ProtoCatalogFacet,
  type CatalogIntegration as ProtoCatalogIntegration,
  type CatalogIntegrationListing as ProtoCatalogIntegrationListing,
  type GetCatalogEntryResponse,
  type SearchCatalogResponse,
} from "../gen/reliant/v1/catalog_pb";
import { ConnectionAuthKind } from "../gen/reliant/v1/connection_pb";
import { asJsonSchema, type JsonSchema } from "../lib/jsonSchema";
import { integrationEventNaming, RAW_EVENT_NAMING, type IntegrationEventNaming } from "../lib/integrationEventNames";
import {
  authKindFromProto,
  authMethodFromProto,
  connectionParamFromProto,
  type AuthKind,
  type AuthMethod,
  type ConnectionParam,
} from "./connection-queries";
import { catalogServiceQueries, useGetCatalogEntry, useSearchCatalog } from "./catalog-service-hooks_gen";

// ── Frontend types ──────────────────────────────────────────────────────────

export type CatalogKind = "action" | "trigger";

export interface CatalogIntegration {
  id: string;
  version: number;
  displayName: string;
  /** Icon hint, e.g. "github" or "globe". */
  icon: string;
  category: string;
}

/** One search result: enough to render a picker row. */
export interface CatalogEntrySummary {
  /** "github/issue.create@1" — what an action node's `uses` names. */
  ref: string;
  kind: CatalogKind;
  id: string;
  displayName: string;
  summary: string;
  integration: CatalogIntegration;
  authKinds: AuthKind[];
  connectionRequired: boolean;
  /** The caller can use it now: no connection needed, one exists, or a delegated authority serves it. */
  connected: boolean;
  /** The action changes external state. */
  mutates: boolean;
  /** A trigger's provider event types ("issues.opened"); empty for an action. */
  events: string[];
}

export interface CatalogEntry {
  summary: CatalogEntrySummary;
  description: string;
  paramsSchema?: JsonSchema;
  /** The params in the manifest's declared order (paramsSchema's keys arrive unordered). */
  paramOrder: string[];
  outputSchema?: JsonSchema;
  payloadSchema?: JsonSchema;
  connection: {
    required: boolean;
    methods: AuthMethod[];
    params: ConnectionParam[];
  };
  toolName: string;
}

export interface CatalogFacet {
  value: string;
  count: number;
}

/** One integration in a browse: enough to draw its row before it is expanded. */
export interface CatalogIntegrationListing {
  integration: CatalogIntegration;
  /** How many of its entries are of the browsed kinds. */
  entryCount: number;
  /** The caller can use it now (see CatalogEntrySummary.connected). */
  connected: boolean;
}

// ── Proto mapping ───────────────────────────────────────────────────────────

function kindFromProto(kind: CatalogEntryKind): CatalogKind {
  return kind === CatalogEntryKind.TRIGGER ? "trigger" : "action";
}

export function catalogKindToProto(kind: CatalogKind): CatalogEntryKind {
  return kind === "trigger" ? CatalogEntryKind.TRIGGER : CatalogEntryKind.ACTION;
}

function integrationFromProto(proto: ProtoCatalogIntegration | undefined): CatalogIntegration {
  return {
    id: proto?.id ?? "",
    version: proto?.version ?? 0,
    displayName: proto?.displayName || proto?.id || "",
    icon: proto?.icon ?? "",
    category: proto?.category ?? "",
  };
}

export function catalogSummaryFromProto(proto: ProtoCatalogEntrySummary): CatalogEntrySummary {
  return {
    ref: proto.ref,
    kind: kindFromProto(proto.kind),
    id: proto.id,
    displayName: proto.displayName || proto.id,
    summary: proto.summary,
    integration: integrationFromProto(proto.integration),
    authKinds: proto.authKinds
      .filter((kind) => kind !== ConnectionAuthKind.UNSPECIFIED)
      .map(authKindFromProto),
    connectionRequired: proto.connectionRequired,
    connected: proto.connected,
    mutates: proto.mutates,
    events: [...(proto.events ?? [])],
  };
}

export function catalogEntryFromProto(proto: ProtoCatalogEntry): CatalogEntry {
  if (!proto.summary) throw new Error("Catalog entry has no summary");
  return {
    summary: catalogSummaryFromProto(proto.summary),
    description: proto.description,
    paramsSchema: asJsonSchema(proto.paramsSchema),
    paramOrder: [...(proto.paramOrder ?? [])],
    outputSchema: asJsonSchema(proto.outputSchema),
    payloadSchema: asJsonSchema(proto.payloadSchema),
    connection: {
      required: proto.connection?.required ?? false,
      methods: (proto.connection?.methods ?? []).map(authMethodFromProto),
      params: (proto.connection?.connectionParams ?? []).map(connectionParamFromProto),
    },
    toolName: proto.toolName,
  };
}

export function catalogListingFromProto(proto: ProtoCatalogIntegrationListing): CatalogIntegrationListing {
  return {
    integration: integrationFromProto(proto.integration),
    entryCount: proto.entryCount,
    connected: proto.connected,
  };
}

export function catalogFacetFromProto(proto: ProtoCatalogFacet): CatalogFacet {
  return { value: proto.value, count: proto.count };
}

/** The integration id a ref names: "github/issue.create@1" → "github". */
export function refIntegration(ref: string): string {
  const slash = ref.indexOf("/");
  return slash > 0 ? ref.slice(0, slash) : "";
}

// One mapping per cached response: every reader of a cached entry gets the
// same CatalogEntry object, so it is safe in a dependency array.
const mappedEntries = new WeakMap<GetCatalogEntryResponse, CatalogEntry>();

/** The CatalogEntry a GetCatalogEntry answer carries, or undefined when it carries none. */
export function catalogEntryOf(response: GetCatalogEntryResponse | undefined): CatalogEntry | undefined {
  if (!response?.entry) return undefined;
  let mapped = mappedEntries.get(response);
  if (!mapped) {
    mapped = catalogEntryFromProto(response.entry);
    mappedEntries.set(response, mapped);
  }
  return mapped;
}

// ── Catalog entries ─────────────────────────────────────────────────────────

/** Entries change only on a deploy; five minutes keeps a builder session to one fetch each. */
export const CATALOG_ENTRY_STALE_MS = 5 * 60_000;

/**
 * The query for one entry, for useQueries, prefetchQuery and fetchQuery: the
 * generated useGetCatalogEntry's own options, with the catalog's stale time.
 */
export function catalogEntryQuery(ref: string) {
  return catalogServiceQueries.getCatalogEntry({ ref }, { staleTime: CATALOG_ENTRY_STALE_MS });
}

/** One catalog entry in full (schemas, connection methods). Shared with the palette's prefetch. */
export function useCatalogEntry(ref: string | undefined) {
  const query = useGetCatalogEntry({ ref: ref ?? "" }, { enabled: !!ref, staleTime: CATALOG_ENTRY_STALE_MS });
  return { ...query, data: catalogEntryOf(query.data) };
}

/** The entry already in the cache (the palette prefetches the highlighted row), without fetching. */
export function cachedCatalogEntry(queryClient: QueryClient, ref: string): CatalogEntry | undefined {
  return catalogEntryOf(queryClient.getQueryData(catalogEntryQuery(ref).queryKey));
}

/** The entry, from the cache when it is fresh, otherwise fetched into it. */
export async function fetchCatalogEntry(queryClient: QueryClient, ref: string): Promise<CatalogEntry> {
  const entry = catalogEntryOf(await queryClient.fetchQuery(catalogEntryQuery(ref)));
  if (!entry) throw new Error(`No catalog entry for ${ref}`);
  return entry;
}

/** Warm an entry so choosing it inserts defaults and opens its form without a wait. */
export function prefetchCatalogEntry(queryClient: QueryClient, ref: string): Promise<void> {
  return queryClient.prefetchQuery(catalogEntryQuery(ref));
}

/**
 * The output schema of each integration action, keyed by node id, from the
 * cached catalog entries (one request per distinct ref). Feeds CEL
 * completion of `nodes.<id>.data.*`.
 */
export function useActionOutputSchemas(refsByNode: ReadonlyArray<readonly [string, string]>): Record<string, JsonSchema> {
  const refs = [...new Set(refsByNode.map(([, ref]) => ref))];
  const results = useQueries({ queries: refs.map(catalogEntryQuery) });
  const byRef = new Map<string, JsonSchema | undefined>();
  refs.forEach((ref, i) => byRef.set(ref, catalogEntryOf(results[i]?.data)?.outputSchema));
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

// ── Trigger types ───────────────────────────────────────────────────────────

/**
 * One integration's trigger types (every integration's when empty): a handful
 * each, so one page of 100 holds them all.
 */
function triggerTypesRequest(integration: string) {
  return { query: "", kinds: [CatalogEntryKind.TRIGGER], integration, pageSize: 100 };
}

function triggerTypesOf(response: SearchCatalogResponse | undefined): CatalogEntrySummary[] | undefined {
  return response?.entries.map(catalogSummaryFromProto);
}

/**
 * Integration events by their catalog names — "GitHub: Issue opened", not
 * "github: issues.opened" — for every run and trigger that names one
 * (lib/integrationEventNames). One search of the trigger types, shared with
 * useDeclaredTriggerRef's cache; the recorded names until it loads.
 */
export function useIntegrationEventNaming(): IntegrationEventNaming {
  const search = useSearchCatalog(triggerTypesRequest(""), { staleTime: CATALOG_ENTRY_STALE_MS });
  return useMemo(() => {
    const entries = triggerTypesOf(search.data);
    return entries ? integrationEventNaming(entries) : RAW_EVENT_NAMING;
  }, [search.data]);
}

/**
 * The catalog trigger type a declared integration trigger was made from, for
 * its payload schema and event list. Declarations name an integration and
 * events, not a ref, so this searches that integration's trigger types (a
 * handful) and picks the one whose events cover the declaration's.
 */
export function useDeclaredTriggerRef(integration: string | undefined, events: readonly string[], knownRef?: string): string | undefined {
  return useDeclaredTriggerRefState(integration, events, knownRef).ref;
}

/**
 * useDeclaredTriggerRef, plus whether the answer is still on its way: `ref` is
 * undefined both while the catalog is being searched and when nothing in it
 * matches, and a caller that shows a message has to tell those apart.
 */
export function useDeclaredTriggerRefState(
  integration: string | undefined,
  events: readonly string[],
  knownRef?: string,
): { ref: string | undefined; loading: boolean } {
  const search = useSearchCatalog(triggerTypesRequest(integration ?? ""), {
    enabled: !!integration && !knownRef,
    staleTime: CATALOG_ENTRY_STALE_MS,
  });
  const candidates = useMemo(() => triggerTypesOf(search.data) ?? [], [search.data]);
  const entries = useQueries({
    queries: candidates.map((candidate) => ({
      ...catalogEntryQuery(candidate.ref),
      enabled: !knownRef && events.length > 0,
    })),
  });
  if (knownRef) return { ref: knownRef, loading: false };
  const loading = search.isLoading || (search.data !== undefined && entries.some((entry) => entry.isLoading));
  if (candidates.length === 1) return { ref: candidates[0]!.ref, loading: false };
  for (let i = 0; i < candidates.length; i += 1) {
    const offered = (catalogEntryOf(entries[i]?.data)?.payloadSchema?.properties?.event?.enum ?? []) as unknown[];
    if (events.length > 0 && events.every((event) => offered.includes(event) || offered.some((o) => event.endsWith(".*") && String(o).startsWith(event.slice(0, -1))))) {
      return { ref: candidates[i]!.ref, loading: false };
    }
  }
  return { ref: undefined, loading };
}
