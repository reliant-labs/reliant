// Copyright (c) 2025 Reliant Labs

/**
 * The data browser's reads, as React Query hooks over DataBrowserApiContext.
 *
 * Every DescribeDatabase and BrowseTable writes an audit row (and, in an
 * audited environment, an Activity entry with the reader's reason), so these
 * never refetch on their own: no window-focus refetch, no polling, and a
 * result stays fresh for a while. A reader who wants newer rows asks (the
 * grid's Refresh), and that is one more recorded read, on purpose.
 */

import { keepPreviousData, useQuery } from "@tanstack/react-query";

import type { BrowseInput, DataAccessConfirmation } from "@/services/controlPlane/observability/appDatabaseApi";
import { useDataBrowserApi } from "@/services/controlPlane/observability/DataBrowserApiContext";
import { dataBrowserRetry, dataBrowserRetryDelay } from "@/services/controlPlane/observability/errors";

const BASE = ["data-browser"] as const;

const READ_OPTIONS = {
  retry: dataBrowserRetry,
  retryDelay: dataBrowserRetryDelay,
  refetchOnWindowFocus: false,
  refetchOnReconnect: false,
} as const;

export function useBrowsableDatabases(environmentId: string | null) {
  const api = useDataBrowserApi();
  return useQuery({
    queryKey: [...BASE, "databases", environmentId],
    queryFn: ({ signal }) => api.listBrowsableDatabases(environmentId!, { signal }),
    enabled: !!environmentId,
    staleTime: 60_000,
    ...READ_OPTIONS,
  });
}

/**
 * The tables of one database. `enabled` is false until the read may run: an
 * audited environment waits for its confirmation. The reason is not in the
 * key: changing it does not make the schema stale.
 */
export function useDescribeDatabase(environmentId: string, database: string | null, access: DataAccessConfirmation | undefined, enabled: boolean) {
  const api = useDataBrowserApi();
  return useQuery({
    queryKey: [...BASE, "describe", environmentId, database],
    queryFn: ({ signal }) => api.describeDatabase({ environmentId, database: database!, ...(access ? { access } : {}) }, { signal }),
    enabled: enabled && !!database,
    staleTime: 5 * 60_000,
    ...READ_OPTIONS,
  });
}

/** The request's identity, without the confirmation (which does not change what the rows are). */
function browseKey(input: BrowseInput) {
  const { access: _access, ...rest } = input;
  return [...BASE, "browse", rest] as const;
}

/** One page of one table. Paging keeps the previous page on screen until the next arrives. */
export function useBrowseTable(input: BrowseInput | null, options: { keepPrevious?: boolean } = {}) {
  const api = useDataBrowserApi();
  return useQuery({
    queryKey: input ? browseKey(input) : [...BASE, "browse", null],
    queryFn: ({ signal }) => api.browseTable(input!, { signal }),
    enabled: !!input,
    staleTime: 60_000,
    placeholderData: options.keepPrevious ? keepPreviousData : undefined,
    ...READ_OPTIONS,
  });
}

/** A Related count: a `limit 1, count ESTIMATED` read, started through `gate` (at most a few at once). */
export function useRelatedCount(input: BrowseInput | null, gate: <T>(task: () => Promise<T>) => Promise<T>) {
  const api = useDataBrowserApi();
  return useQuery({
    queryKey: input ? browseKey(input) : [...BASE, "browse", null],
    queryFn: ({ signal }) => gate(() => api.browseTable(input!, { signal })),
    enabled: !!input,
    staleTime: 60_000,
    ...READ_OPTIONS,
  });
}
