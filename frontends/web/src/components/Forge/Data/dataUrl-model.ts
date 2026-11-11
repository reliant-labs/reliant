// Copyright (c) 2025 Reliant Labs

/**
 * THE DATA BROWSER'S URL STATE, and the BrowseTable request it means.
 *
 * Every view is a link (docs/data-browser.md in control-plane: "a filtered
 * grid or a row is a shareable link"), so the page keeps nothing of its own
 * that changes what is shown. The keys, beside the console scope `env`:
 *
 *   db     the database (a BrowsableDatabase name)
 *   table  schema-qualified, unquoted: `public.chats`
 *   f      filters, one token each: `<column>.<op>[.<value>]`
 *            user_id.eq.42   name.ilike.%ada%   deleted_at.is_null
 *            status.in.open,closed      (IN: a comma list; `\,` is a comma)
 *   sort   one token each, in priority order: `<column>.asc` / `<column>.desc`
 *          (absent: the primary key ascending, the server's default)
 *   cols   the visible columns, in order (absent: every readable column)
 *   offset rows skipped before the page (absent: 0). An offset, not a page
 *          number, because the server may cut a page short of its size to
 *          keep the response small: the next page starts at its next_offset,
 *          which no page number can say. So a link opens on exactly the rows
 *          it was copied from.
 *   page   LEGACY, read only: a 1-based page number from a link made before
 *          `offset`; it means offset (page-1)*size, and the first change to
 *          the grid writes it as `offset`.
 *   size   rows per page, one of DATA_PAGE_SIZES (absent: 50)
 *   row    the open row's primary-key values, in key order, as a comma list
 *          (`\,` a comma, `\\` a backslash): `42`, or `org-1,42` for a
 *          composite key
 *
 * A column name may hold a `.` or a `%` (a quoted identifier), so in a token
 * the column is escaped (`%2E`, `%25`); the op and value are whatever follow.
 *
 * Parsing never throws: a token that does not decode is dropped, so a link
 * from a newer build, or one edited by hand, opens a wider view rather than
 * an error page — the same rule routeSchemas holds for every console key.
 */

import type { BrowseCountMode, BrowseInput, BrowseOp, DataAccessConfirmation, DbTable } from "@/services/controlPlane/observability/appDatabaseApi";

export const DATA_PAGE_SIZES = [25, 50, 100, 200, 500] as const;
export const DEFAULT_PAGE_SIZE = 50;
/** BrowseTable refuses an offset past this: narrow with a filter or a sort instead. */
export const MAX_OFFSET = 1_000_000;

export interface DataSearch {
  /** Explicit Identity selection, bound to a control-plane environment id. Never an audit reason. */
  subject?: string;
  db?: string;
  table?: string;
  f?: string[];
  sort?: string[];
  cols?: string[];
  offset?: number;
  /** Legacy: read as offset (page-1)*size, never written. */
  page?: number;
  size?: number;
  row?: string;
}

/** Every DataSearch key: what a link out of the browser carries, and what leaving it clears. */
export const DATA_SEARCH_KEYS = ["subject", "db", "table", "f", "sort", "cols", "offset", "page", "size", "row"] as const satisfies readonly (keyof DataSearch)[];

export interface DataFilter {
  column: string;
  op: BrowseOp;
  values: string[];
}

export interface DataSort {
  column: string;
  desc: boolean;
}

export interface FilterOpSpec {
  op: BrowseOp;
  /** The menu's words. */
  label: string;
  /** The chip's: `id = 42`. */
  symbol: string;
  /** How many values it takes: none (IS NULL), one, or a list (IN). */
  arity: "none" | "one" | "many";
  /** LIKE / ILIKE: the server accepts them on text columns only. */
  textOnly?: boolean;
}

/** The ops of BrowseFilterOp, in the order the filter builder offers them. */
export const FILTER_OPS: readonly FilterOpSpec[] = [
  { op: "eq", label: "equals", symbol: "=", arity: "one" },
  { op: "neq", label: "does not equal", symbol: "≠", arity: "one" },
  { op: "gt", label: "greater than", symbol: ">", arity: "one" },
  { op: "gte", label: "greater than or equal", symbol: "≥", arity: "one" },
  { op: "lt", label: "less than", symbol: "<", arity: "one" },
  { op: "lte", label: "less than or equal", symbol: "≤", arity: "one" },
  { op: "ilike", label: "matches (any case)", symbol: "ilike", arity: "one", textOnly: true },
  { op: "like", label: "matches (exact case)", symbol: "like", arity: "one", textOnly: true },
  { op: "in", label: "is one of", symbol: "in", arity: "many" },
  { op: "is_null", label: "is NULL", symbol: "is NULL", arity: "none" },
  { op: "not_null", label: "is not NULL", symbol: "is not NULL", arity: "none" },
];

const OP_SPECS = new Map(FILTER_OPS.map((spec) => [spec.op, spec]));

export function opSpec(op: BrowseOp): FilterOpSpec {
  return OP_SPECS.get(op)!;
}

