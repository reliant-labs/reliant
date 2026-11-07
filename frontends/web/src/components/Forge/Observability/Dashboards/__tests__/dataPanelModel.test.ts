// Copyright (c) 2025 Reliant Labs
import { describe, expect, it } from "vitest";
import type { DbTable } from "@/services/controlPlane/observability/appDatabaseApi";
import type { PanelResult } from "@/services/controlPlane/observability/types";
import { decodeFilter, decodeList } from "../../../Data/dataUrl-model";
import { dataHref, searchOf } from "../../../Data/__tests__/dataTestUtils";
import { consolePath, withSearch } from "../../../Console/paths";
import { browseParams, browseSearch, browseViewOf, panelDataTarget, resultRecord } from "../dataPanelModel";
import { dashboardFromJson, dashboardToJson } from "@/services/controlPlane/observability/dashboardsApi";

const table: DbTable = { schema: "public", name: "users", kind: "table", estimatedRows: 1, comment: "", primaryKey: ["tenant", "id"], foreignKeys: [], referencedBy: [], columns: ["tenant", "id", "a.%&"].map((name) => ({ name, pgType: "text", nullable: false, comment: "", isPrimaryKey: true })) };

describe("safe panel Data targets", () => {
  const hostile = "x,\\&env=prod#' OR 1=1 --";
  it("encodes hostile filter column/value through the data URL helpers", () => {
    const target = panelDataTarget({ database: "app", table: "public.users", column: "a.%&", fromField: "key" }, { key: hostile }, table)!;
    expect(decodeFilter(searchOf(dataHref(target)).f![0]!)).toEqual({ column: "a.%&", op: "eq", values: [hostile] });
    const scoped = withSearch(consolePath("shop & api", { kind: "data" }), { env: "staging", ...target });
    expect(scoped.split("?")[0]).toBe("/forge/p/shop%20%26%20api/data");
    expect(new URL(scoped, "https://console.test").searchParams.get("env")).toBe("staging");
  });
  it("uses all composite keys in order, including zero and false", () => {
    const link = { database: "app", table: "public.users", row: true, fromFields: ["tenant_key", "user_key"] };
    const target = panelDataTarget(link, { tenant_key: hostile, user_key: "id,1" }, table)!;
    expect(decodeList(searchOf(dataHref(target)).row!)).toEqual([hostile, "id,1"]);
    expect(decodeList(panelDataTarget(link, { tenant_key: 0, user_key: false }, table)!.row!)).toEqual(["0", "false"]);
    for (const record of [{ tenant_key: "t" }, { tenant_key: "t", user_key: null }, { tenant_key: "t", user_key: {} }]) expect(panelDataTarget(link, record, table)).toBeNull();
    expect(panelDataTarget({ ...link, fromFields: ["tenant_key"] }, { tenant_key: "t" }, table)).toBeNull();
    expect(panelDataTarget(link, {}, null)).toBeNull();
  });
  it("does not read prototype properties or invent absent result columns", () => {
    const link = { database: "app", table: "public.users", column: "id", fromField: "toString" };
    expect(panelDataTarget(link, {}, table)).toBeNull();
    const record = resultRecord({ columns: ["id", "missing"] } as PanelResult, { group: { tenant: "t" }, values: [0] });
    expect(record).toEqual({ id: 0, missing: undefined, tenant: "t" });
    expect(panelDataTarget({ ...link, fromField: "missing" }, record, table)).toBeNull();
  });
});

describe("saved browse descriptors", () => {
  it("round-trips additive link/browse fields through canonical dashboard transport", () => {
    const spec = { id: "saved", title: "Saved", panels: [
      { id: "rows", title: "Rows", kind: "table", link: { database: "app", table: "public.users", row: true, fromFields: ["tenant", "id"] } },
      { id: "view", title: "View", kind: "browse", browse: { database: "app", table: "public.users", columns: ["id"], filters: [{ column: "id", op: "eq", values: ["{owner}"] }], sort: [{ column: "id", desc: true }], size: 25 } },
    ] };
    expect(dashboardToJson(dashboardFromJson(JSON.parse(JSON.stringify(spec))))).toEqual(spec);
  });
  it("binds toggle defaults and explicit false without JavaScript truthiness", () => {
    const dashboard = { id: "d", title: "D", panels: [], filters: [{ id: "include", label: "Include", field: "", kind: "toggle" as const, default: true }] };
    expect(browseParams(dashboard, {}, { startMs: 0, endMs: 1000 }).include).toBe("true");
    expect(browseParams(dashboard, { variables: { include: "false" } }, { startMs: 0, endMs: 1000 }).include).toBe("false");
  });
  it("binds whole-value references as values, never SQL or substitution", () => {
    const value = "'; DROP TABLE users; --";
    const search = browseSearch({ database: "app", table: "public.users", filters: [{ column: "id", op: "eq", values: ["{user}"] }, { column: "tenant", op: "eq", values: ["prefix {user}"] }] }, { user: value }, table);
    expect(search.f!.map(decodeFilter)).toEqual([{ column: "id", op: "eq", values: [value] }, { column: "tenant", op: "eq", values: ["prefix {user}"] }]);
    expect(browseViewOf(search).filters?.[0]?.values).toEqual([value]);
  });
  it("rejects unknown references, invalid values and missing columns rather than widening", () => {
    const view = { database: "app", table: "public.users", filters: [{ column: "id", op: "eq" as const, values: ["{unknown}"] }] };
    expect(() => browseSearch(view, {}, table)).toThrow();
    expect(() => browseSearch(view, { unknown: 123 } as never, table)).toThrow();
    expect(() => browseSearch({ ...view, filters: [{ column: "absent", op: "eq", values: ["x"] }] }, {}, table)).toThrow();
    expect(() => browseSearch({ ...view, filters: [{ column: "id", op: "eq", values: [null] }] } as never, {}, table)).toThrow();
    expect(() => browseSearch({ ...view, sort: [{ column: "id", desc: "yes" }] } as never, {}, table)).toThrow();
    expect(browseSearch(view, { unknown: undefined }, table).f).toEqual([]);
  });
  it("binds only declared parameters with reserved from/to in RFC3339", () => {
    const params = browseParams({ id: "d", title: "D", panels: [], filters: [{ id: "owner", field: "owner", label: "Owner" }] }, { variables: { owner: "42", rogue: "x" } }, { startMs: 0, endMs: 1000 });
    expect(params).toEqual({ owner: "42", from: "1970-01-01T00:00:00.000Z", to: "1970-01-01T00:00:01.000Z" });
  });
});
