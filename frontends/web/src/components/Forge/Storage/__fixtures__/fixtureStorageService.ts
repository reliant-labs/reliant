// Copyright (c) 2025 Reliant Labs

/**
 * An in-memory StorageBrowserService: a small app's buckets, answered the way
 * control-plane's answers (one level per listing with "/" as the delimiter,
 * opaque page tokens sealed to the environment, bucket and prefix, the
 * audited environment's confirmation, preview caps), served as mock-transport
 * handlers (`storageFixtureTransport`), so the component tests exercise the
 * real browser, its real calls included, against real behaviour. (The
 * dev-only preview, /forge-storage-preview, and the demo render the shop
 * fixture pack's smaller handlers, fixtures/shop/handlers/storage.ts.)
 *
 *   environments  env-prod (prod, AUDITED), env-staging (staging, OPEN),
 *                 env-dev (dev, OPEN)
 *   buckets       uploads (hosted), exports (hosted), media (MinIO,
 *                 unreachable)
 *   uploads       robots.txt, avatars/2026/{ada.png, grace.webp, logo.svg},
 *                 avatars/legacy.jpg (14 MiB: too large to preview),
 *                 docs/{readme.txt, config.json, broken.json, server.log
 *                 (3 MiB: over the preview cap)}, reports/q1.pdf,
 *                 blobs/0001 (no content type), and `odd names/` (a `%`, a
 *                 `#`, a `?`, non-ASCII, leading spaces, an empty segment)
 *   exports       daily/: 450 CSVs (three pages of 200)
 *
 * Every call is recorded in `calls`, in the browser's terms (`asked`: the
 * confirmation as `access`, an unset prefix as ""); `presigned` counts the
 * URLs minted. Each rpc's handler is a field of the fixture, looked up per
 * call: a test replaces one to change its answer.
 */

import type { MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError, type Transport } from "@connectrpc/connect";
import { createMockTransport, mockService } from "@reliantlabs/forge-web-runtime/mock-transport";

import type { AccessConfirmation, AccessPolicyName } from "@/components/Forge/AuditedAccess/accessSession-model";
import { completed } from "@/fixtures/shop/handlers/defaults";
import {
  BucketRuntimeKind,
  ObjectPreviewKind,
  StorageAccessPolicy,
  StorageBrowserService,
  type GetObjectMetadataRequest,
  type GetObjectMetadataResponseSchema,
  type ListBrowsableBucketsRequest,
  type ListBrowsableBucketsResponseSchema,
  type ListObjectsRequest,
  type ListObjectsResponseSchema,
  type PresignObjectDownloadRequest,
  type PresignObjectDownloadResponseSchema,
  type PreviewObjectRequest,
  type PreviewObjectResponseSchema,
} from "@/gen/controlplane/services/storage_browser/v1/storage_browser_pb";
import type { PreviewKindName } from "@/services/controlPlane/storageBrowser/storageBrowserApi";

type Method = "listBrowsableBuckets" | "listObjects" | "getObjectMetadata" | "presignObjectDownload" | "previewObject";

export interface StorageFixtureOptions {
  /** The policy, per environment id (default: prod audited, the rest open). */
  policy?: Partial<Record<string, AccessPolicyName>>;
  /** ListBrowsableBuckets answers none. */
  noBuckets?: boolean;
  /** Fail this rpc with this code (and reason, sent as x-forge-error-reason). Only for `keys` when given. */
  fail?: { method: Method; code: Code; reason?: string; message?: string; keys?: string[] };
  /** A listing's page size when the request leaves it 0 (the server's default: 200). */
  defaultPageSize?: number;
  /** Exercise defensive handling of a future server that returns capped text rather than refusing an oversized object. */
  truncatedText?: boolean;
}

