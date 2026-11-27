// Copyright (c) 2025 Reliant Labs

/**
 * The storage browser's reads, as React Query hooks over
 * StorageBrowserService, and its one mutation.
 *
 * Every read but the bucket list writes an audit row, so none refetches on
 * its own: no window-focus or reconnect refetch, no polling. A reader who
 * wants a newer listing asks (Refresh), and that is one more recorded read,
 * on purpose. The confirmation is never part of a key: changing the reason
 * does not make a listing stale.
 *
 * The download URL is a MUTATION (PresignObjectDownload is not
 * NO_SIDE_EFFECTS): minted on a click, handed to the browser, and not kept —
 * gcTime 0, so it does not linger in the mutation cache either.
 *
 * Every call goes to the transport of the QueryClient it runs on: a
 * TransportScope's (a sample app, a preview, a test's fixture), else the
 * app's. PresignObjectDownload and PreviewObject go through the generated
 * hooks file's calls (storageBrowserServiceMutations, with `invalidate:
 * false`: a URL refreshes nothing, and refreshing an audited read writes a
 * row). The three reads stay hand-written on the same scoped client, because
 * a generated key is [service, method, protojson(request)]:
 *   - every key here carries the reader session (principal, epoch), which
 *     useReaderCacheCleanup("storage-browser") evicts by, and every read is
 *     checked against the reader before and after it runs (forReader);
 *   - the audited confirmation (confirm_env_id, reason) stays out of the key;
 *   - ListObjects decides open_bucket when the read runs, not from the key.
 */

import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { StorageBrowserService } from "@/gen/controlplane/services/storage_browser/v1/storage_browser_pb";
import { storageBrowserServiceMutations } from "@/hooks/controlplane/storage-browser-service-hooks_gen";
import { connectClient } from "@/lib/connect";
import { scopedClient } from "@/lib/transportScope";
import {
  listObjectsRequest,
  objectRequest,
  toBucketList,
  toListing,
  toObjectMeta,
  toPresignedDownload,
  toPreview,
  type ListObjectsInput,
  type ObjectInput,
} from "@/services/controlPlane/storageBrowser/storageBrowserApi";
import { storageRetry, storageRetryDelay } from "@/services/controlPlane/storageBrowser/errors";

import { markBucketOpened, wasBucketOpened } from "./storageAccess-model";
import { useReaderSession, requireCurrentReader } from "../AuditedAccess/readerSession";

/** The client a read uses: its QueryClient's TransportScope's transport, else the app's. */
const storageClient = scopedClient(StorageBrowserService, connectClient(StorageBrowserService));

async function forReader<T>(reader: ReturnType<typeof useReaderSession>, read: () => Promise<T>): Promise<T> {
  requireCurrentReader(reader);
  const result = await read();
  requireCurrentReader(reader);
  return result;
}

const BASE = ["storage-browser"] as const;

const READ_OPTIONS = {
  retry: storageRetry,
  retryDelay: storageRetryDelay,
  refetchOnWindowFocus: false,
  refetchOnReconnect: false,
} as const;

export function useBrowsableBuckets(environmentId: string) {
  const reader = useReaderSession();
  const base = [...BASE, reader.principalId, reader.epoch];
  return useQuery({
    queryKey: [...base, "buckets", environmentId],
    queryFn: ({ signal, client }) => forReader(reader, async () => toBucketList(await storageClient({ client }).listBrowsableBuckets({ environmentId }, { signal }))),
    enabled: !!reader.principalId,
    staleTime: 60_000,
    ...READ_OPTIONS,
    retry: (count, error) => { try { requireCurrentReader(reader); } catch { return false; } return storageRetry(count, error); },
  });
}

type ListInput = Omit<ListObjectsInput, "openBucket">;

/**
 * One page of one level. `open_bucket` is decided when the read runs, not
 * from the key: true on this tab's first listing of the bucket, and marked
 * only once a listing carrying it succeeded, so a refused first read (a
 * missing reason) does not swallow the open.
 */
export function useObjectListing(input: ListInput | null) {
  const reader = useReaderSession();
  const base = [...BASE, reader.principalId, reader.epoch];
  const { access: _access, ...identity } = input ?? ({} as Partial<ListInput>);
  return useQuery({
    queryKey: [...base, "list", input ? identity : null],
    queryFn: async ({ signal, client }) => {
      const req = input!;
      const openBucket = !wasBucketOpened(req.environmentId, req.bucket, reader);
      const listing = await forReader(reader, async () => toListing(await storageClient({ client }).listObjects(listObjectsRequest({ ...req, openBucket }), { signal })));
      if (openBucket) markBucketOpened(req.environmentId, req.bucket, reader);
      return listing;
    },
    enabled: !!input && !!reader.principalId,
    staleTime: 60_000,
    placeholderData: keepPreviousData,
    ...READ_OPTIONS,
    retry: (count, error) => { try { requireCurrentReader(reader); } catch { return false; } return storageRetry(count, error); },
  });
}

function objectKey(kind: string, input: ObjectInput | null) {
  if (!input) return [...BASE, kind, null] as const;
  return [...BASE, kind, input.environmentId, input.bucket, input.key] as const;
}

export function useObjectMetadata(input: ObjectInput | null) {
  const reader = useReaderSession();
  const base = [...BASE, reader.principalId, reader.epoch];
  return useQuery({
    queryKey: [...base, ...objectKey("metadata", input)],
    queryFn: ({ signal, client }) => forReader(reader, async () => toObjectMeta(await storageClient({ client }).getObjectMetadata(objectRequest(input!), { signal }))),
    enabled: !!input && !!reader.principalId,
    staleTime: 60_000,
    ...READ_OPTIONS,
    retry: (count, error) => { try { requireCurrentReader(reader); } catch { return false; } return storageRetry(count, error); },
  });
}

/**
 * A preview. An image's URL lives two minutes, so the answer is not kept
 * past its view (gcTime 0) and is stale well before the URL expires: coming
 * back to an object reads a fresh one. PreviewObject is not NO_SIDE_EFFECTS
 * (it mints that URL), so the generated file has it as a write: read here as
 * a query, through that write's call, refreshing nothing.
 */
export function useObjectPreview(input: ObjectInput | null) {
  const reader = useReaderSession();
  const base = [...BASE, reader.principalId, reader.epoch];
  return useQuery({
    queryKey: [...base, ...objectKey("preview", input)],
    queryFn: ({ signal, client }) =>
      forReader(reader, async () => toPreview(await storageBrowserServiceMutations(client).previewObject(objectRequest(input!), { signal, invalidate: false }))),
    enabled: !!input && !!reader.principalId,
    staleTime: 60_000,
    gcTime: 0,
    ...READ_OPTIONS,
    retry: (count, error) => { try { requireCurrentReader(reader); } catch { return false; } return storageRetry(count, error); },
  });
}

export function useObjectDownload(onDownloadUrl: (url: string) => void) {
  const queryClient = useQueryClient();
  const reader = useReaderSession();
  const base = [...BASE, reader.principalId, reader.epoch];
  return useMutation({
    mutationKey: [...base, "download"],
    // Hand it straight to the browser and return nothing: even the mutation's
    // state must not retain this bearer URL while the pane remains mounted.
    // Nothing is invalidated: every refetched audited read would be a new row.
    mutationFn: async (input: ObjectInput): Promise<void> => {
      const minted = await forReader(reader, () => storageBrowserServiceMutations(queryClient).presignObjectDownload(objectRequest(input), { invalidate: false }));
      onDownloadUrl(toPresignedDownload(minted).url);
    },
    gcTime: 0,
    retry: false,
  });
}
