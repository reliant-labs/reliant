// Copyright (c) 2025 Reliant Labs

/**
 * Debounced, paged catalog search for the step palette.
 *
 * One infinite query per (query, kind, category): typing changes the key,
 * React Query keeps the previous page on screen until the next one lands,
 * and an in-flight request for a stale key is aborted. Pages come from
 * `next_page_token`, so the browser only ever holds what was scrolled to.
 *
 * Both reads are forge's generated infinite hooks: keyed under
 * catalogServiceKeys.all (so a connection change refetches them), with the
 * page token owned by the hook and the proto mapped to the view by `select`.
 */

import { keepPreviousData, type InfiniteData } from "@tanstack/react-query";

import type { ListCatalogIntegrationsResponse, SearchCatalogResponse } from "../../../gen/reliant/v1/catalog_pb";
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
import { useListCatalogIntegrationsInfinite, useSearchCatalogInfinite } from "../../../hooks/catalog-service-hooks_gen";
import { useDebounce } from "../../../hooks/useDebounce";

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

interface SearchView {
  entries: CatalogEntrySummary[];
  facets: CatalogFacet[];
  totalSize: number;
}

/** Every loaded page's entries, deduplicated by ref; facets and total from the first page. */
function searchViewOf(data: InfiniteData<SearchCatalogResponse, string>): SearchView {
  const seen = new Set<string>();
  const entries: CatalogEntrySummary[] = [];
  for (const page of data.pages) {
    for (const entry of page.entries) {
      if (seen.has(entry.ref)) continue;
      seen.add(entry.ref);
      entries.push(catalogSummaryFromProto(entry));
    }
  }
  const first = data.pages[0];
  return { entries, facets: first?.categoryFacets.map(catalogFacetFromProto) ?? [], totalSize: first?.totalSize ?? 0 };
}

interface IntegrationsView {
  integrations: CatalogIntegrationListing[];
  facets: CatalogFacet[];
  totalSize: number;
}

/** Every loaded page's integrations, deduplicated by id; facets and total from the first page. */
function integrationsViewOf(data: InfiniteData<ListCatalogIntegrationsResponse, string>): IntegrationsView {
  const seen = new Set<string>();
  const integrations: CatalogIntegrationListing[] = [];
  for (const page of data.pages) {
    for (const listing of page.integrations) {
      const id = listing.integration?.id ?? "";
      if (seen.has(id)) continue;
      seen.add(id);
      integrations.push(catalogListingFromProto(listing));
    }
  }
  const first = data.pages[0];
  return { integrations, facets: first?.categoryFacets.map(catalogFacetFromProto) ?? [], totalSize: first?.totalSize ?? 0 };
}

const NO_ENTRIES: CatalogEntrySummary[] = [];
const NO_INTEGRATIONS: CatalogIntegrationListing[] = [];
const NO_FACETS: CatalogFacet[] = [];

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

  const result = useSearchCatalogInfinite(searchRequest(settledQuery, kind, category, integration), {
    select: searchViewOf,
    // Typing keeps the last results on screen until the next land. Switching
    // integration must not: one integration's actions would show under
    // another's name.
    placeholderData: integration ? undefined : keepPreviousData,
    staleTime: 30_000,
    enabled: active,
  });

  return {
    entries: result.data?.entries ?? NO_ENTRIES,
    facets: result.data?.facets ?? NO_FACETS,
    totalSize: result.data?.totalSize ?? 0,
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
  const result = useListCatalogIntegrationsInfinite(integrationsRequest(kind, category, pageSize), {
    select: integrationsViewOf,
    placeholderData: keepPreviousData,
    staleTime: 30_000,
    enabled,
  });

  return {
    integrations: result.data?.integrations ?? NO_INTEGRATIONS,
    facets: result.data?.facets ?? NO_FACETS,
    totalSize: result.data?.totalSize ?? 0,
    isLoading: result.isLoading,
    isError: result.isError,
    hasNextPage: !!result.hasNextPage,
    isFetchingNextPage: result.isFetchingNextPage,
    fetchNextPage: () => void result.fetchNextPage(),
    refetch: () => void result.refetch(),
  };
}