export interface StorageFixture {
  listBrowsableBuckets(req: ListBrowsableBucketsRequest): Promise<MessageInitShape<typeof ListBrowsableBucketsResponseSchema>>;
  listObjects(req: ListObjectsRequest): Promise<MessageInitShape<typeof ListObjectsResponseSchema>>;
  getObjectMetadata(req: GetObjectMetadataRequest): Promise<MessageInitShape<typeof GetObjectMetadataResponseSchema>>;
  presignObjectDownload(req: PresignObjectDownloadRequest): Promise<MessageInitShape<typeof PresignObjectDownloadResponseSchema>>;
  previewObject(req: PreviewObjectRequest): Promise<MessageInitShape<typeof PreviewObjectResponseSchema>>;
  calls: { method: Method; req: unknown }[];
  presigned: number;
}

const MiB = 1024 * 1024;
const T0 = Date.UTC(2026, 9, 20, 9, 30);

/** A 24×24 gradient PNG: what an inline image preview draws in the harness. */
const FIXTURE_PNG =
  "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAABgAAAAYCAIAAABvFaqvAAAEHElEQVR42g3MIc92UACA4S+/URRFURRFURBMOskEu2fSSSYIJp1kgmDSSSYIJp0kSZLkp3zP9QOuf398Fp/N5/C5fB6fzxfwhXwRX8In+HK+kk/yNXwdn+Ib+WY+zbfxHXyG798fr8Vr8zq8Lq/H6/MGvCFvxJvwCt6ct+SVvA1vx6t4R96ZV/NuvAev4f1Fj8Vj8zg8Lo/H4/MEPCFPxJPwCJ6cp+SRPA1Px6N4Rp6ZR/NsPAeP4flFt8VtczvcLrfH7XMH3CF3xJ1wC+6cu+SW3A13x624R+6ZW3Nv3Ae34f5Fl8VlczlcLpfH5XMFXCFXxJVwCa6cq+SSXA1Xx6W4Rq6ZS3NtXAeX4fpFxsLYGAfjYjyMjwkwISbCJBiByTElRmIaTIdRmBEzYzRmwxwYg/lFp8VpczqcLqfH6XMGnCFnxJlwCs6cs+SUnA1nx6k4R86ZU3NunAen4fxFu8VuszvsLrvH7rMH7CF7xJ6wC/acvWSX7A17x67YR/aZXbNv7Ae7Yf9Fq8VqszqsLqvH6rMGrCFrxJqwCtactWSVrA1rx6pYR9aZVbNurAerYf1F2kLbaAftoj20jw7QITpCJ2iBztElWqIbdIdW6BE9ozV6Qx9og/5Fi8ViszgsLovH4rMELCFLxJKwCJacpWSRLA1Lx6JYRpaZRbNsLAeLYflFk8VkMzlMLpPH5DMFTCFTxJQwCaacqWSSTA1Tx6SYRqaZSTNtTAeTYfpFg8VgMzgMLoPH4DMEDCFDxJAwCIacoWSQDA1Dx6AYRoaZQTNsDAeDYfhFykLZKAflojyUjwpQISpCJSiBylElSqIaVIdSqBE1ozRqQx0og/pFvUVv0zv0Lr1H79MH9CF9RJ/QC/qcvqSX9A19R6/oR/qZXtNv9Ae9of9FrUVr0zq0Lq1H69MGtCFtRJvQCtqctqSVtA1tR6toR9qZVtNutAetof1FtUVtUzvULrVH7VMH1CF1RJ1QC+qcuqSW1A11R62oR+qZWlNv1Ae1of5F0kLaSAfpIj2kjwyQITJCJkiBzJElUiIbZIdUyBE5IzVyQx5Ig/xFlUVlUzlULpVH5VMFVCFVRJVQCaqcqqSSVA1VR6WoRqqZSlNtVAeVofpFhUVhUzgULoVH4VMEFCFFRJFQCIqcoqSQFA1FR6EoRoqZQlNsFAeFofhFmUVmkzlkLplH5pMFZCFZRJaQCbKcrCSTZA1ZR6bIRrKZTJNtZAeZIftFwkLYCAfhIjyEjwgQISJCJAiByBElQiIaRIdQiBExIzRiQxwIg/hFqUVqkzqkLqlH6pMGpCFpRJqQCtKctCSVpA1pR6pIR9KZVJNupAepIf1FsUVsEzvELrFH7BMHxCFxRJwQC+KcuCSWxA1xR6yIR+KZWBNvxAexIf4PnaE+bpE8K2sAAAAASUVORK5CYII=";

