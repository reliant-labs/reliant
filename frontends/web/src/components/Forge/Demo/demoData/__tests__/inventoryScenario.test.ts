// Copyright (c) 2025 Reliant Labs
import { createClient } from "@connectrpc/connect";
import { loadAll } from "js-yaml";
import { describe, expect, it, vi } from "vitest";

import { AppDatabaseService, BrowseCount, BrowseFilterOp } from "@/gen/controlplane/services/app_database/v1/app_database_pb";
import { DeployService } from "@/gen/controlplane/services/deploy/v1/deploy_pb";
import { IdentityUserState } from "@/gen/controlplane/controlplane/v1/identity_pb";
import { IdentityService } from "@/gen/controlplane/services/identity/v1/identity_pb";

import { demoBundleId, DEMO_WITHHELD } from "../configScenario";
import { createDemoTransport } from "../demoTransport";
import { DEMO_SUBJECTS, demoIdentityUsers } from "../inventoryScenario";
import { DEMO_AUDIENCE_SIZE, demoAudience, demoAudienceTotals } from "../shop/audience";
import { createDemoApi } from "../demoApi";

const transport = () => createDemoTransport({ source: "fixtures", url: "" });
const request = { environmentId: "env-dev", database: "shop", table: "public.orders", limit: 50 };
const cell = (value: { kind: { value?: unknown } }) => value.kind.value;

