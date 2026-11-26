// Copyright (c) 2025 Reliant Labs

/**
 * The data browser's reads, as React Query hooks.
 *
 * Every DescribeDatabase and BrowseTable writes an audit row (and, in an
 * audited environment, an Activity entry with the reader's reason), so these
 * never refetch on their own: no window-focus refetch, no polling, and a
 * result stays fresh for a while. A reader who wants newer rows asks (the
 * grid's Refresh), and that is one more recorded read, on purpose.
 *
 * They are hand-written, not AppDatabaseService's generated hooks: every key
 * carries the reader session (principal and epoch), which
 * useReaderCacheCleanup("data-browser") evicts by, and each read checks that
 * reader before and after it runs; DescribeDatabase and BrowseTable leave the
 * audited confirmation (the reason) out of the key, and forge generates them
 * as writes (each records an audit row). The client is still the generated
 * hooks' one: `scopedClient` over the QueryClient the read runs on, so inside
 * a TransportScope (the tour's sample app, the preview) the read goes to the
 * scope's transport, and everywhere else to the app's.
 */

import { keepPreviousData, useQuery, type QueryFunctionContext } from "@tanstack/react-query";

import { AppDatabaseService } from "@/gen/controlplane/services/app_database/v1/app_database_pb";
import { connectClient } from "@/lib/connect";
import { scopedClient } from "@/lib/transportScope";
import { createDataBrowserApi, type BrowseInput, type DataAccessConfirmation } from "@/services/controlPlane/observability/appDatabaseApi";
import { dataBrowserRetry, dataBrowserRetryDelay } from "@/services/controlPlane/observability/errors";

import { useReaderSession, requireCurrentReader, type ReaderSession } from "../AuditedAccess/readerSession";

const BASE = ["data-browser"] as const;
const clientOf = scopedClient(AppDatabaseService, connectClient(AppDatabaseService));
/** The three reads, mapped to the browser's model, on the client the query's QueryClient resolves. */
const readsOf = (context: Pick<QueryFunctionContext, "client">) => createDataBrowserApi(clientOf(context));
async function forReader<T>(reader: ReaderSession, read: () => Promise<T>): Promise<T> {
  requireCurrentReader(reader);
  const result = await read();
  requireCurrentReader(reader);
  return result;
}
function readerRetry(reader: ReaderSession) {
  return (count: number, error: unknown) => {
    try { requireCurrentReader(reader); } catch { return false; }
    return dataBrowserRetry(count, error);
  };
}

const READ_OPTIONS = {
  retry: dataBrowserRetry,
  retryDelay: dataBrowserRetryDelay,
  refetchOnWindowFocus: false,
  refetchOnReconnect: false,
} as const;

export function useBrowsableDatabases(environmentId: string | null) {
  const reader = useReaderSession();
  return useQuery({
    queryKey: [...BASE, reader.principalId, reader.epoch, "databases", environmentId],
    queryFn: (context) => forReader(reader, () => readsOf(context).listBrowsableDatabases(environmentId!, { signal: context.signal })),
    enabled: !!environmentId && !!reader.principalId,
    staleTime: 60_000,
    ...READ_OPTIONS,
    retry: readerRetry(reader),
  });
}

/**
 * The tables of one database. `enabled` is false until the read may run: an
 * audited environment waits for its confirmation. The reason is not in the
 * key: changing it does not make the schema stale.
 */
export function useDescribeDatabase(environmentId: string, database: string | null, access: DataAccessConfirmation | undefined, enabled: boolean) {
  const reader = useReaderSession();
  return useQuery({
    queryKey: [...BASE, reader.principalId, reader.epoch, "describe", environmentId, database],
    queryFn: (context) => forReader(reader, () => readsOf(context).describeDatabase({ environmentId, database: database!, ...(access ? { access } : {}) }, { signal: context.signal })),
    enabled: enabled && !!database && !!reader.principalId,
    staleTime: 5 * 60_000,
    ...READ_OPTIONS,
    retry: readerRetry(reader),
  });
}

/** The request's identity, without the confirmation (which does not change what the rows are). */
function browseKey(input: BrowseInput, reader: ReaderSession) {
  const { access: _access, ...rest } = input;
  return [...BASE, reader.principalId, reader.epoch, "browse", rest] as const;
}

/** One page of one table. Paging keeps the previous page on screen until the next arrives. */
export function useBrowseTable(input: BrowseInput | null, options: { keepPrevious?: boolean } = {}) {
  const reader = useReaderSession();
  return useQuery({
    queryKey: input ? browseKey(input, reader) : [...BASE, reader.principalId, reader.epoch, "browse", null],
    queryFn: (context) => forReader(reader, () => readsOf(context).browseTable(input!, { signal: context.signal })),
    enabled: !!input && !!reader.principalId,
    staleTime: 60_000,
    placeholderData: options.keepPrevious ? keepPreviousData : undefined,
    ...READ_OPTIONS,
    retry: readerRetry(reader),
  });
}

/** A Related count: a `limit 1, count ESTIMATED` read, started through `gate` (at most a few at once). */
export function useRelatedCount(input: BrowseInput | null, gate: <T>(task: () => Promise<T>) => Promise<T>) {
  const reader = useReaderSession();
  return useQuery({
    queryKey: input ? browseKey(input, reader) : [...BASE, reader.principalId, reader.epoch, "browse", null],
    queryFn: (context) => gate(() => forReader(reader, () => readsOf(context).browseTable(input!, { signal: context.signal }))),
    enabled: !!input && !!reader.principalId,
    staleTime: 60_000,
    ...READ_OPTIONS,
    retry: readerRetry(reader),
  });
}
