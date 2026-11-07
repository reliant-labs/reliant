// Copyright (c) 2025 Reliant Labs

import { create } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { beforeEach, describe, expect, it, vi } from "vitest";
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
import { getObjectMetadata, listBrowsableBuckets, listObjects, presignObjectDownload, previewObject } from "../storageBrowserApi";

const client = {
  listBrowsableBuckets: vi.fn(), listObjects: vi.fn(), getObjectMetadata: vi.fn(),
  presignObjectDownload: vi.fn(), previewObject: vi.fn(),
};
vi.mock("../../client", () => ({ getControlPlaneClient: () => client }));
beforeEach(() => vi.clearAllMocks());
const access = { confirmEnvId: "env-prod", reason: "Investigating #4412" };
const target = { environmentId: "env-prod", bucket: "uploads", key: "odd names/100% #?.json", access };

describe("StorageBrowserService wire boundary", () => {
  it("reads the env policy and reachability without a confirmation", async () => {
    client.listBrowsableBuckets.mockResolvedValue(create(ListBrowsableBucketsResponseSchema, {
      policy: StorageAccessPolicy.AUDITED,
      buckets: [{ name: "uploads", runtime: BucketRuntimeKind.HOSTED, reachable: true }],
    }));
    expect(await listBrowsableBuckets("env-prod")).toEqual({ policy: "audited", buckets: [{
      name: "uploads", runtime: "hosted", reachable: true, publicRead: false, unreachableReason: "",
    }] });
    expect(client.listBrowsableBuckets).toHaveBeenCalledWith({ environmentId: "env-prod" }, undefined);
  });

  it("sends the opaque token, exact prefix, opening flag and access on the listing", async () => {
    client.listObjects.mockResolvedValue(create(ListObjectsResponseSchema, {
      prefixes: ["odd names/a/"], objects: [{ key: target.key, size: 1536n, updated: timestampFromMs(1000), etag: "e" }], nextPageToken: "opaque",
    }));
    const signal = new AbortController().signal;
    expect(await listObjects({ ...target, prefix: "odd names/", pageToken: "previous", pageSize: 200, openBucket: true }, { signal })).toEqual({
      prefixes: ["odd names/a/"], objects: [{ key: target.key, size: 1536, updatedMs: 1000, etag: "e" }], nextPageToken: "opaque",
    });
    expect(client.listObjects).toHaveBeenCalledWith({ environmentId: "env-prod", bucket: "uploads", prefix: "odd names/", pageToken: "previous", pageSize: 200, openBucket: true, ...access }, { signal });
    await listObjects({ environmentId: "env-prod", bucket: "uploads", prefix: "", pageToken: "", pageSize: 0, openBucket: false });
    expect(client.listObjects).toHaveBeenLastCalledWith({ environmentId: "env-prod", bucket: "uploads", pageToken: "", pageSize: 0 }, undefined);
  });

  it("maps metadata and both preview bodies, sending access on every call", async () => {
    client.getObjectMetadata.mockResolvedValue(create(GetObjectMetadataResponseSchema, { object: {
      key: target.key, size: 42n, contentType: "application/json", preview: ObjectPreviewKind.JSON, metadata: { author: "Ada" },
    } }));
    expect(await getObjectMetadata(target)).toMatchObject({ key: target.key, size: 42, preview: "json", metadata: { author: "Ada" } });
    const content = new TextEncoder().encode("{}");
    client.previewObject.mockResolvedValue(create(PreviewObjectResponseSchema, { kind: ObjectPreviewKind.JSON, size: 2n, body: { case: "content", value: content } }));
    expect(await previewObject(target)).toMatchObject({ kind: "json", size: 2, image: null, content });
    client.previewObject.mockResolvedValue(create(PreviewObjectResponseSchema, { kind: ObjectPreviewKind.IMAGE, body: { case: "image", value: { url: "https://example.com/preview", expiresAt: timestampFromMs(120000) } } }));
    expect(await previewObject(target)).toMatchObject({ kind: "image", content: null, image: { url: "https://example.com/preview", expiresAtMs: 120000 } });
    for (const method of ["getObjectMetadata", "previewObject"] as const) {
      expect(client[method]).toHaveBeenCalledWith({ environmentId: target.environmentId, bucket: target.bucket, key: target.key, ...access }, undefined);
    }
  });

  it("mints each download by a separate call", async () => {
    client.presignObjectDownload.mockResolvedValue(create(PresignObjectDownloadResponseSchema, { url: "https://example.com/download", filename: "file.json", size: 42n }));
    expect(await presignObjectDownload(target)).toEqual({ url: "https://example.com/download", filename: "file.json", size: 42, expiresAtMs: null });
    await presignObjectDownload(target);
    expect(client.presignObjectDownload).toHaveBeenCalledTimes(2);
    expect(client.presignObjectDownload).toHaveBeenCalledWith({ environmentId: target.environmentId, bucket: target.bucket, key: target.key, ...access }, undefined);
  });
});
