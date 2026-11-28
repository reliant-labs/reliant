// Copyright (c) 2025 Reliant Labs

/**
 * `controlplane.v1.StorageBrowserService`: the storage browser's five reads
 * of an environment's object storage (control-plane
 * proto/services/storage_browser/v1/storage_browser.proto, whose header is
 * the behaviour spec; docs/storage-browser.md is the design). The only file
 * that knows their wire shape: everything above it speaks the plain types
 * below (numbers for sizes, epoch ms for times, policy names).
 *
 *   ListBrowsableBuckets   the env's buckets, their reachability, and the
 *                          policy every other read runs under (no audit row)
 *   ListObjects            one level under a prefix, paged by an opaque token
 *   GetObjectMetadata      one object's HEAD
 *   PresignObjectDownload  a 5-minute attachment URL: a MUTATION (every call
 *                          is a new URL, audit row and Activity line)
 *   PreviewObject          an image's 2-minute inline URL, or text/JSON bytes
 *
 * Every read but the first is audited server-side, and an audited
 * environment's carries confirm_env_id and a reason (`access`).
 *
 * This file builds the requests and reads the answers; the calls themselves
 * are made in components/Forge/Storage/useStorageBrowser.ts (the two writes
 * through the generated hooks file, the three reads on the scope's client).
 */

import { timestampMs, type Timestamp } from "@bufbuild/protobuf/wkt";

import {
  BucketRuntimeKind,
  ObjectPreviewKind,
  StorageAccessPolicy,
  type GetObjectMetadataResponse,
  type ListBrowsableBucketsResponse,
  type ListObjectsResponse,
  type PresignObjectDownloadResponse,
  type PreviewObjectResponse,
} from "@/gen/controlplane/services/storage_browser/v1/storage_browser_pb";
import type { AccessConfirmation, AccessPolicyName } from "@/components/Forge/AuditedAccess/accessSession-model";

/** What every request but the bucket list carries: the target, and the audited environment's confirmation when given. */
type StorageRequest = { environmentId: string; bucket: string; confirmEnvId?: string; reason?: string };

export type BucketRuntimeName = "minio" | "s3" | "gcs" | "hosted" | "unknown";

/** How an object can be shown inline, as GetObjectMetadata judged it. */
export type PreviewKindName = "image" | "text" | "json" | "none";

export interface StorageBucket {
  /** The forge.Bucket name every other read takes, not the cloud name. */
  name: string;
  runtime: BucketRuntimeName;
  publicRead: boolean;
  reachable: boolean;
  /** Why it cannot be browsed, for a person. Empty when reachable. */
  unreachableReason: string;
}

export interface StorageBucketList {
  /** Sorted by name. */
  buckets: StorageBucket[];
  /** The environment's data_access.storage: one policy for all of its buckets. */
  policy: AccessPolicyName;
}

export interface StorageObjectRow {
  key: string;
  size: number;
  /** Epoch ms, or null when the backend sent none. */
  updatedMs: number | null;
  etag: string;
}

export interface StorageListing {
  /** Full key prefixes ending in "/", lexical. */
  prefixes: string[];
  /** The objects directly under the prefix, lexical. */
  objects: StorageObjectRow[];
  /** Empty on the last page. */
  nextPageToken: string;
}

export interface StorageObjectMeta {
  key: string;
  size: number;
  /** The stored Content-Type, or empty. */
  contentType: string;
  updatedMs: number | null;
  etag: string;
  /** User metadata, keys without the x-amz-meta- / x-goog-meta- prefix. */
  metadata: Record<string, string>;
  preview: PreviewKindName;
}

export interface PresignedDownload {
  url: string;
  filename: string;
  size: number;
  expiresAtMs: number | null;
}

export interface StoragePreview {
  kind: PreviewKindName;
  contentType: string;
  size: number;
  /** IMAGE: the inline URL for an <img>. */
  image: { url: string; expiresAtMs: number | null } | null;
  /** TEXT and JSON: the object's bytes, at most 1 MiB. */
  content: Uint8Array | null;
}

/** Which object or level a read addresses, and the audited environment's confirmation. */
export interface StorageTarget {
  environmentId: string;
  bucket: string;
  access?: AccessConfirmation;
}

export interface ListObjectsInput extends StorageTarget {
  /** "" for the bucket's root. */
  prefix: string;
  /** "" for the first page. */
  pageToken: string;
  /** 0: the server's default (200). */
  pageSize: number;
  /** The first listing of this bucket in the tab session: records the Activity line. */
  openBucket: boolean;
}

