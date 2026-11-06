// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import type { DbTable } from "@/services/controlPlane/observability/appDatabaseApi";

import { FIXTURE_TABLES } from "../__fixtures__/fixtureDataApi";
import {
  cellText,
  compactCount,
  createLimiter,
  estimatedRowsLabel,
  findTable,
  foreignKeyTarget,
  formatCount,
  relatedCountLabel,
  relatedTarget,
  rowFilters,
  rowLabel,
  rowParamOf,
  rowValuesOf,
  totalLabel,
} from "../dataLinks-model";

const table = (name: string) => FIXTURE_TABLES.find((t) => `${t.schema}.${t.name}` === name)!;
const orders = table("public.orders");
const items = table("public.order_items");
const users = table("public.users");
const record = (entries: Record<string, unknown>) => new Map(Object.entries(entries)) as Map<string, never>;

describe("row addressing", () => {
  it("addresses a row by its key, in key order", () => {
    expect(rowParamOf(orders, record({ status: "paid", id: 1042 }))).toBe("1042");
    expect(rowParamOf(items, record({ line: 2, order_id: 1042, sku: "x" }))).toBe("1042,2");
  });

  it("escapes a key value holding a comma, and decodes it back", () => {
    const keyed: DbTable = { ...orders, primaryKey: ["org", "slug"] };
    const param = rowParamOf(keyed, record({ org: "acme, inc", slug: "a\\b" }))!;
    expect(param).toBe("acme\\, inc,a\\\\b");
    expect(rowValuesOf(keyed, param)).toEqual(["acme, inc", "a\\b"]);
  });

  it("has no address without a key, or with a NULL or unread key column", () => {
    expect(rowParamOf(table("public.order_totals"), record({ order_id: 1 }))).toBeNull();
    expect(rowParamOf(items, record({ order_id: 1042 }))).toBeNull();
    expect(rowParamOf(orders, record({ id: null }))).toBeNull();
  });

  it("refuses a row param that does not fit the key", () => {
    expect(rowValuesOf(items, "1042")).toBeNull();
    expect(rowValuesOf(items, "1042,2")).toEqual(["1042", "2"]);
    expect(rowValuesOf(table("public.order_totals"), "1")).toBeNull();
  });

  it("reads a row with one eq filter per key column, and names it", () => {
    expect(rowFilters(items, ["1042", "2"])).toEqual([
      { column: "order_id", op: "eq", values: ["1042"] },
      { column: "line", op: "eq", values: ["2"] },
    ]);
    expect(rowLabel(items, ["1042", "2"])).toBe("order_id 1042 · line 2");
  });

  it("turns a cell into the text a filter carries", () => {
    expect(cellText(42)).toBe("42");
    expect(cellText(true)).toBe("true");
    expect(cellText("x")).toBe("x");
    expect(cellText(null)).toBeNull();
    expect(cellText({ a: 1 })).toBe('{"a":1}');
  });
});

describe("foreign-key links", () => {
  it("leads to the referenced row when the key points at its primary key", () => {
    const fk = orders.foreignKeys[0]!;
    expect(foreignKeyTarget(fk, FIXTURE_TABLES, record({ user_id: "u-1" }))).toEqual({ table: "public.users", row: "u-1" });
  });

  it("leads to a filtered grid when it points at another unique column", () => {
    const fk = { ...orders.foreignKeys[0]!, refColumns: ["email"] };
    expect(foreignKeyTarget(fk, FIXTURE_TABLES, record({ user_id: "ada@example.com" }))).toEqual({ table: "public.users", f: ["email.eq.ada@example.com"] });
  });

  it("orders a composite key's values by the referenced key, not the constraint", () => {
    const parent: DbTable = { ...items, schema: "s", name: "parent", primaryKey: ["a", "b"] };
    const fk = { constraint: "c", schema: "s", table: "child", columns: ["pb", "pa"], refSchema: "s", refTable: "parent", refColumns: ["b", "a"] };
    expect(foreignKeyTarget(fk, [parent], record({ pa: "1", pb: "2" }))).toEqual({ table: "s.parent", row: "1,2" });
  });

  it("has no link for NULL, or into a table that is not readable", () => {
    const fk = orders.foreignKeys[0]!;
    expect(foreignKeyTarget(fk, FIXTURE_TABLES, record({ user_id: null }))).toBeNull();
    expect(foreignKeyTarget(fk, FIXTURE_TABLES.filter((t) => t !== users), record({ user_id: "u-1" }))).toBeNull();
  });

  it("links a Related entry to the referencing table filtered to this row", () => {
    const fk = orders.referencedBy.find((k) => k.table === "order_items")!;
    expect(relatedTarget(fk, record({ id: 1042 }))).toEqual({ table: "public.order_items", f: ["order_id.eq.1042"] });
    expect(relatedTarget(fk, record({ id: null }))).toBeNull();
  });

  it("finds a table by its qualified name", () => {
    expect(findTable(FIXTURE_TABLES, "billing.invoices")?.name).toBe("invoices");
    expect(findTable(FIXTURE_TABLES, "public.nope")).toBeNull();
    expect(findTable(FIXTURE_TABLES, undefined)).toBeNull();
  });
});

