// Copyright (c) 2025 Reliant Labs

/**
 * The demo host's Data, Storage and Identity reads: the console's DEFAULT
 * clients (the ones production runs, over `connectClient`; Identity's are its
 * generated hooks' queries and writes on the app's QueryClient), with the shell
 * transport the demo installs at boot, the shop pack's (demoShellBoot.ts). No
 * demo client exists to test any more: these are the real ones.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Code } from "@connectrpc/connect";

import { createShopTransport } from "@/fixtures/shop";
import { demoSubjects } from "@/fixtures/shop/handlers/inventory";
import { DEMO_ORG } from "@/fixtures/shop/shopStory";
import { installShellTransport } from "@/lib/connect";
import { queryClient } from "@/lib/query-client";
import { IdentityService } from "@/gen/controlplane/services/identity/v1/identity_pb";
import { identityServiceMutations, identityServiceQueries } from "@/hooks/controlplane/identity-service-hooks_gen";
import { connectClient } from "@/lib/connect";
import { listIdentityUsersRequest, toIdentityStatus, toIdentityUser, toIdentityUsersPage, type IdentityUserFilter } from "@/services/controlPlane/identity/api";
import { currentOrgId } from "@/services/controlPlane/orgs";
import { controlPlaneDataBrowserApi as data } from "@/services/controlPlane/observability/DataBrowserApiContext";
import { StorageBrowserService } from "@/gen/controlplane/services/storage_browser/v1/storage_browser_pb";
import { storageBrowserServiceMutations } from "@/hooks/controlplane/storage-browser-service-hooks_gen";
import {
  objectRequest,
  toBucketList,
  toObjectMeta,
  toPresignedDownload,
  toPreview,
  type ObjectInput,
} from "@/services/controlPlane/storageBrowser/storageBrowserApi";

// The demo bundle's twin (services/controlPlane/config.demo.ts): the cloud reads are on.
vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://demo.invalid", hasControlPlane: true }));

/** The storage browser's calls as useStorageBrowser makes them, outside a scope: the reads on connectClient, the two writes through the generated calls. */
const storage = {
  listBrowsableBuckets: async (environmentId: string) => toBucketList(await connectClient(StorageBrowserService).listBrowsableBuckets({ environmentId })),
  getObjectMetadata: async (target: ObjectInput) => toObjectMeta(await connectClient(StorageBrowserService).getObjectMetadata(objectRequest(target))),
  previewObject: async (target: ObjectInput) => toPreview(await storageBrowserServiceMutations(queryClient).previewObject(objectRequest(target), { invalidate: false })),
  presignObjectDownload: async (target: ObjectInput) =>
    toPresignedDownload(await storageBrowserServiceMutations(queryClient).presignObjectDownload(objectRequest(target), { invalidate: false })),
};

/** The Identity tab's calls, outside React: the generated hooks' queries and writes on the app's QueryClient. */
const identity = {
  listUsers: async (orgId: string, envId: string, filter: IdentityUserFilter) =>
    toIdentityUsersPage(await queryClient.fetchQuery(identityServiceQueries.listUsers(listIdentityUsersRequest(orgId, envId, filter)))),
  getUser: async (orgId: string, envId: string, userId: string) =>
    toIdentityUser(await queryClient.fetchQuery(identityServiceQueries.getUser({ orgId, environmentId: envId, userId }))),
  getStatus: async (orgId: string, envId: string) =>
    toIdentityStatus(await queryClient.fetchQuery(identityServiceQueries.getIdentityStatus({ orgId, environmentId: envId }))),
  writes: () => identityServiceMutations(queryClient),
  // Never cached (a signed credential): the tab calls it on the module client too (hooks/identity-queries.ts).
  getExportURL: (orgId: string, envId: string) => connectClient(IdentityService).getExportURL({ orgId, environmentId: envId }),
};

const request = { environmentId: "env-staging", database: "shop", table: "public.customers", columns: [], filters: [], sort: [], limit: 25, offset: 0, count: "estimated" as const };

beforeEach(() => installShellTransport(createShopTransport()));
afterEach(() => {
  installShellTransport(null);
  queryClient.clear();
});

