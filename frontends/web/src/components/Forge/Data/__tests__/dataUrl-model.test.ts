// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import { forgeConsoleDataSearchSchema } from "@/routeSchemas";

import { FIXTURE_TABLES } from "../__fixtures__/fixtureDataApi";
import {
  browseRequestOf,
  decodeFilter,
  decodeList,
  decodeSort,
  effectiveSort,
  encodeFilter,
  encodeList,
  encodeSort,
  filtersOf,
  nextSort,
  isTextType,
  offsetOf,
  pageSizeOf,
  patchSearch,
  previousOffset,
  servable,
  requestedColumns,
  visibleColumns,
  type DataFilter,
} from "../dataUrl-model";

const orders = FIXTURE_TABLES.find((t) => t.name === "orders")!;
const items = FIXTURE_TABLES.find((t) => t.name === "order_items")!;

describe("filter tokens", () => {
  const cases: [DataFilter, string][] = [
    [{ column: "user_id", op: "eq", values: ["42"] }, "user_id.eq.42"],
    [{ column: "name", op: "ilike", values: ["%ada%"] }, "name.ilike.%ada%"],
    [{ column: "deleted_at", op: "is_null", values: [] }, "deleted_at.is_null"],
    [{ column: "deleted_at", op: "not_null", values: [] }, "deleted_at.not_null"],
    [{ column: "status", op: "in", values: ["open", "closed"] }, "status.in.open,closed"],
    // A value may hold dots, and IN values commas.
    [{ column: "host", op: "eq", values: ["api.example.com"] }, "host.eq.api.example.com"],
    [{ column: "tag", op: "in", values: ["a,b", "c\\d"] }, "tag.in.a\\,b,c\\\\d"],
    // A quoted column name may hold a dot or a percent sign.
    [{ column: "odd.name%", op: "gt", values: ["1"] }, "odd%2Ename%25.gt.1"],
    // The empty string is a value.
    [{ column: "notes", op: "eq", values: [""] }, "notes.eq."],
  ];
  it.each(cases)("round-trips %j as %s", (filter, token) => {
    expect(encodeFilter(filter)).toBe(token);
    expect(decodeFilter(token)).toEqual(filter);
  });

  it("drops what does not decode instead of failing", () => {
    expect(decodeFilter("user_id")).toBeNull();
    expect(decodeFilter("user_id.between.1")).toBeNull();
    expect(decodeFilter("user_id.eq")).toBeNull();
    expect(decodeFilter("status.in.")).toBeNull();
    expect(decodeFilter(".eq.1")).toBeNull();
  });

  it("keeps only filters on the table's columns", () => {
    expect(filtersOf({ f: ["status.eq.paid", "nope.eq.1", "garbage"] }, orders)).toEqual([{ column: "status", op: "eq", values: ["paid"] }]);
  });

  it("encodes the comma list and decodes it back", () => {
    for (const values of [["42"], ["org-1", "42"], ["a,b", "c"], ["back\\slash"], [""], ["", ""]]) {
      expect(decodeList(encodeList(values))).toEqual(values);
    }
  });
});

