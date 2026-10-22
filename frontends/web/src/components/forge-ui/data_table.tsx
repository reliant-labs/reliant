import React, { useState } from "react";

import Link from "./link";

export interface DataTableColumn<T> {
  key: string;
  /** Header text, sentence case. Use `srHeader` for a column whose header is visually empty (row actions). */
  header: string;
  render?: (value: unknown, row: T) => React.ReactNode;
  sortable?: boolean;
  width?: string;
  /** A figure: right-aligned, tabular numerals, header aligned to match. */
  numeric?: boolean;
  /**
   * The cell holds its own controls (a menu, a button). In a table with
   * `getRowHref` the whole row is a link; an interactive cell is lifted above
   * it so its controls keep their own clicks.
   */
  interactive?: boolean;
  /** Extra classes for this column's cells (alignment, truncation). */
  className?: string;
  /** Extra classes for this column's header cell. */
  headerClassName?: string;
  /** Render the header for screen readers only. */
  srHeader?: boolean;
}

/** Attributes a caller may set on a body row: a test id, a highlight. */
export type DataTableRowProps = {
  className?: string;
} & { [dataAttribute: `data-${string}`]: string | undefined };

export interface DataTableProps<T extends Record<string, unknown>> {
  columns: DataTableColumn<T>[];
  data: T[];
  onSort?: (key: string, direction: "asc" | "desc") => void;
  onPageChange?: (page: number) => void;
  onPageSizeChange?: (size: number) => void;
  onSelectionChange?: (selectedRows: T[]) => void;
  page?: number;
  pageSize?: number;
  totalItems?: number;
  loading?: boolean;
  emptyMessage?: React.ReactNode;
  selectable?: boolean;
  /**
   * Whether to render the pagination footer.
   *
   * Defaults to whether the caller wired EITHER paging handler, because a
   * footer without one is not merely decorative — it actively misinforms. A
   * table showing a complete finite set that displays "Rows per page: 10" with
   * dead Previous/Next buttons tells the reader the list is truncated.
   *
   * Pass it explicitly to override in either direction.
   */
  paginated?: boolean;
  /**
   * Make each row a link to this href. The WHOLE row is the target — the
   * first column's content is the anchor (so its text is the link's name) and
   * a pseudo-element stretches it across the row. Return undefined for a row
   * that goes nowhere. Mark columns holding their own controls `interactive`.
   */
  getRowHref?: (row: T, index: number) => string | undefined;
  /**
   * A stable key per row. Defaults to the row index, which is wrong for any
   * list that re-sorts or filters: React then reuses one row's DOM (and its
   * open menus, focus) for a different record.
   */
  getRowKey?: (row: T, index: number) => React.Key;
  /** Per-row attributes: a test id, a highlight class. */
  getRowProps?: (row: T, index: number) => DataTableRowProps;
  /**
   * "fixed" lays the table out from the column widths alone, so every row's
   * cells line up and a long value truncates instead of widening its column.
   * Give each column a `width` (one may be left to take the remainder).
   */
  layout?: "auto" | "fixed";
  /** 32px rows instead of 40px, for a long list in a narrow panel. */
  compact?: boolean;
  /**
   * Drop the table's own border and radius, to sit flush inside a <Card> that
   * already frames it (title above, table to the edges). Never nest a framed
   * table in a card: that is a card in a card.
   */
  flush?: boolean;
  /** The table's accessible name. */
  ariaLabel?: string;
}

function SkeletonRow({ cols, height }: { cols: number; height: string }) {
  return (
    <tr>
      <td colSpan={cols} className="p-0">
        <span className="sr-only">Loading…</span>
        <div aria-hidden="true" className={`flex items-center gap-4 px-4 ${height}`}>
          {Array.from({ length: cols }).map((_, i) => (
            <div
              key={i}
              className="h-3 flex-1 animate-pulse motion-reduce:animate-none rounded bg-ink/5"
            />
          ))}
        </div>
      </td>
    </tr>
  );
}