describe("counts", () => {
  it("compacts like a person reads them", () => {
    expect([0, 950, 1000, 1234, 12_345, 123_456, 1_234_567, 3_400_000_000].map(compactCount)).toEqual(["0", "950", "1k", "1.2k", "12k", "123k", "1.2M", "3.4B"]);
  });

  it("marks an estimate and a capped count", () => {
    expect(formatCount(1234, { estimate: true })).toBe("~1.2k");
    expect(formatCount(10_000, { lowerBound: true })).toBe("10,000+");
    expect(formatCount(12_345)).toBe("12,345");
    expect(estimatedRowsLabel(-1)).toBeNull();
    expect(estimatedRowsLabel(24_800)).toBe("~25k");
  });

  const page = (over: Partial<Parameters<typeof totalLabel>[0]>) => ({ total: null, totalIsEstimate: false, totalIsLowerBound: false, hasMore: true, rows: [[1]], ...over });

  it("says the grid's total as measured, and exactly on the last page", () => {
    expect(totalLabel(page({ total: 1180, totalIsEstimate: true }), 0)).toBe("~1.2k");
    expect(totalLabel(page({ total: 10_000, totalIsLowerBound: true }), 0)).toBe("10,000+");
    expect(totalLabel(page({ total: null }), 0)).toBeNull();
    // A last page proves the total.
    expect(totalLabel(page({ hasMore: false, rows: [[1], [2]] }), 50)).toBe("52");
    // An estimate below what is on screen is raised to it.
    expect(totalLabel(page({ total: 3, totalIsEstimate: true, rows: Array.from({ length: 50 }, () => [1]) }), 50)).toBe("~101");
    // An empty page past the end proves nothing.
    expect(totalLabel(page({ hasMore: false, rows: [], total: 120, totalIsEstimate: true }), 500)).toBe("~120");
  });

  it("says a Related count: none exactly, one exactly, more as an estimate never below what was seen", () => {
    expect(relatedCountLabel({ rows: [], hasMore: false, total: 4 })).toEqual({ label: "None", none: true });
    expect(relatedCountLabel({ rows: [[1]], hasMore: false, total: 0 })).toEqual({ label: "1", none: false });
    expect(relatedCountLabel({ rows: [[1]], hasMore: true, total: 0 })).toEqual({ label: "~2", none: false });
    expect(relatedCountLabel({ rows: [[1]], hasMore: true, total: 1500 })).toEqual({ label: "~1.5k", none: false });
  });
});

describe("createLimiter", () => {
  it("runs at most N at once, the rest in order as earlier ones settle", async () => {
    const gate = createLimiter(2);
    let running = 0;
    let peak = 0;
    const order: number[] = [];
    const resolvers: (() => void)[] = [];
    const task = (i: number) => () =>
      new Promise<number>((resolve) => {
        running++;
        peak = Math.max(peak, running);
        order.push(i);
        resolvers.push(() => {
          running--;
          resolve(i);
        });
      });
    const all = Promise.all([0, 1, 2, 3, 4].map((i) => gate(task(i))));
    await Promise.resolve();
    expect(order).toEqual([0, 1]);
    while (resolvers.length) {
      resolvers.shift()!();
      await new Promise((r) => setTimeout(r, 0));
    }
    expect(await all).toEqual([0, 1, 2, 3, 4]);
    expect(peak).toBe(2);
    expect(order).toEqual([0, 1, 2, 3, 4]);
  });

  it("frees the slot of a task that fails", async () => {
    const gate = createLimiter(1);
    await expect(gate(() => Promise.reject(new Error("no")))).rejects.toThrow("no");
    await expect(gate(() => Promise.resolve("next"))).resolves.toBe("next");
  });
});