interface FixtureObject {
  key: string;
  size: number;
  contentType: string;
  updatedMs: number;
  metadata?: Record<string, string>;
  /** Text and JSON objects: their content (the server reads at most 1 MiB of it). */
  body?: () => string;
}

const LOG_LINE = "2026-10-20T09:30:00Z INFO request served path=/api/orders status=200 duration_ms=12\n";

const UPLOADS: FixtureObject[] = [
  { key: "robots.txt", size: 26, contentType: "text/plain; charset=utf-8", updatedMs: T0 - 86_400_000 * 40, body: () => "User-agent: *\nDisallow: /\n" },
  { key: "avatars/2026/ada.png", size: 48_213, contentType: "image/png", updatedMs: T0 - 3_600_000, metadata: { "uploaded-by": "web", "user-id": "u_1042" } },
  { key: "avatars/2026/grace.webp", size: 31_002, contentType: "image/webp", updatedMs: T0 - 7_200_000 },
  { key: "avatars/2026/logo.svg", size: 2_048, contentType: "image/svg+xml", updatedMs: T0 - 86_400_000 },
  { key: "avatars/legacy.jpg", size: 14 * MiB, contentType: "image/jpeg", updatedMs: T0 - 86_400_000 * 300 },
  {
    key: "docs/readme.txt",
    size: 74,
    contentType: "text/plain",
    updatedMs: T0 - 86_400_000 * 2,
    body: () => "Shop uploads.\n\nAvatars live under avatars/<year>/, exports in the exports bucket.\n",
  },
  {
    key: "docs/config.json",
    size: 58,
    contentType: "application/json",
    updatedMs: T0 - 86_400_000 * 3,
    metadata: { "cache-control": "no-store" },
    body: () => '{"feature":{"flags":["checkout-v2","avatars"]},"limit":3}',
  },
  { key: "docs/broken.json", size: 21, contentType: "application/json", updatedMs: T0 - 86_400_000 * 3, body: () => '{"feature": [1, 2,, }' },
  { key: "docs/server.log", size: 3 * MiB, contentType: "text/plain", updatedMs: T0 - 600_000, body: () => LOG_LINE.repeat(Math.ceil((3 * MiB) / LOG_LINE.length)).slice(0, 3 * MiB) },
  { key: "reports/q1.pdf", size: 1_572_864, contentType: "application/pdf", updatedMs: T0 - 86_400_000 * 90 },
  { key: "blobs/0001", size: 4_096, contentType: "", updatedMs: T0 - 86_400_000 * 5 },
  { key: "odd names/100% done #1?.txt", size: 12, contentType: "text/plain", updatedMs: T0 - 86_400_000, body: () => "done, mostly" },
  { key: "odd names/ünïcødé ✓.md", size: 19, contentType: "text/markdown", updatedMs: T0 - 86_400_000, body: () => "# ünïcødé ✓\n\nhello\n" },
  { key: "odd names/  leading spaces.txt", size: 3, contentType: "text/plain", updatedMs: T0 - 86_400_000, body: () => "hi\n" },
  { key: "odd names/a//b.txt", size: 5, contentType: "text/plain", updatedMs: T0 - 86_400_000, body: () => "deep\n" },
];

const EXPORTS: FixtureObject[] = Array.from({ length: 450 }, (_, i) => ({
  key: `daily/2026-${String(i + 1).padStart(3, "0")}.csv`,
  size: 10_000 + i * 37,
  contentType: "text/csv",
  updatedMs: T0 - (450 - i) * 86_400_000,
  body: () => "id,total\n1,42\n",
}));

const BUCKETS = [
  { name: "exports", runtime: BucketRuntimeKind.HOSTED, publicRead: false, reachable: true, unreachableReason: "" },
  { name: "media", runtime: BucketRuntimeKind.MINIO, publicRead: false, reachable: false, unreachableReason: "self-managed MinIO: not reachable from the control plane" },
  { name: "uploads", runtime: BucketRuntimeKind.HOSTED, publicRead: false, reachable: true, unreachableReason: "" },
];

