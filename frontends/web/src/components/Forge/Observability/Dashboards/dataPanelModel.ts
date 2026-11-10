// Copyright (c) 2025 Reliant Labs

import type { BrowseView, Dashboard, Panel, PanelLink, PanelResult, TimeWindow } from "@/services/controlPlane/observability/types";
import type { DbTable } from "@/services/controlPlane/observability/appDatabaseApi";
import { DATA_PAGE_SIZES, decodeFilter, decodeSort, encodeFilter, encodeList, encodeSort, FILTER_OPS, type DataSearch } from "../../Data/dataUrl-model";
import { viewerFilterValue } from "./viewerModel";
import type { DashboardSearch } from "./dashboardModel";

/** A result field is present, scalar and non-null; absent keys never become the string 'undefined'. */
export function resultRecord(result: PanelResult, row: PanelResult["rows"][number]): Record<string, unknown> {
  const record: Record<string, unknown> = Object.create(null);
  result.columns.forEach((column, index) => { record[column] = row.values[index]; });
  Object.assign(record, row.group);
  return record;
}

export function panelDataTarget(link: PanelLink, record: Record<string, unknown>, table: DbTable | null): DataSearch | null {
  if (!table || `${table.schema}.${table.name}` !== link.table || !link.database) return null;
  const columns = new Set(table.columns.map((column) => column.name));
  const valueOf = (field: string): string | null => {
    if (!Object.hasOwn(record, field)) return null;
    const value = record[field];
    return typeof value === "string" || typeof value === "boolean" || (typeof value === "number" && Number.isFinite(value)) ? String(value) : null;
  };
  const base = { db: link.database, table: link.table };
  if (link.row) {
    const fields = link.fromFields;
    if (link.column || link.fromField || !Array.isArray(fields) || !fields.length || fields.some((field) => typeof field !== "string") || fields.length !== table.primaryKey.length || table.primaryKey.some((key) => !columns.has(key))) return null;
    const values = fields.map(valueOf);
    return values.some((value) => value === null) ? null : { ...base, row: encodeList(values as string[]) };
  }
  if (link.fromFields?.length || !link.column || !columns.has(link.column) || !link.fromField) return null;
  const value = valueOf(link.fromField);
  return value === null ? null : { ...base, f: [encodeFilter({ column: link.column, op: "eq", values: [value] })] };
}

export function browseParams(dashboard: Dashboard, search: DashboardSearch, window: TimeWindow): Record<string, string | undefined> {
  const values: Record<string, string | undefined> = Object.create(null);
  for (const filter of dashboard.filters) {
    const value = filter.kind === "toggle" ? search.variables?.[filter.id] ?? String(filter.default ?? false) : viewerFilterValue(filter, search);
    // A malformed URL toggle must not become a truthy SQL-ish expression.
    values[filter.id] = filter.kind === "toggle" && value !== "true" && value !== "false" ? String(filter.default ?? false) : value;
  }
  values.from = new Date(window.startMs).toISOString();
  values.to = new Date(window.endMs).toISOString();
  return values;
}

/** Fail closed on a malformed descriptor; never drop a bad filter into a wider request. */
export function browseSearch(view: BrowseView, params: Record<string, string | undefined>, table: DbTable): DataSearch {
  const fail = (): never => { throw new Error("Saved view has invalid columns, filters or parameters"); };
  if (!view || typeof view.database !== "string" || !view.database || view.table !== `${table.schema}.${table.name}`) return fail();
  const known = new Set(table.columns.map((column) => column.name));
  const checkColumn = (name: unknown): name is string => typeof name === "string" && known.has(name);
  const size = view.size ?? 50;
  if (!(DATA_PAGE_SIZES as readonly number[]).includes(size)) return fail();
  if (view.columns && (!Array.isArray(view.columns) || view.columns.some((name) => !checkColumn(name)))) return fail();
  if ((view.filters && !Array.isArray(view.filters)) || (view.sort && !Array.isArray(view.sort))) return fail();
  const filters = (view.filters ?? []).flatMap((filter) => {
    if (!filter || !checkColumn(filter.column)) return fail();
    const spec = FILTER_OPS.find((op) => op.op === filter.op);
    const raw = filter.values ?? [];
    if (!spec || !Array.isArray(raw) || raw.some((value) => typeof value !== "string") || (spec.arity === "none" ? raw.length !== 0 : spec.arity === "one" ? raw.length !== 1 : !raw.length || raw.length > 100)) return fail();
    let unset = false;
    const values = raw.map((value) => {
      const ref = /^\{([A-Za-z0-9][A-Za-z0-9_.-]*)\}$/.exec(value)?.[1];
      if (!ref) return value;
      if (!Object.hasOwn(params, ref)) return fail();
      const bound = params[ref];
      if (bound === undefined || bound === "") { unset = true; return ""; }
      if (typeof bound !== "string") return fail();
      return bound;
    });
    return unset ? [] : [encodeFilter({ column: filter.column, op: filter.op, values })];
  });
  const sort = (view.sort ?? []).map((item) => {
    if (!item || !checkColumn(item.column) || (item.desc !== undefined && typeof item.desc !== "boolean")) return fail();
    return encodeSort({ column: item.column, desc: item.desc ?? false });
  });
  return { db: view.database, table: view.table, cols: view.columns, f: filters, sort, size };
}

/** Snapshot the browser's current structured view; row selection/pagination are not saved. */
export function browseViewOf(search: DataSearch): BrowseView {
  if (!search.db || !search.table) throw new Error("Choose a database and table first");
  const filters = (search.f ?? []).map(decodeFilter);
  const sort = (search.sort ?? []).map(decodeSort);
  if (filters.some((filter) => !filter) || sort.some((item) => !item)) throw new Error("The view contains an invalid filter or sort");
  // Exact {name} strings would acquire parameter semantics when saved. Refuse rather than change their meaning.
  if (filters.some((filter) => filter?.values.some((value) => /^\{[A-Za-z0-9][A-Za-z0-9_.-]*\}$/.test(value)))) throw new Error("Literal parameter-shaped values cannot be saved as a view");
  return { database: search.db, table: search.table, columns: search.cols, filters: filters as NonNullable<BrowseView["filters"]>, sort: sort as NonNullable<BrowseView["sort"]>, size: search.size ?? 50 };
}

export function linkablePanel(panel: Panel): boolean {
  return panel.kind === "table" || panel.kind === "bar" || (panel.kind === "postgres" && [undefined, "auto", "table", "bar"].includes(panel.postgres?.draw));
}