/**
 * DataTable — the list every console page is made of.
 *
 * Density is the point: 36px header, 40px rows, `px-4` cells, hairline row
 * dividers, no zebra, no header fill. Headers are sentence case in
 * `text-xs text-ink-muted` — never `uppercase tracking-wider`. Figures are
 * right-aligned in tabular numerals (`numeric`). A column that holds the same
 * value on every row belongs in the page's meta line, not in the table.
 *
 *   <DataTable
 *     columns={[{ key: "name", header: "Name" }, { key: "p95", header: "p95", numeric: true }]}
 *     data={services}
 *     getRowKey={(s) => s.name}
 *     getRowHref={(s) => `/services/${s.name}`}
 *   />
 */
export default function DataTable<T extends Record<string, unknown>>({
  columns,
  data,
  onSort,
  onPageChange,
  onPageSizeChange,
  onSelectionChange,
  page = 1,
  pageSize = 10,
  totalItems,
  loading = false,
  emptyMessage = "Nothing here yet",
  selectable = false,
  paginated,
  getRowHref,
  getRowKey,
  getRowProps,
  layout = "auto",
  compact = false,
  flush = false,
  ariaLabel,
}: DataTableProps<T>) {
  const rowHeight = compact ? "h-8" : "h-10";
  const [sortKey, setSortKey] = useState<string | null>(null);
  const [sortDir, setSortDir] = useState<"asc" | "desc">("asc");
  const [selected, setSelected] = useState<Set<number>>(new Set());

  const total = totalItems ?? data.length;
  const totalPages = Math.max(1, Math.ceil(total / pageSize));

  function handleSort(key: string) {
    const dir = sortKey === key && sortDir === "asc" ? "desc" : "asc";
    setSortKey(key);
    setSortDir(dir);
    onSort?.(key, dir);
  }

  function toggleAll() {
    if (selected.size === data.length) {
      setSelected(new Set());
      onSelectionChange?.([]);
    } else {
      const all = new Set(data.map((_, i) => i));
      setSelected(all);
      onSelectionChange?.([...data]);
    }
  }

  function toggleRow(index: number) {
    const next = new Set(selected);
    if (next.has(index)) {
      next.delete(index);
    } else {
      next.add(index);
    }
    setSelected(next);
    onSelectionChange?.(data.filter((_, i) => next.has(i)));
  }

  const allCols = selectable ? columns.length + 1 : columns.length;

  // A footer the caller cannot act on is a claim about paging that is not
  // true. Default to showing it only when paging is actually wired up.
  const showPagination = paginated ?? (!!onPageChange || !!onPageSizeChange);

  const pagerButton =
    "inline-flex h-8 items-center rounded-md border border-border px-3 text-sm font-medium text-ink transition-colors hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent disabled:cursor-not-allowed disabled:opacity-50";

  return (
    <div className={flush ? "" : "overflow-hidden rounded-lg border border-border bg-surface"}>
      <div className="overflow-x-auto">
        <table
          className={`min-w-full border-collapse ${layout === "fixed" ? "w-full table-fixed" : ""}`}
          aria-label={ariaLabel}
        >
          <thead>
            <tr className="border-b border-border">
              {selectable && (
                <th className="h-9 w-10 px-4">
                  <input
                    type="checkbox"
                    aria-label="Select all rows"
                    checked={selected.size === data.length && data.length > 0}
                    onChange={toggleAll}
                    className="h-4 w-4 rounded border-border-strong text-accent focus:ring-accent"
                  />
                </th>
              )}
              {columns.map((col) => (
                <th
                  key={col.key}
                  scope="col"
                  aria-sort={
                    col.sortable && sortKey === col.key
                      ? sortDir === "asc"
                        ? "ascending"
                        : "descending"
                      : undefined
                  }
                  className={`h-9 whitespace-nowrap px-4 text-xs font-medium text-ink-muted ${
                    col.numeric ? "text-right" : "text-left"
                  } ${col.headerClassName ?? ""}`}
                  style={col.width ? { width: col.width } : undefined}
                >
                  {col.srHeader ? (
                    <span className="sr-only">{col.header}</span>
                  ) : col.sortable ? (
                    <button
                      type="button"
                      onClick={() => handleSort(col.key)}
                      className="inline-flex items-center gap-1 rounded hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
                    >
                      {col.header}
                      <span aria-hidden="true" className="w-3 text-ink">
                        {sortKey === col.key ? (sortDir === "asc" ? "↑" : "↓") : ""}
                      </span>
                    </button>
                  ) : (
                    col.header
                  )}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {loading ? (
              Array.from({ length: pageSize }).map((_, i) => (
                <SkeletonRow key={i} cols={allCols} height={rowHeight} />
              ))
            ) : data.length === 0 ? (
              <tr>
                <td colSpan={allCols} className="px-4 py-12 text-center text-sm text-ink-muted">
                  {emptyMessage}
                </td>
              </tr>
            ) : (
              data.map((row, i) => {
                const { className: rowClassName, ...rowAttributes } = getRowProps?.(row, i) ?? {};
                const href = getRowHref?.(row, i);
                return (
                  <tr
                    key={getRowKey ? getRowKey(row, i) : i}
                    {...rowAttributes}
                    className={`border-t border-border/70 first:border-t-0 ${
                      href ? "relative cursor-pointer hover:bg-ink/5" : ""
                    } ${selected.has(i) ? "bg-accent-surface" : ""} ${rowClassName ?? ""}`}
                  >
                    {selectable && (
                      <td className={`${rowHeight} relative z-10 px-4`}>
                        <input
                          type="checkbox"
                          aria-label={`Select row ${i + 1}`}
                          checked={selected.has(i)}
                          onChange={() => toggleRow(i)}
                          className="h-4 w-4 rounded border-border-strong text-accent focus:ring-accent"
                        />
                      </td>
                    )}
                    {columns.map((col, ci) => {
                      const content = col.render
                        ? col.render(row[col.key], row)
                        : String(row[col.key] ?? "");
                      return (
                        <td
                          key={col.key}
                          className={`${rowHeight} whitespace-nowrap px-4 text-sm text-ink ${
                            col.numeric ? "text-right tabular-nums" : ""
                          } ${col.interactive ? "relative z-10" : ""} ${col.className ?? ""}`}
                        >
                          {href && ci === 0 ? (
                            <Link
                              href={href}
                              className="after:absolute after:inset-0 after:content-[''] focus-visible:outline-none focus-visible:after:ring-2 focus-visible:after:ring-inset focus-visible:after:ring-accent"
                            >
                              {content}
                            </Link>
                          ) : (
                            content
                          )}
                        </td>
                      );
                    })}
                  </tr>
                );
              })
            )}
          </tbody>
        </table>
      </div>

      {/* Pagination — only when the caller can actually page. */}
      {showPagination && (
        <div className="flex items-center justify-between border-t border-border px-4 py-2">
          <label className="flex items-center gap-2 text-xs text-ink-muted">
            Rows per page
            <select
              value={pageSize}
              onChange={(e) => onPageSizeChange?.(Number(e.target.value))}
              className="h-7 rounded-md border border-border bg-surface px-2 text-xs text-ink focus:border-accent-border focus:outline-none focus:ring-1 focus:ring-accent"
            >
              {[10, 25, 50, 100].map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </select>
          </label>
          <div className="flex items-center gap-3">
            <span className="text-xs tabular-nums text-ink-muted">
              Page {page} of {totalPages}
            </span>
            <div className="flex gap-2">
              <button
                type="button"
                onClick={() => onPageChange?.(page - 1)}
                disabled={page <= 1}
                className={pagerButton}
              >
                Previous
              </button>
              <button
                type="button"
                onClick={() => onPageChange?.(page + 1)}
                disabled={page >= totalPages}
                className={pagerButton}
              >
                Next
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
