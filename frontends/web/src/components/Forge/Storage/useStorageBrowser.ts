// Copyright (c) 2025 Reliant Labs

/**
 * The storage browser's reads, as React Query hooks over
 * StorageBrowserApiContext, and its one mutation.
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
 */

import { keepPreviousData, useMutation, useQuery } from "@tanstack/react-query";

import type { ListObjectsInput, ObjectInput } from "@/services/controlPlane/storageBrowser/storageBrowserApi";
import { useStorageBrowserApi } from "@/services/controlPlane/storageBrowser/StorageBrowserApiContext";
import { storageRetry, storageRetryDelay } from "@/services/controlPlane/storageBrowser/errors";

import { markBucketOpened, wasBucketOpened } from "./storageAccess-model";
import { useReaderSession, requireCurrentReader } from "../AuditedAccess/readerSession";

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
  const api = useStorageBrowserApi();
  const reader = useReaderSession();
  const base = [...BASE, reader.principalId, reader.epoch];
  return useQuery({
    queryKey: [...base, "buckets", environmentId],
    queryFn: ({ signal }) => forReader(reader, () => api.listBrowsableBuckets(environmentId, { signal })),
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
  const api = useStorageBrowserApi();
  const reader = useReaderSession();
  const base = [...BASE, reader.principalId, reader.epoch];
  const { access: _access, ...identity } = input ?? ({} as Partial<ListInput>);
  return useQuery({
    queryKey: [...base, "list", input ? identity : null],
    queryFn: async ({ signal }) => {
      const req = input!;
      const openBucket = !wasBucketOpened(req.environmentId, req.bucket, reader);
      const listing = await forReader(reader, () => api.listObjects({ ...req, openBucket }, { signal }));
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
  const api = useStorageBrowserApi();
  const reader = useReaderSession();
  const base = [...BASE, reader.principalId, reader.epoch];
  return useQuery({
    queryKey: [...base, ...objectKey("metadata", input)],
    queryFn: ({ signal }) => forReader(reader, () => api.getObjectMetadata(input!, { signal })),
    enabled: !!input && !!reader.principalId,
    staleTime: 60_000,
    ...READ_OPTIONS,
    retry: (count, error) => { try { requireCurrentReader(reader); } catch { return false; } return storageRetry(count, error); },
  });
}

/**
 * A preview. An image's URL lives two minutes, so the answer is not kept
 * past its view (gcTime 0) and is stale well before the URL expires: coming
 * back to an object reads a fresh one.
 */
export function useObjectPreview(input: ObjectInput | null) {
  const api = useStorageBrowserApi();
  const reader = useReaderSession();
  const base = [...BASE, reader.principalId, reader.epoch];
  return useQuery({
    queryKey: [...base, ...objectKey("preview", input)],
    queryFn: ({ signal }) => forReader(reader, () => api.previewObject(input!, { signal })),
    enabled: !!input && !!reader.principalId,
    staleTime: 60_000,
    gcTime: 0,
    ...READ_OPTIONS,
    retry: (count, error) => { try { requireCurrentReader(reader); } catch { return false; } return storageRetry(count, error); },
  });
}

export function useObjectDownload(onDownloadUrl: (url: string) => void) {
  const api = useStorageBrowserApi();
  const reader = useReaderSession();
  const base = [...BASE, reader.principalId, reader.epoch];
  return useMutation({
    mutationKey: [...base, "download"],
    // Hand it straight to the browser and return nothing: even the mutation's
    // state must not retain this bearer URL while the pane remains mounted.
    mutationFn: async (input: ObjectInput): Promise<void> => {
      const minted = await forReader(reader, () => api.presignObjectDownload(input));
      onDownloadUrl(minted.url);
    },
    gcTime: 0,
    retry: false,
  });
}
