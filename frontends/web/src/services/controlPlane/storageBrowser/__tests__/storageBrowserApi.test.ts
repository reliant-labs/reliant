// Copyright (c) 2025 Reliant Labs

import { create } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { describe, expect, it } from "vitest";
import {
  BucketRuntimeKind,
  GetObjectMetadataResponseSchema,
  ListBrowsableBucketsResponseSchema,
  ListObjectsResponseSchema,
  ObjectPreviewKind,
  PresignObjectDownloadResponseSchema,
  PreviewObjectResponseSchema,
  StorageAccessPolicy,
} from "@/gen/controlplane/services/storage_browser/v1/storage_browser_pb";
import { listObjectsRequest, objectRequest, toBucketList, toListing, toObjectMeta, toPresignedDownload, toPreview } from "../storageBrowserApi";

const access = { confirmEnvId: "env-prod", reason: "Investigating #4412" };
const target = { environmentId: "env-prod", bucket: "uploads", key: "odd names/100% #?.json", access };

describe("StorageBrowserService wire boundary", () => {
  it("reads the env policy and reachability", () => {
    const res = create(ListBrowsableBucketsResponseSchema, {
      policy: StorageAccessPolicy.AUDITED,
      buckets: [{ name: "uploads", runtime: BucketRuntimeKind.HOSTED, reachable: true }],
    });
    expect(toBucketList(res)).toEqual({ policy: "audited", buckets: [{
      name: "uploads", runtime: "hosted", reachable: true, publicRead: false, unreachableReason: "",
    }] });
  });

  it("sends the opaque token, exact prefix, opening flag and access on the listing", () => {
    const res = create(ListObjectsResponseSchema, {
      prefixes: ["odd names/a/"], objects: [{ key: target.key, size: 1536n, updated: timestampFromMs(1000), etag: "e" }], nextPageToken: "opaque",
    });
    expect(toListing(res)).toEqual({
      prefixes: ["odd names/a/"], objects: [{ key: target.key, size: 1536, updatedMs: 1000, etag: "e" }], nextPageToken: "opaque",
    });
    expect(listObjectsRequest({ ...target, prefix: "odd names/", pageToken: "previous", pageSize: 200, openBucket: true }))
      .toEqual({ environmentId: "env-prod", bucket: "uploads", prefix: "odd names/", pageToken: "previous", pageSize: 200, openBucket: true, ...access });
    expect(listObjectsRequest({ environmentId: "env-prod", bucket: "uploads", prefix: "", pageToken: "", pageSize: 0, openBucket: false }))
      .toStrictEqual({ environmentId: "env-prod", bucket: "uploads", pageToken: "", pageSize: 0 });
  });

  it("maps metadata and both preview bodies, sending access on every object call", () => {
    const meta = create(GetObjectMetadataResponseSchema, { object: {
      key: target.key, size: 42n, contentType: "application/json", preview: ObjectPreviewKind.JSON, metadata: { author: "Ada" },
    } });
    expect(toObjectMeta(meta)).toMatchObject({ key: target.key, size: 42, preview: "json", metadata: { author: "Ada" } });
    const content = new TextEncoder().encode("{}");
    expect(toPreview(create(PreviewObjectResponseSchema, { kind: ObjectPreviewKind.JSON, size: 2n, body: { case: "content", value: content } })))
      .toMatchObject({ kind: "json", size: 2, image: null, content });
    const image = create(PreviewObjectResponseSchema, { kind: ObjectPreviewKind.IMAGE, body: { case: "image", value: { url: "https://example.com/preview", expiresAt: timestampFromMs(120000) } } });
    expect(toPreview(image)).toMatchObject({ kind: "image", content: null, image: { url: "https://example.com/preview", expiresAtMs: 120000 } });
    expect(objectRequest(target)).toEqual({ environmentId: target.environmentId, bucket: target.bucket, key: target.key, ...access });
    expect(objectRequest({ environmentId: "env-staging", bucket: "uploads", key: "a" })).toStrictEqual({ environmentId: "env-staging", bucket: "uploads", key: "a" });
    expect(() => toObjectMeta(create(GetObjectMetadataResponseSchema, {}))).toThrow("The server returned no object metadata.");
  });

  it("reads a minted download", () => {
    const res = create(PresignObjectDownloadResponseSchema, { url: "https://example.com/download", filename: "file.json", size: 42n });
    expect(toPresignedDownload(res)).toEqual({ url: "https://example.com/download", filename: "file.json", size: 42, expiresAtMs: null });
  });
});
