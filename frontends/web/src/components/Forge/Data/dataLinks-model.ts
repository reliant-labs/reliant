// Copyright (c) 2025 Reliant Labs

/**
 * HOW ROWS ARE ADDRESSED AND LINKED: the row page's address, the foreign-key
 * links out of a row, the Related links into it, and how a count reads.
 *
 *   A ROW is addressed by its primary key, values in key order, as the `row`
 *   param's comma list (dataUrl-model). A table with no primary key (or a
 *   view) has rows with no stable address, so they get no row link.
 *
 *   AN OUTGOING FOREIGN KEY links to the referenced row when the key points
 *   at the referenced table's primary key (the usual case), and to the
 *   referenced table's grid filtered to the matching rows otherwise (a key on
 *   a UNIQUE column). A referenced table the browser cannot read gets no link:
 *   DescribeDatabase leaves it out of `tables`.
 *
 *   A RELATED entry is one foreign key of another table pointing at this one:
 *   the link is that table's grid with an eq filter per key column, set to
 *   this row's referenced values.
 */

import type { BrowsePage, DbCell, DbForeignKey, DbTable } from "@/services/controlPlane/observability/appDatabaseApi";

import { decodeList, encodeFilter, encodeList, type DataFilter, type DataSearch } from "./dataUrl-model";

export function qualifiedName(table: Pick<DbTable, "schema" | "name">): string {
  return `${table.schema}.${table.name}`;
}

export function findTable(tables: readonly DbTable[], qualified: string | undefined): DbTable | null {
  if (!qualified) return null;
  return tables.find((table) => qualifiedName(table) === qualified) ?? null;
}

/**
 * A cell as the text a filter or a key carries: what Postgres converts back to
 * the column's type. Null for NULL, which no eq filter matches.
 */
export function cellText(value: DbCell | undefined): string | null {
  if (value === null || value === undefined) return null;
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  return JSON.stringify(value);
}

/** A row's values by column name. */
export function rowRecord(columns: readonly { name: string }[], row: readonly DbCell[]): Map<string, DbCell> {
  return new Map(columns.map((column, i) => [column.name, row[i] ?? null]));
}

// ── Row addressing ──────────────────────────────────────────────────────────

/** The `row` param for a row of `table`, or null when it has no address (no key, a NULL or missing key column). */
export function rowParamOf(table: Pick<DbTable, "primaryKey">, record: ReadonlyMap<string, DbCell>): string | null {
  if (table.primaryKey.length === 0) return null;
  const values: string[] = [];
  for (const column of table.primaryKey) {
    const text = cellText(record.get(column));
    if (text === null) return null;
    values.push(text);
  }
  return encodeList(values);
}

/** The key values a `row` param names, in key order, or null when it does not fit the key. */
export function rowValuesOf(table: Pick<DbTable, "primaryKey">, param: string): string[] | null {
  if (table.primaryKey.length === 0) return null;
  const values = decodeList(param);
  return values.length === table.primaryKey.length ? values : null;
}

/** The row page's BrowseTable filters: one eq per key column. */
export function rowFilters(table: Pick<DbTable, "primaryKey">, values: readonly string[]): DataFilter[] {
  return table.primaryKey.map((column, i) => ({ column, op: "eq" as const, values: [values[i]!] }));
}

/** How the row page titles a row: `id 42`, or `org_id org-1 · id 42`. */
export function rowLabel(table: Pick<DbTable, "primaryKey">, values: readonly string[]): string {
  return table.primaryKey.map((column, i) => `${column} ${values[i] ?? ""}`).join(" · ");
}

// ── Outgoing foreign keys ───────────────────────────────────────────────────

/** The foreign keys of `table` that `column` is part of. */
export function foreignKeysOf(table: Pick<DbTable, "foreignKeys">, column: string): DbForeignKey[] {
  return table.foreignKeys.filter((fk) => fk.columns.includes(column));
}

function sameSet(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every((name) => b.includes(name));
}

/**
 * Where a foreign key's value in `record` leads: the referenced row, or the
 * referenced table filtered to it. Null when the value is NULL (no reference),
 * or the referenced table is not readable.
 */
export function foreignKeyTarget(fk: DbForeignKey, tables: readonly DbTable[], record: ReadonlyMap<string, DbCell>): DataSearch | null {
  const target = findTable(tables, `${fk.refSchema}.${fk.refTable}`);
  if (!target) return null;
  const byRefColumn = new Map<string, string>();
  for (let i = 0; i < fk.columns.length; i++) {
    const text = cellText(record.get(fk.columns[i]!));
    if (text === null) return null;
    byRefColumn.set(fk.refColumns[i]!, text);
  }
  const table = qualifiedName(target);
  if (target.primaryKey.length > 0 && sameSet(fk.refColumns, target.primaryKey)) {
    return { table, row: encodeList(target.primaryKey.map((column) => byRefColumn.get(column)!)) };
  }
  return { table, f: fk.refColumns.map((column) => encodeFilter({ column, op: "eq", values: [byRefColumn.get(column)!] })) };
}

// ── Related (incoming foreign keys) ─────────────────────────────────────────