const POLICIES: Record<AccessPolicyName, StorageAccessPolicy> = {
  open: StorageAccessPolicy.OPEN,
  audited: StorageAccessPolicy.AUDITED,
  deny: StorageAccessPolicy.DENY,
  unknown: StorageAccessPolicy.UNSPECIFIED,
};

const PREVIEW_KINDS: Record<PreviewKindName, ObjectPreviewKind> = {
  image: ObjectPreviewKind.IMAGE,
  text: ObjectPreviewKind.TEXT,
  json: ObjectPreviewKind.JSON,
  none: ObjectPreviewKind.UNSPECIFIED,
};

/** A small text object's size is its body's (the log, over the cap, keeps its stated 3 MiB). */
const sized = (object: FixtureObject): FixtureObject =>
  object.body && object.size < MiB ? { ...object, size: new TextEncoder().encode(object.body()).length } : object;

const CONTENTS: Record<string, FixtureObject[]> = {
  uploads: UPLOADS.map(sized).sort((a, b) => (a.key < b.key ? -1 : 1)),
  exports: EXPORTS,
};

const IMAGE_TYPES = new Set(["image/png", "image/jpeg", "image/gif", "image/webp"]);
const IMAGE_CAP = 10 * MiB;
const TEXT_CAP = MiB;

/** What GetObjectMetadata says a preview would be: the server's rule. */
function previewOf(object: FixtureObject): PreviewKindName {
  const type = object.contentType.split(";")[0]!.trim().toLowerCase();
  if (IMAGE_TYPES.has(type)) return object.size <= IMAGE_CAP ? "image" : "none";
  if (object.size > TEXT_CAP) return "none";
  if (type === "application/json") return "json";
  if (type.startsWith("text/")) return "text";
  return "none";
}

function reasonError(message: string, code: Code, reason?: string): ConnectError {
  return new ConnectError(message, code, reason ? new Headers({ "x-forge-error-reason": reason }) : undefined);
}

/** A page token: opaque to the client, sealed to what it was issued for. */
function seal(environmentId: string, bucket: string, prefix: string, index: number): string {
  return btoa(encodeURIComponent(JSON.stringify({ e: environmentId, b: bucket, p: prefix, i: index })));
}

function unseal(token: string, environmentId: string, bucket: string, prefix: string): number {
  try {
    const parsed = JSON.parse(decodeURIComponent(atob(token))) as { e?: string; b?: string; p?: string; i?: number };
    if (parsed.e === environmentId && parsed.b === bucket && parsed.p === prefix && typeof parsed.i === "number") return parsed.i;
  } catch {
    // Not one of ours.
  }
  throw reasonError("page_token was not issued for this bucket and prefix", Code.InvalidArgument, "bad_page_token");
}

/** One level under `prefix`: common prefixes and objects together, in lexical order (as S3 and GCS page them). */
function level(objects: FixtureObject[], prefix: string): ({ kind: "prefix"; name: string } | { kind: "object"; object: FixtureObject })[] {
  const prefixes = new Set<string>();
  const entries: ({ kind: "prefix"; name: string } | { kind: "object"; object: FixtureObject })[] = [];
  for (const object of objects) {
    if (!object.key.startsWith(prefix)) continue;
    const rest = object.key.slice(prefix.length);
    const slash = rest.indexOf("/");
    if (slash === -1) entries.push({ kind: "object", object });
    else prefixes.add(prefix + rest.slice(0, slash + 1));
  }
  for (const name of prefixes) entries.push({ kind: "prefix", name });
  return entries.sort((a, b) => {
    const x = a.kind === "prefix" ? a.name : a.object.key;
    const y = b.kind === "prefix" ? b.name : b.object.key;
    return x < y ? -1 : x > y ? 1 : 0;
  });
}

type Confirmed = { confirmEnvId?: string; reason?: string };

