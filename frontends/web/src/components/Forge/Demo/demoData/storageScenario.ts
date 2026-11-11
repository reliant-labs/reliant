// Copyright (c) 2025 Reliant Labs

/** Public sample files only. Never mint a URL, contact storage or accept a foreign scope. */
import { Code, ConnectError } from "@connectrpc/connect";
import { mockService } from "@reliantlabs/forge-web-runtime/mock-transport";
import { BucketRuntimeKind, ObjectPreviewKind, StorageAccessPolicy, StorageBrowserService } from "@/gen/controlplane/services/storage_browser/v1/storage_browser_pb";
import { completed } from "./demoServiceDefaults";
import { demoEnvId } from "./demoScope";

const files = {
  "README.txt": { type: "text/plain", text: "Synthetic shop uploads. These sample files never contact a real backend." },
  "orders/sample.json": { type: "application/json", text: '{"order":"demo-order-001","status":"paid","synthetic":true}' },
} as const;
/**
 * Which environments declare shop's one bucket. dev declares none, so the
 * demo shows the browser's "declares no storage buckets" state there (switch
 * the console to dev); prod (audited) and staging (open) browse `uploads`.
 */
const DECLARES_UPLOADS: ReadonlySet<string> = new Set(["env-staging", "env-prod"]);

const bytes = (key: keyof typeof files) => new TextEncoder().encode(files[key].text);

export function storageScenarioHandlers() {
  function admit(req: { environmentId: string; bucket: string; confirmEnvId?: string; reason?: string }) {
    if (!demoEnvId(req.environmentId) || !DECLARES_UPLOADS.has(req.environmentId) || req.bucket !== "uploads") throw new ConnectError("No such sample bucket.", Code.NotFound);
    if (req.environmentId === "env-prod" && (req.confirmEnvId !== req.environmentId || !req.reason?.trim())) throw new ConnectError("Sample prod reads need confirmation and a reason.", Code.FailedPrecondition);
  }
  function file(req: { key: string }) {
    if (!Object.hasOwn(files, req.key)) throw new ConnectError("No such sample file.", Code.NotFound);
    return req.key as keyof typeof files;
  }
  return completed(StorageBrowserService, mockService(StorageBrowserService, {
    listBrowsableBuckets: (req) => {
      if (!demoEnvId(req.environmentId)) throw new ConnectError("No such sample environment.", Code.NotFound);
      return { policy: req.environmentId === "env-prod" ? StorageAccessPolicy.AUDITED : StorageAccessPolicy.OPEN, buckets: DECLARES_UPLOADS.has(req.environmentId) ? [{ name: "uploads", runtime: BucketRuntimeKind.HOSTED, reachable: true, publicRead: false }] : [] };
    },
    listObjects: (req) => {
      admit(req);
      if (req.pageToken) throw new ConnectError("No such sample page.", Code.InvalidArgument);
      const prefix = req.prefix ?? "";
      if (prefix !== "" && prefix !== "orders/") throw new ConnectError("No such sample folder.", Code.NotFound);
      return { prefixes: prefix ? [] : ["orders/"], objects: Object.keys(files).filter((key) => key.startsWith(prefix) && !key.slice(prefix.length).includes("/")).map((key) => ({ key, size: BigInt(bytes(key as keyof typeof files).length), etag: "synthetic-sample" })), nextPageToken: "" };
    },
    getObjectMetadata: (req) => {
      admit(req); const key = file(req);
      return { object: { key, size: BigInt(bytes(key).length), contentType: files[key].type, etag: "synthetic-sample", metadata: { source: "public-demo-fixtures" }, preview: key.endsWith(".json") ? ObjectPreviewKind.JSON : ObjectPreviewKind.TEXT } };
    },
    previewObject: (req) => {
      admit(req); const key = file(req);
      return { kind: key.endsWith(".json") ? ObjectPreviewKind.JSON : ObjectPreviewKind.TEXT, contentType: files[key].type, size: BigInt(bytes(key).length), body: { case: "content", value: bytes(key) } };
    },
    // Issuance remains a mutation even for sample bytes. The demo never mints it.
    presignObjectDownload: () => { throw new ConnectError("This is a demo. Downloads and changes are turned off.", Code.PermissionDenied); },
  }));
}