/** The filters that select the rows of `fk`'s table pointing at the row in `record`, or null when a referenced value is NULL or not read. */
export function relatedFilters(fk: DbForeignKey, record: ReadonlyMap<string, DbCell>): DataFilter[] | null {
  const filters: DataFilter[] = [];
  for (let i = 0; i < fk.columns.length; i++) {
    const text = cellText(record.get(fk.refColumns[i]!));
    if (text === null) return null;
    filters.push({ column: fk.columns[i]!, op: "eq", values: [text] });
  }
  return filters;
}

/** The Related entry's link: the referencing table's grid, filtered to this row. */
export function relatedTarget(fk: DbForeignKey, record: ReadonlyMap<string, DbCell>): DataSearch | null {
  const filters = relatedFilters(fk, record);
  if (!filters) return null;
  return { table: `${fk.schema}.${fk.table}`, f: filters.map(encodeFilter) };
}

/** A stable key for a foreign key: the constraint name is unique per table, not per database. */
export function foreignKeyId(fk: DbForeignKey): string {
  return `${fk.schema}.${fk.table}.${fk.constraint}`;
}

// ── Counts ──────────────────────────────────────────────────────────────────

/** 950, 1.2k, 12k, 123k, 1.2M, 3.4B. */
export function compactCount(n: number): string {
  const abs = Math.abs(n);
  const units: [number, string][] = [
    [1e9, "B"],
    [1e6, "M"],
    [1e3, "k"],
  ];
  for (const [size, suffix] of units) {
    if (abs >= size) {
      const scaled = n / size;
      const text = Math.abs(scaled) >= 10 ? Math.round(scaled).toString() : (Math.round(scaled * 10) / 10).toString();
      return `${text}${suffix}`;
    }
  }
  return Math.round(n).toString();
}

/**
 * A row count, as it was measured: an estimate is `~1.2k` (the planner's
 * guess, never more precise than that), a capped exact count `10,000+` (at
 * least that many), an exact one in full.
 */
export function formatCount(n: number, how: { estimate?: boolean; lowerBound?: boolean } = {}): string {
  if (how.lowerBound) return `${n.toLocaleString("en-US")}+`;
  if (how.estimate) return `~${compactCount(Math.max(0, n))}`;
  return n.toLocaleString("en-US");
}

/** A table's estimated rows in the rail, or null when unknown (-1: a view, never analyzed). */
export function estimatedRowsLabel(estimatedRows: number): string | null {
  return estimatedRows < 0 ? null : formatCount(estimatedRows, { estimate: true });
}

/**
 * A grid's total: from the response's count, or — when the server counted
 * nothing — what the page itself proves (a last page is an exact total).
 */
export function totalLabel(page: Pick<BrowsePage, "total" | "totalIsEstimate" | "totalIsLowerBound" | "hasMore" | "rows">, offset: number): string | null {
  // An empty page past the end proves nothing about where the end is.
  if (!page.hasMore && (page.rows.length > 0 || offset === 0)) return formatCount(offset + page.rows.length);
  if (page.total === null) return null;
  if (!page.hasMore) return formatCount(page.total, { estimate: page.totalIsEstimate, lowerBound: page.totalIsLowerBound });
  // An estimate below what is already on screen is wrong in the one direction the reader can see.
  const seen = offset + page.rows.length + 1;
  if (page.totalIsEstimate) return formatCount(Math.max(page.total, seen), { estimate: true });
  return formatCount(page.total, { lowerBound: page.totalIsLowerBound });
}

/**
 * A Related count from a `limit 1, count EXACT` read: the exact number, or
 * the server's cap with a plus ("10,000+") when it stopped counting. A
 * response with no total (it counted nothing) says only what the one row
 * read proves: none, 1, or "1+".
 */
export function relatedCountLabel(page: Pick<BrowsePage, "total" | "totalIsLowerBound" | "hasMore" | "rows">): { label: string; n: number; none: boolean } {
  if (page.total !== null) {
    return { label: formatCount(page.total, { lowerBound: page.totalIsLowerBound }), n: page.total, none: page.total === 0 && !page.totalIsLowerBound };
  }
  if (page.rows.length === 0) return { label: "0", n: 0, none: true };
  return { label: page.hasMore ? "1+" : "1", n: page.hasMore ? 2 : 1, none: false };
}

// ── Concurrency ─────────────────────────────────────────────────────────────

/** The row page fires its Related counts at most this many at a time. */
export const RELATED_CONCURRENCY = 4;

/**
 * A run gate: at most `max` of the functions given to it run at once, the
 * rest start in order as earlier ones settle. A Related section with thirty
 * referencing tables is thirty audited reads; firing them all at once would
 * hit the server's per-principal in-flight cap and fail most of them.
 */
export function createLimiter(max: number): <T>(task: () => Promise<T>) => Promise<T> {
  let running = 0;
  const waiting: (() => void)[] = [];
  const release = () => {
    running--;
    waiting.shift()?.();
  };
  return <T>(task: () => Promise<T>) =>
    new Promise<T>((resolve, reject) => {
      const start = () => {
        running++;
        task().then(resolve, reject).finally(release);
      };
      if (running < max) start();
      else waiting.push(start);
    });
}