/** The confirmation a request carried, as the browser names it: absent when it sent none. */
function accessOf(req: Confirmed): { access?: AccessConfirmation } {
  return req.confirmEnvId === undefined && req.reason === undefined ? {} : { access: { confirmEnvId: req.confirmEnvId ?? "", reason: req.reason ?? "" } };
}

/** A request, recorded in the browser's terms (storageBrowserApi.ts ListObjectsInput / ObjectInput). */
type AnyRequest = Confirmed & { environmentId: string; bucket?: string; prefix?: string; pageToken?: string; pageSize?: number; openBucket?: boolean; key?: string };

function asked(method: Method, req: AnyRequest): unknown {
  if (method === "listBrowsableBuckets") return { environmentId: req.environmentId };
  const target = { environmentId: req.environmentId, bucket: req.bucket ?? "" };
  if (method === "listObjects") {
    return { ...target, prefix: req.prefix ?? "", pageToken: req.pageToken ?? "", pageSize: req.pageSize ?? 0, openBucket: req.openBucket ?? false, ...accessOf(req) };
  }
  return { ...target, key: req.key ?? "", ...accessOf(req) };
}

export function createStorageFixture(options: StorageFixtureOptions = {}): StorageFixture {
  const policies: Record<string, AccessPolicyName> = { "env-prod": "audited", "env-staging": "open", "env-dev": "open", ...options.policy } as Record<string, AccessPolicyName>;

  function begin(method: Method, key?: string) {
    const fail = options.fail;
    if (fail?.method === method && (!fail.keys || (key !== undefined && fail.keys.includes(key)))) {
      throw reasonError(fail.message ?? "refused", fail.code, fail.reason);
    }
  }

  function admit(environmentId: string, confirmed: Confirmed) {
    const policy = policies[environmentId] ?? "open";
    const access = accessOf(confirmed).access;
    if (policy === "deny" || policy === "unknown") throw new ConnectError("the environment's storage access policy denies reads", Code.PermissionDenied);
    if (policy === "audited" && (!access || access.confirmEnvId !== environmentId || !access.reason)) {
      throw reasonError("this environment is audited: confirm_env_id and a reason are required", Code.FailedPrecondition, "confirmation_required");
    }
  }

  function contents(bucket: string): FixtureObject[] {
    const declared = BUCKETS.find((candidate) => candidate.name === bucket);
    if (!declared) throw new ConnectError(`no bucket ${bucket} in this environment`, Code.NotFound);
    if (!declared.reachable) throw reasonError(declared.unreachableReason, Code.FailedPrecondition, "bucket_unreachable");
    return CONTENTS[bucket] ?? [];
  }

  function fixturePreview(object: FixtureObject): PreviewKindName {
    return options.truncatedText && object.key === "docs/server.log" ? "text" : previewOf(object);
  }

  function find(req: { environmentId: string; bucket: string; key: string } & Confirmed): FixtureObject {
    admit(req.environmentId, req);
    const object = contents(req.bucket).find((candidate) => candidate.key === req.key);
    if (!object) throw new ConnectError(`no object ${req.key}`, Code.NotFound);
    return object;
  }

  const fixture: StorageFixture = {
    calls: [],
    presigned: 0,

    async listBrowsableBuckets(req) {
      begin("listBrowsableBuckets");
      return {
        buckets: options.noBuckets ? [] : BUCKETS.map((bucket) => ({ ...bucket })),
        policy: POLICIES[policies[req.environmentId] ?? "open"] ?? StorageAccessPolicy.UNSPECIFIED,
      };
    },

    async listObjects(req) {
      begin("listObjects");
      admit(req.environmentId, req);
      const prefix = req.prefix ?? "";
      const entries = level(contents(req.bucket), prefix);
      const start = req.pageToken ? unseal(req.pageToken, req.environmentId, req.bucket, prefix) : 0;
      const size = req.pageSize || options.defaultPageSize || 200;
      const page = entries.slice(start, start + size);
      const end = start + page.length;
      return {
        prefixes: page.flatMap((entry) => (entry.kind === "prefix" ? [entry.name] : [])),
        objects: page.flatMap((entry) =>
          entry.kind === "object"
            ? [{ key: entry.object.key, size: BigInt(entry.object.size), updated: timestampFromMs(entry.object.updatedMs), etag: etagOf(entry.object) }]
            : []
        ),
        nextPageToken: end < entries.length ? seal(req.environmentId, req.bucket, prefix, end) : "",
      };
    },

    async getObjectMetadata(req) {
      begin("getObjectMetadata", req.key);
      const object = find(req);
      return {
        object: {
          key: object.key,
          size: BigInt(object.size),
          contentType: object.contentType,
          updated: timestampFromMs(object.updatedMs),
          etag: etagOf(object),
          metadata: { ...(object.metadata ?? {}) },
          preview: PREVIEW_KINDS[fixturePreview(object)],
        },
      };
    },

    async presignObjectDownload(req) {
      begin("presignObjectDownload", req.key);
      const object = find(req);
      fixture.presigned++;
      return {
        url: `https://storage.fixture.test/${req.bucket}/${encodeURIComponent(object.key)}?X-Goog-Signature=fixture-${fixture.presigned}`,
        filename: object.key.slice(object.key.lastIndexOf("/") + 1),
        size: BigInt(object.size),
        expiresAt: timestampFromMs(Date.now() + 5 * 60_000),
      };
    },

    async previewObject(req) {
      begin("previewObject", req.key);
      const object = find(req);
      const kind = fixturePreview(object);
      if (kind === "none") {
        const type = object.contentType.split(";")[0]!.trim().toLowerCase();
        if (IMAGE_TYPES.has(type) || type.startsWith("text/") || type === "application/json") {
          throw reasonError("over the preview size cap", Code.FailedPrecondition, "too_large");
        }
        throw reasonError("not an image, text or JSON object", Code.FailedPrecondition, "not_previewable");
      }
      const answer = { kind: PREVIEW_KINDS[kind], contentType: object.contentType, size: BigInt(object.size) };
      if (kind === "image") {
        return { ...answer, body: { case: "image", value: { url: FIXTURE_PNG, expiresAt: timestampFromMs(Date.now() + 2 * 60_000) } } };
      }
      const bytes = new TextEncoder().encode(object.body?.() ?? "");
      return { ...answer, body: { case: "content", value: bytes.slice(0, TEXT_CAP) } };
    },
  };

  return fixture;
}