describe("public demo inventory wire reads", () => {
  it("serves sample reads locally; no fetch or write reaches any server", async () => {
    const fetch = vi.spyOn(globalThis, "fetch").mockRejectedValue(new Error("network forbidden"));
    try {
      const t = transport();
      const data = createClient(AppDatabaseService, t);
      expect((await data.listBrowsableDatabases({ environmentId: "env-dev" })).databases.map((d) => d.name)).toEqual(["shop"]);
      expect((await data.browseTable({ ...request, environmentId: "env-staging" })).rows).toHaveLength(50);
      expect((await createClient(IdentityService, t).getUser({ environmentId: "env-dev", userId: DEMO_SUBJECTS[0] })).user?.email).toBe("lena.fischer@example.com");
      await expect(createClient(DeployService, t).getWorkloadConfig({ environmentId: "env-dev", workload: "api" })).resolves.toMatchObject({ releaseVersion: "v1.8.4" });
      await expect(createClient(IdentityService, t).deleteUser({ environmentId: "env-dev", userId: DEMO_SUBJECTS[0] })).rejects.toMatchObject({ code: 7 });
      expect(fetch).not.toHaveBeenCalled();
    } finally { fetch.mockRestore(); }
  });

  it("is a shop's schema: customers, products, stock, orders and order items, with PK/FKs both ways", async () => {
    const t = transport(); const data = createClient(AppDatabaseService, t);
    const desc = await data.describeDatabase({ environmentId: "env-dev", database: "shop" });
    expect(desc.tables.map((table) => table.name).sort()).toEqual(["customers", "order_items", "orders", "products", "stock"]);
    const customers = desc.tables.find((table) => table.name === "customers")!;
    expect(customers.primaryKey).toEqual(["id"]);
    expect(customers.referencedBy.map((fk) => fk.table)).toEqual(["orders"]);
    expect(desc.tables.find((table) => table.name === "products")!.referencedBy.map((fk) => fk.table).sort()).toEqual(["order_items", "stock"]);
    expect(desc.tables.find((table) => table.name === "stock")!.primaryKey).toEqual(["sku"]);
    expect(customers.columns.find((column) => column.name === "identity_subject")?.comment).toBe("forge:identity-subject=id");
    const row = await data.browseTable({ ...request, table: "public.customers", columns: ["id", "identity_subject"], filters: [{ column: "email", op: BrowseFilterOp.EQ, values: ["lena.fischer@example.com"] }] });
    expect(row.rows[0].values.map(cell)).toEqual(["cus_4f1a9c", DEMO_SUBJECTS[0]]);
    const identity = createClient(IdentityService, t);
    expect((await identity.getUser({ environmentId: "env-dev", userId: "cus_4f1a9c" })).user).toBeUndefined();
    const first = await identity.listUsers({ environmentId: "env-dev", pageSize: 1 });
    const next = await identity.listUsers({ environmentId: "env-dev", pageSize: 1, pageToken: first.nextPageToken });
    // Oldest first, as the real ListUsers sorts: the team that built shop, then its customers.
    expect(first.users[0].email).toBe("jon@example.com");
    expect(next.users[0].email).toBe("maya@example.com");
    expect((await identity.listUsers({ environmentId: "env-dev", query: "AIKO." })).users.map((u) => u.userId)).toEqual([DEMO_SUBJECTS[2]]);
  });

  it("lists the one audience every page counts: totals, pages and customers agree", async () => {
    const identity = createClient(IdentityService, transport());
    for (const env of ["env-prod", "env-staging", "env-dev"]) {
      const totals = demoAudienceTotals(env);
      const page = await identity.listUsers({ environmentId: env, pageSize: 100 });
      expect(page.totalSize, env).toBe(totals.accounts);
      expect(page.totalSize, env).toBe(DEMO_AUDIENCE_SIZE[env]);
      expect(page.users, env).toHaveLength(Math.min(100, totals.accounts));
      // Every customer in Data is an account, by its identity subject.
      const subjects = new Set(demoAudience(env).map((account) => account.id));
      expect(demoIdentityUsers(env).every((row) => subjects.has(String(row.identity_subject))), env).toBe(true);
      // Unique people on example.com, none signed in before they joined.
      const accounts = demoAudience(env);
      expect(new Set(accounts.map((a) => a.email)).size, env).toBe(accounts.length);
      expect(accounts.every((a) => a.email.endsWith("@example.com") && (a.lastSignInAtMs === null || a.lastSignInAtMs >= a.createdAtMs)), env).toBe(true);
    }
    expect(demoAudienceTotals("env-prod")).toMatchObject({ accounts: 1284, locked: 2, invited: 1, disabled: 1 });
    expect(demoAudienceTotals("env-staging")).toMatchObject({ locked: 1, invited: 0 });
    const locked = await identity.listUsers({ environmentId: "env-prod", state: IdentityUserState.LOCKED });
    expect(locked.totalSize).toBe(2);
    let token = "";
    let seen = 0;
    do {
      const page = await identity.listUsers({ environmentId: "env-prod", pageSize: 100, pageToken: token });
      seen += page.users.length;
      token = page.nextPageToken;
    } while (token);
    expect(seen).toBe(1284);
  });

  it("has real product names, and prod's only payment_failed orders are the incident's", async () => {
    const data = createClient(AppDatabaseService, transport());
    const products = await data.browseTable({ ...request, table: "public.products", columns: ["name"] });
    expect(products.rows.map((r) => String(cell(r.values[0]))).every((name) => !/ \/ \d+$/.test(name))).toBe(true);
    const failed = { ...request, count: BrowseCount.EXACT, filters: [{ column: "state", op: BrowseFilterOp.EQ, values: ["payment_failed"] }] };
    expect((await data.browseTable({ ...failed, environmentId: "env-staging" })).total).toBe(0n);
    const prod = await data.browseTable({ ...failed, environmentId: "env-prod", confirmEnvId: "env-prod", reason: "incident orders", columns: ["created_at"] });
    expect(prod.total).toBeGreaterThan(0n);
    for (const r of prod.rows) expect(Date.now() - Date.parse(String(cell(r.values[0])).replace(" ", "T").replace("+00", "Z"))).toBeLessThan(45 * 60_000);
  });

  it("paginates stable row references without duplicates, with projection and counts", async () => {
    const data = createClient(AppDatabaseService, transport());
    const items = { ...request, environmentId: "env-staging", table: "public.order_items", columns: ["id", "quantity"] };
    const first = await data.browseTable({ ...items, count: BrowseCount.EXACT });
    const second = await data.browseTable({ ...items, offset: first.nextOffset });
    expect(first.total).toBeGreaterThan(100n); expect(first.nextOffset).toBe(50n); expect(first.hasMore).toBe(true);
    expect(first.columns.map((c) => c.name)).toEqual(["id", "quantity"]);
    expect(second.total).toBeUndefined();
    const ids = [...first.rows, ...second.rows].map((row) => cell(row.values[0]));
    expect(new Set(ids).size).toBe(100);
    expect(ids).toEqual([...ids].sort((a, b) => Number(a) - Number(b)));
  });

  it("applies typed AND filters, numeric sort, null semantics and SQL LIKE wildcards", async () => {
    const data = createClient(AppDatabaseService, transport());
    const stock = await data.browseTable({ ...request, table: "public.stock", columns: ["on_hand"], sort: [{ column: "on_hand", desc: true }], filters: [{ column: "on_hand", op: BrowseFilterOp.GT, values: ["9"] }] });
    const onHand = stock.rows.map((row) => Number(cell(row.values[0])));
    expect(onHand.length).toBeGreaterThan(0);
    expect(onHand).toEqual([...onHand].sort((a, b) => b - a));
    expect(onHand.every((n) => n > 9)).toBe(true);
    const unpaid = await data.browseTable({ ...request, count: BrowseCount.EXACT, filters: [{ column: "paid_at", op: BrowseFilterOp.IS_NULL }] });
    const cancelled = await data.browseTable({ ...request, count: BrowseCount.EXACT, filters: [{ column: "state", op: BrowseFilterOp.IN, values: ["cancelled", "payment_failed"] }] });
    expect(unpaid.total).toBe(cancelled.total);
    const neq = await data.browseTable({ ...request, filters: [{ column: "paid_at", op: BrowseFilterOp.NEQ, values: ["2026-01-10T12:00:00Z"] }, { column: "paid_at", op: BrowseFilterOp.IS_NULL }] });
    expect(neq.rows).toHaveLength(0);
    const tees = await data.browseTable({ ...request, table: "public.products", count: BrowseCount.EXACT, filters: [{ column: "name", op: BrowseFilterOp.ILIKE, values: ["%COTTON TEE%"] }, { column: "sku", op: BrowseFilterOp.LIKE, values: ["TEE-ORG-_-INK"] }] });
    expect(tees.total).toBe(2n);
    await expect(data.browseTable({ ...request, filters: [{ column: "total_cents", op: BrowseFilterOp.EQ, values: ["10 OR true"] }] })).rejects.toMatchObject({ code: 3 });
    await expect(data.browseTable({ ...request, columns: ["password"] })).rejects.toMatchObject({ code: 3 });
    await expect(data.browseTable({ ...request, offset: -1n })).rejects.toMatchObject({ code: 3 });
  });

  it("keeps prices in cents on the wire, including typed price filtering and sorting", async () => {
    const data = createClient(AppDatabaseService, transport());
    const products = { ...request, table: "public.products", columns: ["sku", "price_cents"] };
    const filtered = await data.browseTable({ ...products, filters: [{ column: "price_cents", op: BrowseFilterOp.GTE, values: ["6900"] }], sort: [{ column: "price_cents", desc: true }] });
    expect(filtered.rows.map((row) => row.values.map(cell))).toEqual([["BLNK-WOOL-GRY", 11900], ["GRND-HAND-01", 8900], ["KETL-GOOSE-BLK", 6900]]);
    await expect(data.browseTable({ ...products, filters: [{ column: "price_cents", op: BrowseFilterOp.EQ, values: ["12.00"] }] })).rejects.toMatchObject({ code: 3 });
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
    expect(historical.objects[0].data).toMatchObject({ ORDER_TIMEOUT_MS: "3000", DATABASE_URL: DEMO_WITHHELD });
    expect(current.renderedYaml).not.toMatch(/forge\.dev\/redacted:.*sha256/);
    await expect(deploy.getWorkloadConfig({ environmentId: "env-prod", workload: "api", bundleId: current.bundleId })).rejects.toMatchObject({ code: 5 });
    await expect(deploy.getWorkloadConfig({ environmentId: "env-dev", workload: "unknown" })).rejects.toMatchObject({ code: 5 });
  });
});