describe("sort", () => {
  it("round-trips tokens", () => {
    expect(encodeSort({ column: "created_at", desc: true })).toBe("created_at.desc");
    expect(decodeSort("created_at.desc")).toEqual({ column: "created_at", desc: true });
    expect(decodeSort("a%2Eb.asc")).toEqual({ column: "a.b", desc: false });
    expect(decodeSort("created_at.sideways")).toBeNull();
  });

  it("defaults to the primary key ascending, which the server does on its own", () => {
    expect(effectiveSort({}, items)).toEqual([
      { column: "order_id", desc: false },
      { column: "line", desc: false },
    ]);
    expect(effectiveSort({ sort: ["qty.desc"] }, items)).toEqual([{ column: "qty", desc: true }]);
  });

  it("cycles a column asc → desc → default, and keeps the URL clean at the default", () => {
    const start = effectiveSort({}, orders);
    const asc = nextSort(start, "status", orders);
    expect(asc).toEqual([{ column: "status", desc: false }]);
    const desc = nextSort(asc, "status", orders);
    expect(desc).toEqual([{ column: "status", desc: true }]);
    expect(nextSort(desc, "status", orders)).toEqual([]);
    // Clicking the key column from the default flips it.
    expect(nextSort(start, "id", orders)).toEqual([{ column: "id", desc: true }]);
  });

  it("adds a key with shift-click, and flips one in place", () => {
    const one = [{ column: "status", desc: false }];
    expect(nextSort(one, "created_at", orders, true)).toEqual([
      { column: "status", desc: false },
      { column: "created_at", desc: false },
    ]);
    expect(nextSort(one, "status", orders, true)).toEqual([{ column: "status", desc: true }]);
  });
});

describe("columns and paging", () => {
  it("shows the URL's columns that exist, in its order, else all", () => {
    expect(visibleColumns({ cols: ["status", "nope", "id"] }, orders)).toEqual(["status", "id"]);
    expect(visibleColumns({ cols: ["nope"] }, orders)).toEqual(orders.columns.map((c) => c.name));
    expect(visibleColumns({}, orders)).toEqual(orders.columns.map((c) => c.name));
  });

  it("asks for a hidden key so a row stays openable, and for everything when nothing is hidden", () => {
    expect(requestedColumns({ cols: ["status"] }, orders)).toEqual(["status", "id"]);
    expect(requestedColumns({ cols: ["qty"] }, items)).toEqual(["qty", "order_id", "line"]);
    expect(requestedColumns({}, orders)).toEqual([]);
    expect(requestedColumns({ cols: orders.columns.map((c) => c.name) }, orders)).toEqual([]);
  });

  it("pages by the URL's offset, with a known page size", () => {
    expect(pageSizeOf({})).toBe(50);
    expect(pageSizeOf({ size: 37 })).toBe(50);
    expect(pageSizeOf({ size: 200 })).toBe(200);
    expect(offsetOf({})).toBe(0);
    expect(offsetOf({ offset: 137 })).toBe(137);
    expect(offsetOf({ offset: -4 })).toBe(0);
    expect(offsetOf({ offset: 2_000_000 })).toBe(1_000_000);
  });

  it("reads a legacy page number as the offset it meant, and prefers an offset when both are there", () => {
    expect(offsetOf({ page: 3, size: 25 })).toBe(50);
    expect(offsetOf({ page: 3 })).toBe(100);
    expect(offsetOf({ page: 0 })).toBe(0);
    expect(offsetOf({ page: 999_999, size: 500 })).toBe(1_000_000);
    expect(offsetOf({ page: 3, offset: 7 })).toBe(7);
  });

  it("goes back a page size, never below zero, and knows the server's offset cap", () => {
    expect(previousOffset(137, 50)).toBe(87);
    expect(previousOffset(30, 50)).toBe(0);
    expect(servable(1_000_000)).toBe(true);
    expect(servable(1_000_001)).toBe(false);
  });

  it("offers LIKE on described text types only", () => {
    for (const t of ["text", "character varying(255)", "character varying", "character(3)", "citext", "name"]) expect(isTextType(t)).toBe(true);
    // Never the internal spellings' arrays, a number, or an array of text.
    for (const t of ["text[]", "bigint", "uuid", "jsonb", "timestamp with time zone"]) expect(isTextType(t)).toBe(false);
  });
});

