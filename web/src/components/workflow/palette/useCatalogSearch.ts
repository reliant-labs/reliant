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

import {
  catalogSearchGrpc,
  type CatalogEntrySummary,
  type CatalogFacet,
  type CatalogKind,
} from "../../../api/catalog-search-grpc";
import { useDebounce } from "../../../hooks/useDebounce";

export const CATALOG_SEARCH_DEBOUNCE_MS = 150;
export const CATALOG_PAGE_SIZE = 20;

export interface CatalogSearchArgs {
  query: string;
  kind: CatalogKind;
  category?: string;
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

export const catalogSearchKeys = {
  all: ["catalogSearch"] as const,
  search: (query: string, kind: CatalogKind, category: string) =>
    [...catalogSearchKeys.all, kind, category, query] as const,
  entry: (ref: string) => ["catalogEntry", ref] as const,
};

export function useCatalogSearch({ query, kind, category = "", enabled = true }: CatalogSearchArgs): CatalogSearchState {
  const settledQuery = useDebounce(query.trim(), CATALOG_SEARCH_DEBOUNCE_MS);

  const result = useInfiniteQuery({
    queryKey: catalogSearchKeys.search(settledQuery, kind, category),
    queryFn: ({ pageParam, signal }) =>
      catalogSearchGrpc.search(
        { query: settledQuery, kinds: [kind], category, pageSize: CATALOG_PAGE_SIZE, pageToken: pageParam },
        signal,
      ),
    initialPageParam: "",
    getNextPageParam: (last) => last.nextPageToken || undefined,
    placeholderData: keepPreviousData,
    staleTime: 30_000,
    enabled,
  });

  const entries = useMemo(() => {
    const seen = new Set<string>();
    const out: CatalogEntrySummary[] = [];
    for (const page of result.data?.pages ?? []) {
      for (const entry of page.entries) {
        if (seen.has(entry.ref)) continue;
        seen.add(entry.ref);
        out.push(entry);
      }
    }
    return out;
  }, [result.data]);

  const first = result.data?.pages[0];

  return {
    entries,
    facets: first?.categoryFacets ?? [],
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
