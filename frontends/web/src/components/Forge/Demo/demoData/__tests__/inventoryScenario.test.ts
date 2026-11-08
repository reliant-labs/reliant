// Copyright (c) 2025 Reliant Labs
import { createClient } from "@connectrpc/connect";
import { loadAll } from "js-yaml";
import { describe, expect, it, vi } from "vitest";

import { AppDatabaseService, BrowseCount, BrowseFilterOp } from "@/gen/controlplane/services/app_database/v1/app_database_pb";
import { DeployService } from "@/gen/controlplane/services/deploy/v1/deploy_pb";
import { IdentityService } from "@/gen/controlplane/services/identity/v1/identity_pb";

import { demoBundleId, DEMO_WITHHELD } from "../configScenario";
import { createDemoTransport } from "../demoTransport";
import { DEMO_SUBJECTS } from "../inventoryScenario";
import { createDemoApi } from "../demoApi";

const transport = () => createDemoTransport({ source: "fixtures", url: "" });
const request = { environmentId: "env-dev", database: "shop", table: "public.chats", limit: 50 };
const cell = (value: { kind: { value?: unknown } }) => value.kind.value;

describe("public demo inventory wire reads", () => {
  it("serves sample reads locally; no fetch or write reaches any server", async () => {
    const fetch = vi.spyOn(globalThis, "fetch").mockRejectedValue(new Error("network forbidden"));
    try {
      const t = transport();
      const data = createClient(AppDatabaseService, t);
      expect((await data.listBrowsableDatabases({ environmentId: "env-dev" })).databases.map((d) => d.name)).toEqual(["shop"]);
      expect((await data.browseTable(request)).rows).toHaveLength(50);
      expect((await createClient(IdentityService, t).getUser({ environmentId: "env-dev", userId: DEMO_SUBJECTS[0] })).user?.email).toBe("maya@example.com");
      await expect(createClient(DeployService, t).getWorkloadConfig({ environmentId: "env-dev", workload: "api" })).resolves.toMatchObject({ releaseVersion: "v1.8.4" });
      await expect(createClient(IdentityService, t).deleteUser({ environmentId: "env-dev", userId: DEMO_SUBJECTS[0] })).rejects.toMatchObject({ code: 7 });
      expect(fetch).not.toHaveBeenCalled();
    } finally { fetch.mockRestore(); }
  });

  it("exposes PK/FKs in both directions and distinct synthetic identity subjects", async () => {
    const t = transport(); const data = createClient(AppDatabaseService, t);
    const desc = await data.describeDatabase({ environmentId: "env-dev", database: "shop" });
    const users = desc.tables.find((table) => table.name === "users")!;
    expect(users.primaryKey).toEqual(["id"]);
    expect(users.referencedBy.map((fk) => fk.table).sort()).toEqual(["chats", "machines"]);
    expect(users.columns.find((column) => column.name === "identity_subject")?.comment).toBe("forge:identity-subject=id");
    const row = await data.browseTable({ ...request, table: "public.users", columns: ["id", "identity_subject"] });
    expect(row.rows[0].values.map(cell)).toEqual(["customer-1", "subject-maya"]);
    const identity = createClient(IdentityService, t);
    expect((await identity.getUser({ environmentId: "env-dev", userId: "customer-1" })).user).toBeUndefined();
    const first = await identity.listUsers({ environmentId: "env-dev", pageSize: 1 });
    const next = await identity.listUsers({ environmentId: "env-dev", pageSize: 1, pageToken: first.nextPageToken });
    expect(first.users[0].userId).toBe("subject-maya");
    expect(next.users[0].userId).toBe("subject-jon");
    expect((await identity.listUsers({ environmentId: "env-dev", query: "PRIYA@" })).users).toHaveLength(1);
  });

  it("paginates stable row references without duplicates, with projection and counts", async () => {
    const data = createClient(AppDatabaseService, transport());
    const first = await data.browseTable({ ...request, columns: ["id", "message_count"], count: BrowseCount.EXACT });
    const second = await data.browseTable({ ...request, columns: ["id", "message_count"], offset: first.nextOffset });
    expect(first.total).toBe(123n); expect(first.nextOffset).toBe(50n); expect(first.hasMore).toBe(true);
    expect(first.columns.map((c) => c.name)).toEqual(["id", "message_count"]);
    expect(second.total).toBeUndefined();
    const ids = [...first.rows, ...second.rows].map((row) => cell(row.values[0]));
    expect(new Set(ids).size).toBe(100);
    const last = await data.browseTable({ ...request, offset: second.nextOffset });
    expect(last.rows).toHaveLength(23); expect(last.hasMore).toBe(false);
  });

  it("applies typed AND filters, numeric sort, null semantics and SQL LIKE wildcards", async () => {
    const data = createClient(AppDatabaseService, transport());
    const result = await data.browseTable({ ...request, table: "public.inventory", columns: ["quantity"], sort: [{ column: "quantity", desc: true }], filters: [{ column: "quantity", op: BrowseFilterOp.GT, values: ["9"] }, { column: "available", op: BrowseFilterOp.EQ, values: ["true"] }] });
    expect(result.rows.map((row) => cell(row.values[0]))).toEqual(Array.from({ length: 28 }, (_, i) => 93 - i * 3));
    const nulls = await data.browseTable({ ...request, count: BrowseCount.EXACT, filters: [{ column: "closed_at", op: BrowseFilterOp.IS_NULL }] });
    expect(nulls.total).toBe(98n);
    const neq = await data.browseTable({ ...request, filters: [{ column: "closed_at", op: BrowseFilterOp.NEQ, values: ["2026-01-10T12:00:00Z"] }] });
    expect(neq.rows).toHaveLength(0);
    const match = await data.browseTable({ ...request, count: BrowseCount.EXACT, filters: [{ column: "title", op: BrowseFilterOp.ILIKE, values: ["%ORDER_"] }, { column: "user_id", op: BrowseFilterOp.IN, values: ["customer-1", "customer-2"] }] });
    expect(match.total).toBe(41n);
    await expect(data.browseTable({ ...request, filters: [{ column: "message_count", op: BrowseFilterOp.EQ, values: ["10 OR true"] }] })).rejects.toMatchObject({ code: 3 });
    await expect(data.browseTable({ ...request, columns: ["password"] })).rejects.toMatchObject({ code: 3 });
    await expect(data.browseTable({ ...request, offset: -1n })).rejects.toMatchObject({ code: 3 });
  });

  it("requires prod confirmation plus reason, isolates foreign environments/databases", async () => {
    const data = createClient(AppDatabaseService, transport());
    await expect(data.describeDatabase({ environmentId: "env-prod", database: "shop" })).rejects.toMatchObject({ code: 7 });
    await expect(data.browseTable({ ...request, environmentId: "env-prod", confirmEnvId: "env-prod", reason: " " })).rejects.toMatchObject({ code: 7 });
    expect((await data.browseTable({ ...request, environmentId: "env-prod", confirmEnvId: "env-prod", reason: "Explore sample orders" })).auditId).toBe("demo-local-read");
    expect((await data.listBrowsableDatabases({ environmentId: "foreign-env" })).databases).toEqual([]);
    expect((await data.browseTable({ ...request, environmentId: "foreign-env" })).rows).toEqual([]);
    await expect(data.browseTable({ ...request, database: "private" })).rejects.toMatchObject({ code: 5 });
    expect((await createClient(IdentityService, transport()).getUser({ environmentId: "foreign-env", userId: DEMO_SUBJECTS[0] })).user).toBeUndefined();
  });

  it("shows the same current release and immutable bundle in workload rows, history and config", async () => {
    const api = createDemoApi({ wire: false });
    const deploy = createClient(DeployService, transport());
    for (const environmentId of ["env-dev", "env-staging", "env-prod"]) {
      const req = { orgId: "org-demo", projectId: "shop", envIds: [environmentId], filters: [], window: { startMs: Date.now() - 3_600_000, endMs: Date.now() } };
      const list = await api.listWorkloads(req);
      const detail = await api.getWorkload({ ...req, workload: "api" });
      const config = await deploy.getWorkloadConfig({ environmentId, workload: "api" });
      const promotions = await deploy.listPromotions({ environmentId });
      expect(list.workloads.find((w) => w.workload === "api")?.releaseVersion).toBe(config.releaseVersion);
      expect(detail.workloads[0].releaseVersion).toBe(config.releaseVersion);
      expect(detail.workloads[0].declared?.bundleId).toBe(config.bundleId);
      expect(detail.workloads[0].containers.every((c) => c.imageTag === config.releaseVersion)).toBe(true);
      expect(promotions.promotions[0].releaseVersion).toBe(config.releaseVersion);
    }
  });

  it("pins historical config, derives YAML from redacted tree, and never fingerprints withheld values", async () => {
    const deploy = createClient(DeployService, transport());
    const current = await deploy.getWorkloadConfig({ environmentId: "env-dev", workload: "api" });
    const historical = await deploy.getWorkloadConfig({ environmentId: "env-dev", workload: "api", releaseVersion: "v1.8.3" });
    const pinned = await deploy.getWorkloadConfig({ environmentId: "env-dev", workload: "api", bundleId: historical.bundleId });
    expect(pinned.objects).toEqual(historical.objects);
    expect(current.bundleId).toBe(demoBundleId("env-dev", "v1.8.4"));
    expect(current.bundleDigest).toMatch(/^sha256:[a-f0-9]{64}$/);
    expect(loadAll(current.renderedYaml)).toEqual(current.objects);
    expect(loadAll(historical.renderedYaml)).toEqual(historical.objects);
    expect(current.objects[0].data).toMatchObject({ ORDER_TIMEOUT_MS: "5000", DATABASE_URL: DEMO_WITHHELD, PAYMENTS_API_KEY: DEMO_WITHHELD });
    expect(historical.objects[0].data).toMatchObject({ ORDER_TIMEOUT_MS: "2000", DATABASE_URL: DEMO_WITHHELD });
    expect(current.renderedYaml).not.toMatch(/forge\.dev\/redacted:.*sha256/);
    await expect(deploy.getWorkloadConfig({ environmentId: "env-prod", workload: "api", bundleId: current.bundleId })).rejects.toMatchObject({ code: 5 });
    await expect(deploy.getWorkloadConfig({ environmentId: "env-dev", workload: "unknown" })).rejects.toMatchObject({ code: 5 });
  });
});