const TEXT_TYPES = new Set(["text", "character varying", "varchar", "character", "char", "bpchar", "name", "citext"]);

/**
 * Whether a DESCRIBED type (DescribeDatabase's spelling: "text",
 * "character varying(40)", "character(3)", "citext") is text, which is what
 * LIKE and ILIKE accept. An array ("text[]") is not.
 */
export function isTextType(described: string): boolean {
  const base = described.trim().replace(/\(\d+\)$/, "");
  return TEXT_TYPES.has(base);
}

// ── Escaping ────────────────────────────────────────────────────────────────

function escapeColumn(column: string): string {
  return column.replace(/%/g, "%25").replace(/\./g, "%2E");
}

function unescapeColumn(column: string): string {
  return column.replace(/%2E/gi, ".").replace(/%25/g, "%");
}

/** A comma list where `\,` is a comma and `\\` a backslash: IN values and a row's key values. */
export function encodeList(values: readonly string[]): string {
  return values.map((value) => value.replace(/\\/g, "\\\\").replace(/,/g, "\\,")).join(",");
}

export function decodeList(text: string): string[] {
  const out: string[] = [];
  let current = "";
  for (let i = 0; i < text.length; i++) {
    const ch = text[i]!;
    if (ch === "\\" && i + 1 < text.length) {
      current += text[++i];
    } else if (ch === ",") {
      out.push(current);
      current = "";
    } else {
      current += ch;
    }
  }
  out.push(current);
  return out;
}

// ── Filters and sort ────────────────────────────────────────────────────────

export function encodeFilter(filter: DataFilter): string {
  const head = `${escapeColumn(filter.column)}.${filter.op}`;
  const spec = opSpec(filter.op);
  if (spec.arity === "none") return head;
  return `${head}.${spec.arity === "one" ? (filter.values[0] ?? "") : encodeList(filter.values)}`;
}

/** A token back into a filter, or null when it does not decode (an unknown op, a missing value). */
export function decodeFilter(token: string): DataFilter | null {
  const first = token.indexOf(".");
  if (first <= 0) return null;
  const column = unescapeColumn(token.slice(0, first));
  const rest = token.slice(first + 1);
  const second = rest.indexOf(".");
  const op = (second < 0 ? rest : rest.slice(0, second)) as BrowseOp;
  const spec = OP_SPECS.get(op);
  if (!spec) return null;
  const value = second < 0 ? null : rest.slice(second + 1);
  switch (spec.arity) {
    case "none":
      return { column, op, values: [] };
    case "one":
      return value === null ? null : { column, op, values: [value] };
    case "many": {
      if (value === null || value === "") return null;
      return { column, op, values: decodeList(value) };
    }
  }
}

export function encodeSort(sort: DataSort): string {
  return `${escapeColumn(sort.column)}.${sort.desc ? "desc" : "asc"}`;
}

export function decodeSort(token: string): DataSort | null {
  const dot = token.lastIndexOf(".");
  if (dot <= 0) return null;
  const direction = token.slice(dot + 1);
  if (direction !== "asc" && direction !== "desc") return null;
  return { column: unescapeColumn(token.slice(0, dot)), desc: direction === "desc" };
}

/** The URL's filters that decode and name a column of `table`. */
export function filtersOf(search: DataSearch, table: Pick<DbTable, "columns"> | null): DataFilter[] {
  const known = table ? new Set(table.columns.map((c) => c.name)) : null;
  return (search.f ?? []).flatMap((token) => {
    const filter = decodeFilter(token);
    return filter && (!known || known.has(filter.column)) ? [filter] : [];
  });
}

/** The URL's sort, or [] for the default (the primary key ascending). */
export function sortOf(search: DataSearch, table: Pick<DbTable, "columns"> | null): DataSort[] {
  const known = table ? new Set(table.columns.map((c) => c.name)) : null;
  const seen = new Set<string>();
  return (search.sort ?? []).flatMap((token) => {
    const sort = decodeSort(token);
    if (!sort || seen.has(sort.column) || (known && !known.has(sort.column))) return [];
    seen.add(sort.column);
    return [sort];
  });
}

/** The order the grid is actually in: the URL's, else the primary key ascending (what the server does). */
export function effectiveSort(search: DataSearch, table: Pick<DbTable, "columns" | "primaryKey">): DataSort[] {
  const explicit = sortOf(search, table);
  return explicit.length > 0 ? explicit : table.primaryKey.map((column) => ({ column, desc: false }));
}

/**
 * The sort after a header click. A plain click sorts by that column alone:
 * ascending, then descending, then back to the default. With `additive`
 * (shift-click) it adds the column as the next key, or flips it in place.
 * A result equal to the default is [] so the URL stays clean.
 */