describe("the BrowseTable request", () => {
  it("is what the URL says", () => {
    const req = browseRequestOf(
      { f: ["status.in.paid,shipped", "notes.not_null"], sort: ["total_cents.desc"], cols: ["status", "total_cents"], offset: 25, size: 25 },
      orders,
      { environmentId: "env-prod", database: "shop", count: "estimated", access: { confirmEnvId: "env-prod", reason: "why" } }
    );
    expect(req).toEqual({
      environmentId: "env-prod",
      database: "shop",
      table: "public.orders",
      columns: ["status", "total_cents", "id"],
      filters: [
        { column: "status", op: "in", values: ["paid", "shipped"] },
        { column: "notes", op: "not_null", values: [] },
      ],
      sort: [{ column: "total_cents", desc: true }],
      limit: 25,
      offset: 25,
      count: "estimated",
      access: { confirmEnvId: "env-prod", reason: "why" },
    });
  });

  it("sends no confirmation where none was given", () => {
    expect(browseRequestOf({}, orders, { environmentId: "e", database: "d", count: "none" })).not.toHaveProperty("access");
  });
});

describe("the route's search schema", () => {
  it("keeps the view, and drops a key that does not fit rather than failing the route", () => {
    expect(
      forgeConsoleDataSearchSchema.parse({ env: "prod", db: "shop", table: "public.orders", f: ["status.eq.paid", 42], sort: ["id.desc"], cols: ["id"], offset: 137, size: 100, row: 1042 })
    ).toEqual({ env: "prod", db: "shop", table: "public.orders", f: ["status.eq.paid", "42"], sort: ["id.desc"], cols: ["id"], offset: 137, size: 100, row: "1042" });
    // A link made before `offset` still parses.
    expect(forgeConsoleDataSearchSchema.parse({ table: "public.orders", page: 2 })).toEqual({ table: "public.orders", page: 2 });
    expect(forgeConsoleDataSearchSchema.parse({ size: 37, page: -1, offset: -1, f: "status.eq.paid", cols: [{}] })).toEqual({});
    expect(forgeConsoleDataSearchSchema.parse({ offset: 1_000_001 })).toEqual({});
  });
});

describe("patchSearch", () => {
  it("goes back to the first row when the matching rows change, and keeps it when only the size does", () => {
    expect(patchSearch({ table: "public.orders", offset: 150 }, { f: ["status.eq.paid"] })).toEqual({ table: "public.orders", f: ["status.eq.paid"] });
    expect(patchSearch({ table: "public.orders", offset: 150 }, { sort: ["id.desc"] })).toEqual({ table: "public.orders", sort: ["id.desc"] });
    expect(patchSearch({ table: "public.orders", offset: 150 }, { size: 100 })).toEqual({ table: "public.orders", offset: 150, size: 100 });
    expect(patchSearch({ table: "public.orders", offset: 150 }, { cols: ["id"] })).toEqual({ table: "public.orders", offset: 150, cols: ["id"] });
  });

  it("writes a legacy page as the offset it meant, under the page size it was made with", () => {
    expect(patchSearch({ table: "public.orders", page: 4, size: 25 }, { cols: ["id"] })).toEqual({ table: "public.orders", offset: 75, size: 25, cols: ["id"] });
    expect(patchSearch({ table: "public.orders", page: 4 }, { size: 100 })).toEqual({ table: "public.orders", offset: 150, size: 100 });
    expect(patchSearch({ table: "public.orders", page: 4 }, { offset: 200 })).toEqual({ table: "public.orders", offset: 200 });
  });

  it("clears a table's own state when the table changes, and everything when the database does", () => {
    const at = { db: "shop", table: "public.orders", f: ["status.eq.paid"], sort: ["id.desc"], cols: ["id"], offset: 50, row: "1001" };
    expect(patchSearch(at, { table: "public.users" })).toEqual({ db: "shop", table: "public.users" });
    expect(patchSearch(at, { db: "analytics" })).toEqual({ db: "analytics" });
  });

  it("drops defaults so a link stays short", () => {
    expect(patchSearch({ table: "t", offset: 50 }, { offset: 0, size: 50, f: [] })).toEqual({ table: "t" });
  });
});
