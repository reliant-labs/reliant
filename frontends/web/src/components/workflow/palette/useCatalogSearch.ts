// Copyright (c) 2025 Reliant Labs

/**
 * Debounced, paged catalog search for the step palette.
 *
 * One infinite query per (query, kind, category): typing changes the key,
 * React Query keeps the previous page on screen until the next one lands,
 * and an in-flight request for a stale key is aborted. Pages come from
 * `next_page_token`, so the browser only ever holds what was scrolled to.
 */

import { keepPreviousData, useInfiniteQuery } from "@tanstack/react-query";
import { useMemo } from "react";

import { CatalogService } from "../../../gen/reliant/v1/catalog_pb";
import { connectClient } from "../../../lib/connect";
import {
  catalogFacetFromProto,
  catalogKindToProto,
  catalogListingFromProto,
  catalogSummaryFromProto,
  type CatalogEntrySummary,
  type CatalogFacet,
  type CatalogIntegrationListing,
  type CatalogKind,
} from "../../../hooks/catalog-queries";
import { catalogServiceKeys } from "../../../hooks/catalog-service-hooks_gen";
import { useDebounce } from "../../../hooks/useDebounce";

const client = connectClient(CatalogService);

export const CATALOG_SEARCH_DEBOUNCE_MS = 150;
export const CATALOG_PAGE_SIZE = 20;

export interface CatalogSearchArgs {
  query: string;
  kind: CatalogKind;
  category?: string;
  /** Keep only this integration's entries: what expanding it in a browse lists. */
  integration?: string;
  /**
   * Fetch nothing until the debounced query is non-empty: the caller browses
   * some other way before anything is typed.
   */
  requireQuery?: boolean;
  enabled?: boolean;
}

export interface CatalogSearchState {
  entries: CatalogEntrySummary[];
  facets: CatalogFacet[];
  totalSize: number;
  /** The debounced query the entries answer. */
  settledQuery: string;
  isLoading: boolean;
  isFetching: boolean;
  isError: boolean;
  error: unknown;
  hasNextPage: boolean;
  isFetchingNextPage: boolean;
  fetchNextPage: () => void;
  refetch: () => void;
}

/**
 * The paged reads' keys: the generated key of the first page's request (no
 * page token), plus a suffix. An infinite query caches `{ pages }`, not one
 * response, so it must never share an entry with a plain useSearchCatalog of
 * the same request; it stays under catalogServiceKeys.all, so a connection
 * change still refetches it.
 */
export const catalogPageKeys = {
  search: (query: string, kind: CatalogKind, category: string, integration = "") =>
    [...catalogServiceKeys.searchCatalog(searchRequest(query, kind, category, integration)), "pages"] as const,
  integrations: (kind: CatalogKind, category: string, pageSize: number) =>
    [...catalogServiceKeys.listCatalogIntegrations(integrationsRequest(kind, category, pageSize)), "pages"] as const,
};

function searchRequest(query: string, kind: CatalogKind, category: string, integration: string) {
  return { query: query.slice(0, 256), kinds: [catalogKindToProto(kind)], category, integration, pageSize: CATALOG_PAGE_SIZE };
}

function integrationsRequest(kind: CatalogKind, category: string, pageSize: number) {
  return { kinds: [catalogKindToProto(kind)], category, pageSize };
}

export function useCatalogSearch({
  query,
  kind,
  category = "",
  integration = "",
  requireQuery = false,
  enabled = true,
}: CatalogSearchArgs): CatalogSearchState {
  const settledQuery = useDebounce(query.trim(), CATALOG_SEARCH_DEBOUNCE_MS);
  const active = enabled && (!requireQuery || settledQuery !== "");

  const result = useInfiniteQuery({
    queryKey: catalogPageKeys.search(settledQuery, kind, category, integration),
    queryFn: ({ pageParam, signal }) =>
      client.searchCatalog({ ...searchRequest(settledQuery, kind, category, integration), pageToken: pageParam }, { signal }),
    initialPageParam: "",
    getNextPageParam: (last) => last.nextPageToken || undefined,
    // Typing keeps the last results on screen until the next land. Switching
    // integration must not: one integration's actions would show under
    // another's name.
    placeholderData: integration ? undefined : keepPreviousData,
    staleTime: 30_000,
    enabled: active,
  });

  const entries = useMemo(() => {
    const seen = new Set<string>();
    const out: CatalogEntrySummary[] = [];
    for (const page of result.data?.pages ?? []) {
      for (const entry of page.entries) {
        if (seen.has(entry.ref)) continue;
        seen.add(entry.ref);
        out.push(catalogSummaryFromProto(entry));
      }
    }
    return out;
  }, [result.data]);

  const first = result.data?.pages[0];
  const facets = useMemo(() => first?.categoryFacets.map(catalogFacetFromProto) ?? [], [first]);

  return {
    entries,
    facets,
    totalSize: first?.totalSize ?? 0,
    settledQuery,
    isLoading: result.isLoading,
    isFetching: result.isFetching,
    isError: result.isError,
    error: result.error,
    hasNextPage: !!result.hasNextPage,
    isFetchingNextPage: result.isFetchingNextPage,
    fetchNextPage: () => void result.fetchNextPage(),
    refetch: () => void result.refetch(),
  };
}

export interface CatalogIntegrationsArgs {
  kind: CatalogKind;
  category?: string;
  pageSize?: number;
  enabled?: boolean;
}

export interface CatalogIntegrationsState {
  /** Connected first, then by display name; deduplicated across pages. */
  integrations: CatalogIntegrationListing[];
  facets: CatalogFacet[];
  totalSize: number;
  isLoading: boolean;
  isError: boolean;
  hasNextPage: boolean;
  isFetchingNextPage: boolean;
  fetchNextPage: () => void;
  refetch: () => void;
}

/**
 * The catalog browsed by integration — what the palette lists before the
 * user types. Paged by token like the search, so a catalog of hundreds of
 * integrations is fetched only as far as it is scrolled.
 */
export function useCatalogIntegrations({ kind, category = "", pageSize = CATALOG_PAGE_SIZE, enabled = true }: CatalogIntegrationsArgs): CatalogIntegrationsState {
  const result = useInfiniteQuery({
    queryKey: catalogPageKeys.integrations(kind, category, pageSize),
    queryFn: ({ pageParam, signal }) =>
      client.listCatalogIntegrations({ ...integrationsRequest(kind, category, pageSize), pageToken: pageParam }, { signal }),
    initialPageParam: "",
    getNextPageParam: (last) => last.nextPageToken || undefined,
    placeholderData: keepPreviousData,
    staleTime: 30_000,
    enabled,
  });

  const integrations = useMemo(() => {
    const seen = new Set<string>();
    const out: CatalogIntegrationListing[] = [];
    for (const page of result.data?.pages ?? []) {
      for (const listing of page.integrations) {
        const id = listing.integration?.id ?? "";
        if (seen.has(id)) continue;
        seen.add(id);
        out.push(catalogListingFromProto(listing));
      }
    }
    return out;
  }, [result.data]);

  const first = result.data?.pages[0];
  const facets = useMemo(() => first?.categoryFacets.map(catalogFacetFromProto) ?? [], [first]);
  return {
    integrations,
    facets,
    totalSize: first?.totalSize ?? 0,
    isLoading: result.isLoading,
    isError: result.isError,
    hasNextPage: !!result.hasNextPage,
    isFetchingNextPage: result.isFetchingNextPage,
    fetchNextPage: () => void result.fetchNextPage(),
    refetch: () => void result.refetch(),
  };
}