export function nextSort(current: readonly DataSort[], column: string, table: Pick<DbTable, "primaryKey">, additive = false): DataSort[] {
  const defaults = table.primaryKey.map((name) => ({ column: name, desc: false }));
  const index = current.findIndex((sort) => sort.column === column);
  let next: DataSort[];
  if (additive) {
    next =
      index < 0
        ? [...current, { column, desc: false }]
        : current.map((sort, i) => (i === index ? { column, desc: !sort.desc } : sort));
  } else if (current.length === 1 && index === 0) {
    next = current[0]!.desc ? [] : [{ column, desc: true }];
  } else {
    next = [{ column, desc: false }];
  }
  const sameAsDefault = next.length === defaults.length && next.every((sort, i) => sort.column === defaults[i]!.column && sort.desc === defaults[i]!.desc);
  return sameAsDefault ? [] : next;
}

// ── Columns and paging ──────────────────────────────────────────────────────

/** The visible columns, in order: the URL's that the table has, else every column. */
export function visibleColumns(search: DataSearch, table: Pick<DbTable, "columns">): string[] {
  const known = new Set(table.columns.map((c) => c.name));
  const chosen = (search.cols ?? []).filter((name, i, all) => known.has(name) && all.indexOf(name) === i);
  return chosen.length > 0 ? chosen : table.columns.map((c) => c.name);
}

/**
 * The columns to ask for: the visible ones, plus the primary key's when the
 * picker hid them — a row's link is built from its key, so the grid needs
 * the key whether or not it shows it. [] (every column) when nothing is hidden.
 */
export function requestedColumns(search: DataSearch, table: Pick<DbTable, "columns" | "primaryKey">): string[] {
  const visible = visibleColumns(search, table);
  if (!search.cols || visible.length === table.columns.length) return [];
  return [...visible, ...table.primaryKey.filter((key) => !visible.includes(key))];
}

export function pageSizeOf(search: DataSearch): number {
  return (DATA_PAGE_SIZES as readonly number[]).includes(search.size ?? -1) ? search.size! : DEFAULT_PAGE_SIZE;
}

/** The rows skipped before the page: the URL's offset, else a legacy page number's, within what the server serves. */
export function offsetOf(search: DataSearch): number {
  if (search.offset !== undefined) {
    return Number.isInteger(search.offset) && search.offset > 0 ? Math.min(search.offset, MAX_OFFSET) : 0;
  }
  const page = search.page ?? 1;
  return Number.isInteger(page) && page > 1 ? Math.min((page - 1) * pageSizeOf(search), MAX_OFFSET) : 0;
}

/**
 * Where Previous goes: a page size back, never below 0. A page before this one
 * may have been cut short, so going back can show a few rows again; it never
 * skips one.
 */
export function previousOffset(offset: number, size: number): number {
  return Math.max(0, offset - size);
}

/** Whether the server will serve a page at `offset` (it refuses past MAX_OFFSET). */
export function servable(offset: number): boolean {
  return offset <= MAX_OFFSET;
}

// ── The request ─────────────────────────────────────────────────────────────

export interface BrowseContext {
  environmentId: string;
  database: string;
  count: BrowseCountMode;
  access?: DataAccessConfirmation;
}

/** The grid's BrowseTable request for the URL's state over `table`. */
export function browseRequestOf(search: DataSearch, table: DbTable, ctx: BrowseContext): BrowseInput {
  return {
    environmentId: ctx.environmentId,
    database: ctx.database,
    table: `${table.schema}.${table.name}`,
    columns: requestedColumns(search, table),
    filters: filtersOf(search, table),
    sort: sortOf(search, table),
    limit: pageSizeOf(search),
    offset: offsetOf(search),
    count: ctx.count,
    ...(ctx.access ? { access: ctx.access } : {}),
  };
}

/**
 * The search to navigate to after a change to the grid. Anything that changes
 * WHICH rows match (filters, sort) goes back to the first row; a page size
 * change keeps the first row on screen; a table change also clears the
 * columns, the row and the table's own state. A legacy `page` is written as
 * the offset it means.
 */
export function patchSearch(search: DataSearch, patch: Partial<DataSearch>): DataSearch {
  const current: DataSearch = { ...search };
  if (current.page !== undefined) {
    if (current.offset === undefined) current.offset = offsetOf(current);
    delete current.page;
  }
  const next: DataSearch = { ...current, ...patch };
  delete next.page;
  if ("table" in patch && patch.table !== current.table) {
    next.f = patch.f;
    next.sort = patch.sort;
    next.cols = patch.cols;
    next.offset = patch.offset;
    next.row = patch.row;
  } else if ("f" in patch || "sort" in patch) {
    next.offset = patch.offset;
  }
  if ("db" in patch && patch.db !== current.db) {
    next.table = patch.table;
    next.f = patch.f;
    next.sort = patch.sort;
    next.cols = patch.cols;
    next.offset = patch.offset;
    next.row = patch.row;
  }
  for (const key of DATA_SEARCH_KEYS) {
    const value = next[key];
    if (value === undefined || (Array.isArray(value) && value.length === 0) || (key === "offset" && value === 0) || (key === "size" && value === DEFAULT_PAGE_SIZE)) {
      delete next[key];
    }
  }
  return next;
}