/**
 * The fixture's StorageBrowserService, as mock-transport handlers: each call
 * is recorded (`asked`), then answered by the fixture's handler of that name,
 * read at call time so a test may have replaced it.
 */
export function storageFixtureHandlers(fixture: StorageFixture) {
  const record = (method: Method, req: AnyRequest) => fixture.calls.push({ method, req: asked(method, req) });
  return completed(
    StorageBrowserService,
    mockService(StorageBrowserService, {
      listBrowsableBuckets: (req) => (record("listBrowsableBuckets", req), fixture.listBrowsableBuckets(req)),
      listObjects: (req) => (record("listObjects", req), fixture.listObjects(req)),
      getObjectMetadata: (req) => (record("getObjectMetadata", req), fixture.getObjectMetadata(req)),
      presignObjectDownload: (req) => (record("presignObjectDownload", req), fixture.presignObjectDownload(req)),
      previewObject: (req) => (record("previewObject", req), fixture.previewObject(req)),
    })
  );
}

/** A transport serving only the fixture's StorageBrowserService: what the Storage tests' TransportScope reads. */
export function storageFixtureTransport(fixture: StorageFixture): Transport {
  return createMockTransport({ scenario: { name: "storage-fixture", handlers: storageFixtureHandlers(fixture) } });
}

function etagOf(object: FixtureObject): string {
  let hash = 0x811c9dc5;
  for (const char of object.key) hash = Math.imul(hash ^ char.charCodeAt(0), 0x01000193) >>> 0;
  return `"${hash.toString(16).padStart(8, "0")}${object.size.toString(16)}"`;
}