describe("the demo's browser reads, on the real clients over the shop transport", () => {
  it("uses typed synthetic reads without any network, auth or live control-plane client", async () => {
    const fetch = vi.spyOn(globalThis, "fetch").mockRejectedValue(new Error("network forbidden"));
    try {
      expect(await currentOrgId()).toBe(DEMO_ORG);
      expect((await identity.listUsers(DEMO_ORG, "env-staging", {})).users[0]?.email).toBe("jon@example.com");
      expect((await identity.listUsers(DEMO_ORG, "env-staging", { query: "lena.fischer" })).users[0]?.userId).toBe(demoSubjects()[0]);
      expect((await identity.getUser(DEMO_ORG, "env-staging", demoSubjects()[0]))?.userId).toBe(demoSubjects()[0]);
      // Another org or environment is answered empty (fixtures/shop/handlers/scope.ts): never shop's identity.
      expect((await identity.listUsers("foreign-org", "env-staging", {})).users).toEqual([]);
      expect(await identity.getStatus(DEMO_ORG, "foreign-env")).toMatchObject({ hasInstance: false, issuer: "", apps: [] });
      // Every write is refused by the transport's read-only guard, before the pack sees it.
      const writes = identity.writes();
      await expect(writes.deleteUser({ orgId: DEMO_ORG, environmentId: "env-staging", userId: demoSubjects()[0] })).rejects.toMatchObject({ code: Code.PermissionDenied });
      await expect(writes.inviteUser({ orgId: DEMO_ORG, environmentId: "env-staging", email: "new@example.com", roles: [] })).rejects.toMatchObject({ code: Code.PermissionDenied });
      await expect(writes.requestExport({ orgId: DEMO_ORG, environmentId: "env-staging" })).rejects.toMatchObject({ code: Code.PermissionDenied });
      // Read-named, but it signs a credential: the pack refuses it too.
      await expect(identity.getExportURL(DEMO_ORG, "env-staging")).rejects.toMatchObject({ code: Code.PermissionDenied });
      expect((await data.describeDatabase(request)).tables.map((table) => table.name)).toContain("customers");
      expect((await data.browseTable(request)).rows.flat()).toContain("lena.fischer@example.com");
      expect((await storage.listBrowsableBuckets("env-staging")).buckets[0]?.name).toBe("uploads");
      const target = { environmentId: "env-staging", bucket: "uploads", key: "README.txt" };
      expect((await storage.getObjectMetadata(target)).metadata.source).toBe("public-demo-fixtures");
      expect(new TextDecoder().decode((await storage.previewObject(target)).content!)).toContain("Synthetic shop uploads");
      await expect(storage.presignObjectDownload(target)).rejects.toMatchObject({ code: Code.PermissionDenied });
      expect(fetch).not.toHaveBeenCalled();
    } finally { fetch.mockRestore(); }
  });

  it("refuses foreign scope and requires deliberate synthetic prod confirmation", async () => {
    expect((await data.browseTable({ ...request, environmentId: "foreign-env" })).rows).toEqual([]);
    expect((await storage.listBrowsableBuckets("foreign-env")).buckets).toEqual([]);
    const target = { environmentId: "env-prod", bucket: "uploads", key: "README.txt" };
    await expect(storage.getObjectMetadata(target)).rejects.toMatchObject({ code: Code.FailedPrecondition });
    await expect(storage.getObjectMetadata({ ...target, access: { confirmEnvId: "env-staging", reason: "wrong env" } })).rejects.toMatchObject({ code: Code.FailedPrecondition });
    expect((await storage.getObjectMetadata({ ...target, access: { confirmEnvId: "env-prod", reason: "Inspect synthetic sample" } })).key).toBe("README.txt");
  });

  it("declares no bucket in dev, so the demo shows the empty Storage state there, and reads nothing from it", async () => {
    expect(await storage.listBrowsableBuckets("env-dev")).toMatchObject({ buckets: [], policy: "open" });
    expect((await storage.listBrowsableBuckets("env-prod")).buckets.map((bucket) => bucket.name)).toEqual(["uploads"]);
    await expect(storage.getObjectMetadata({ environmentId: "env-dev", bucket: "uploads", key: "README.txt" })).rejects.toMatchObject({ code: Code.NotFound });
  });
});