export interface ObjectInput extends StorageTarget {
  key: string;
}

const POLICY_NAMES: Record<StorageAccessPolicy, AccessPolicyName> = {
  [StorageAccessPolicy.UNSPECIFIED]: "unknown",
  [StorageAccessPolicy.OPEN]: "open",
  [StorageAccessPolicy.AUDITED]: "audited",
  [StorageAccessPolicy.DENY]: "deny",
};

const RUNTIME_NAMES: Record<BucketRuntimeKind, BucketRuntimeName> = {
  [BucketRuntimeKind.UNSPECIFIED]: "unknown",
  [BucketRuntimeKind.MINIO]: "minio",
  [BucketRuntimeKind.S3]: "s3",
  [BucketRuntimeKind.GCS]: "gcs",
  [BucketRuntimeKind.HOSTED]: "hosted",
};

const PREVIEW_NAMES: Record<ObjectPreviewKind, PreviewKindName> = {
  [ObjectPreviewKind.UNSPECIFIED]: "none",
  [ObjectPreviewKind.IMAGE]: "image",
  [ObjectPreviewKind.TEXT]: "text",
  [ObjectPreviewKind.JSON]: "json",
};

const msOf = (ts: Timestamp | undefined): number | null => (ts ? timestampMs(ts) : null);

function accessFields(access: AccessConfirmation | undefined) {
  return access ? { confirmEnvId: access.confirmEnvId, reason: access.reason } : {};
}

export function toBucketList(res: ListBrowsableBucketsResponse): StorageBucketList {
  return {
    policy: POLICY_NAMES[res.policy] ?? "unknown",
    buckets: res.buckets.map((bucket) => ({
      name: bucket.name,
      runtime: RUNTIME_NAMES[bucket.runtime] ?? "unknown",
      publicRead: bucket.publicRead,
      reachable: bucket.reachable,
      unreachableReason: bucket.unreachableReason,
    })),
  };
}

/** ListObjects' request: `prefix` and `open_bucket` only when set, the confirmation only when given. */
export function listObjectsRequest(req: ListObjectsInput): StorageRequest & { prefix?: string; pageToken: string; pageSize: number; openBucket?: boolean } {
  return {
    environmentId: req.environmentId,
    bucket: req.bucket,
    ...(req.prefix ? { prefix: req.prefix } : {}),
    pageToken: req.pageToken,
    pageSize: req.pageSize,
    ...(req.openBucket ? { openBucket: true } : {}),
    ...accessFields(req.access),
  };
}

export function toListing(res: ListObjectsResponse): StorageListing {
  return {
    prefixes: [...res.prefixes],
    objects: res.objects.map((object) => ({ key: object.key, size: Number(object.size), updatedMs: msOf(object.updated), etag: object.etag })),
    nextPageToken: res.nextPageToken,
  };
}

/** GetObjectMetadata's, PresignObjectDownload's and PreviewObject's request: one object, and the confirmation when given. */
export function objectRequest(req: ObjectInput): StorageRequest & { key: string } {
  return { environmentId: req.environmentId, bucket: req.bucket, key: req.key, ...accessFields(req.access) };
}

export function toObjectMeta(res: GetObjectMetadataResponse): StorageObjectMeta {
  const object = res.object;
  if (!object) throw new Error("The server returned no object metadata.");
  return {
    key: object.key,
    size: Number(object.size),
    contentType: object.contentType,
    updatedMs: msOf(object.updated),
    etag: object.etag,
    metadata: { ...object.metadata },
    preview: PREVIEW_NAMES[object.preview] ?? "none",
  };
}

/** A new URL every call: never cache it, never ask for one but on a click. */
export function toPresignedDownload(res: PresignObjectDownloadResponse): PresignedDownload {
  return { url: res.url, filename: res.filename, size: Number(res.size), expiresAtMs: msOf(res.expiresAt) };
}

export function toPreview(res: PreviewObjectResponse): StoragePreview {
  return {
    kind: PREVIEW_NAMES[res.kind] ?? "none",
    contentType: res.contentType,
    size: Number(res.size),
    image: res.body.case === "image" ? { url: res.body.value.url, expiresAtMs: msOf(res.body.value.expiresAt) } : null,
    content: res.body.case === "content" ? res.body.value : null,
  };
}
