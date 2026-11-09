// Copyright (c) 2025 Reliant Labs
import { describe, expect, it, vi } from "vitest";
import { Code } from "@connectrpc/connect";
import { createDemoBrowserApis } from "../browserApis";

const request = { environmentId: "env-staging", database: "shop", table: "public.users", columns: [], filters: [], sort: [], limit: 25, offset: 0, count: "estimated" as const };
describe("isolated demo browser clients", () => {
  it("uses typed synthetic reads without any network, auth or live control-plane client", async () => {
    const fetch = vi.spyOn(globalThis, "fetch").mockRejectedValue(new Error("network forbidden"));
    try {
      const { data, storage } = createDemoBrowserApis();
      expect((await data.describeDatabase(request)).tables.map((table) => table.name)).toContain("users");
      expect((await data.browseTable(request)).rows.flat()).toContain("maya@example.com");
      expect((await storage.listBrowsableBuckets("env-staging")).buckets[0]?.name).toBe("uploads");
      const target = { environmentId: "env-staging", bucket: "uploads", key: "README.txt" };
      expect((await storage.getObjectMetadata(target)).metadata.source).toBe("public-demo-fixtures");
      expect(new TextDecoder().decode((await storage.previewObject(target)).content!)).toContain("Synthetic shop uploads");
      await expect(storage.presignObjectDownload(target)).rejects.toMatchObject({ code: Code.PermissionDenied });
      expect(fetch).not.toHaveBeenCalled();
    } finally { fetch.mockRestore(); }
  });

  it("refuses foreign scope and requires deliberate synthetic prod confirmation", async () => {
    const { data, storage } = createDemoBrowserApis();
    expect((await data.browseTable({ ...request, environmentId: "foreign-env" })).rows).toEqual([]);
    expect((await storage.listBrowsableBuckets("foreign-env")).buckets).toEqual([]);
    const target = { environmentId: "env-prod", bucket: "uploads", key: "README.txt" };
    await expect(storage.getObjectMetadata(target)).rejects.toMatchObject({ code: Code.FailedPrecondition });
    await expect(storage.getObjectMetadata({ ...target, access: { confirmEnvId: "env-staging", reason: "wrong env" } })).rejects.toMatchObject({ code: Code.FailedPrecondition });
    expect((await storage.getObjectMetadata({ ...target, access: { confirmEnvId: "env-prod", reason: "Inspect synthetic sample" } })).key).toBe("README.txt");
  });
});
