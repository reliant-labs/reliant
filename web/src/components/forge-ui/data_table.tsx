import React, { useState } from "react";

interface Column<T> {
  key: string;
  header: string;
  render?: (value: unknown, row: T) => React.ReactNode;
  sortable?: boolean;
  width?: string;
}

interface DataTableProps<T extends Record<string, unknown>> {
  columns: Column<T>[];
  data: T[];
  onSort?: (key: string, direction: "asc" | "desc") => void;
  onPageChange?: (page: number) => void;
  onPageSizeChange?: (size: number) => void;
  onSelectionChange?: (selectedRows: T[]) => void;
  page?: number;
  pageSize?: number;
  totalItems?: number;
  loading?: boolean;
  emptyMessage?: string;
  selectable?: boolean;
  /**
   * Whether to render the pagination footer.
   *
   * Defaults to whether the caller wired EITHER paging handler, because a
   * footer without one is not merely decorative — it actively misinforms. The
   * footer previously rendered unconditionally, so a table showing a complete
   * finite set (every workload in a cluster; every member of a team) displayed
   * "Rows per page: 10" above sixteen rows, with dead Previous/Next buttons. A
   * reader who believes that chrome concludes the list is truncated and goes
   * looking for page two.
   *
   * Pass it explicitly to override in either direction: `false` to suppress the
   * footer on a paged table, `true` to keep it on an uncontrolled one.
   */
  paginated?: boolean;
}

function SkeletonRow({ cols }: { cols: number }) {
  return (
    <tr>
      <td colSpan={cols} className="p-0">
        <span className="sr-only">Loading…</span>
        <div aria-hidden="true" className="flex items-center gap-4 px-4 py-3">
          {Array.from({ length: cols }).map((_, i) => (
            <div
              key={i}
              className="h-4 flex-1 animate-pulse rounded bg-surface-muted"
            />
          ))}
        </div>
      </td>
    </tr>
  );
}

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
  emptyMessage = "No data available",
  selectable = false,
  paginated,
}: DataTableProps<T>) {
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

  return (
    <div className="overflow-hidden rounded-lg border border-border bg-surface">
      <div className="overflow-x-auto">
        <table className="min-w-full divide-y divide-border">
          <thead className="bg-surface-muted">
            <tr>
              {selectable && (
                <th className="w-10 px-4 py-3">
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
                  className={`px-4 py-3 text-left text-xs font-semibold uppercase tracking-wider text-ink-muted ${
                    col.sortable
                      ? "cursor-pointer select-none hover:text-ink-muted"
                      : ""
                  }`}
                  style={col.width ? { width: col.width } : undefined}
                  onClick={() => col.sortable && handleSort(col.key)}
                >
                  <span className="inline-flex items-center gap-1">
                    {col.header}
                    {col.sortable && sortKey === col.key && (
                      <span className="text-accent">
                        {sortDir === "asc" ? "↑" : "↓"}
                      </span>
                    )}
                  </span>
                </th>
              ))}
            </tr>
          </thead>
          <tbody className="divide-y divide-border">
            {loading ? (
              Array.from({ length: pageSize }).map((_, i) => (
                <SkeletonRow key={i} cols={allCols} />
              ))
            ) : data.length === 0 ? (
              <tr>
                <td colSpan={allCols} className="px-4 py-12 text-center">
                  <p className="text-sm text-ink-muted">{emptyMessage}</p>
                </td>
              </tr>
            ) : (
              data.map((row, i) => (
                <tr
                  key={i}
                  className={`transition-colors hover:bg-surface-muted ${
                    selected.has(i) ? "bg-accent-surface" : ""
                  }`}
                >
                  {selectable && (
                    <td className="px-4 py-3">
                      <input
                        type="checkbox"
                        aria-label={`Select row ${i + 1}`}
                        checked={selected.has(i)}
                        onChange={() => toggleRow(i)}
                        className="h-4 w-4 rounded border-border-strong text-accent focus:ring-accent"
                      />
                    </td>
                  )}
                  {columns.map((col) => (
                    <td
                      key={col.key}
                      className="whitespace-nowrap px-4 py-3 text-sm text-ink-muted"
                    >
                      {col.render
                        ? col.render(row[col.key], row)
                        : String(row[col.key] ?? "")}
                    </td>
                  ))}
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>

      {/* Pagination — only when the caller can actually page. */}
      {showPagination && (
      <div className="flex items-center justify-between border-t border-border bg-surface px-4 py-3">
        <div className="flex items-center gap-2 text-sm text-ink-muted">
          <span>Rows per page:</span>
          <select
            value={pageSize}
            onChange={(e) => onPageSizeChange?.(Number(e.target.value))}
            className="rounded border border-border-strong bg-surface px-2 py-1 text-sm focus:border-accent-border focus:outline-none focus:ring-1 focus:ring-accent"
          >
            {[10, 25, 50, 100].map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
        </div>
        <div className="flex items-center gap-2">
          <span className="text-sm text-ink-muted">
            Page {page} of {totalPages}
          </span>
          <div className="flex gap-1">
            <button
              onClick={() => onPageChange?.(page - 1)}
              disabled={page <= 1}
              className="rounded-lg border border-border-strong px-3 py-1.5 text-sm font-medium text-ink-muted transition hover:bg-surface-muted disabled:cursor-not-allowed disabled:opacity-50"
            >
              Previous
            </button>
            <button
              onClick={() => onPageChange?.(page + 1)}
              disabled={page >= totalPages}
              className="rounded-lg border border-border-strong px-3 py-1.5 text-sm font-medium text-ink-muted transition hover:bg-surface-muted disabled:cursor-not-allowed disabled:opacity-50"
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
