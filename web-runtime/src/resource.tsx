"use client";

// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// <Resource> — the owned-once data-table container. It encapsulates the
// loading / error / empty / data tristate ladder, a server-side (debounced)
// filter, and cursor pagination, so list pages stop re-hand-rolling the
// ladder and stop client-side-filtering a single page cap.
//
// Pairs with the app's useQueryResource hook: map a query result to
// { status, data, error } and hand it here. Pagination is
// cursor-based (page_token / next_page_token in Connect list RPCs): the page
// owns the cursor stack and passes onNextPage/onPrevPage.
import { useEffect, useState, type ReactElement, type ReactNode } from "react";

import { userMessage } from "./errors.js";

export interface ResourceColumn<T> {
  header: string;
  cell: (row: T) => ReactNode;
  /** Extra classes for the cell + header (alignment, width). */
  className?: string;
}

export type ResourceStatus = "loading" | "error" | "success";

export interface ResourceProps<T> {
  status: ResourceStatus;
  data: T[] | undefined;
  columns: ResourceColumn<T>[];
  rowKey: (row: T) => string;

  title?: string;
  /** Header-right slot, e.g. a "New" button. */
  actions?: ReactNode;
  error?: Error | null;
  onRetry?: () => void;
  onRowClick?: (row: T) => void;

  emptyTitle?: string;
  emptyMessage?: string;

  // Server-side filter (debounced). Omit onFilterChange to hide the search box.
  filter?: string;
  onFilterChange?: (value: string) => void;
  filterPlaceholder?: string;
  filterDebounceMs?: number;

  // Cursor pagination. Omit the handlers to hide the footer.
  onNextPage?: () => void;
  onPrevPage?: () => void;
  hasNextPage?: boolean;
  hasPrevPage?: boolean;
  /** True during a background refetch (page change / filter change). */
  isFetching?: boolean;
}

export function Resource<T>({
  status,
  data,
  columns,
  rowKey,
  title,
  actions,
  error,
  onRetry,
  onRowClick,
  emptyTitle = "Nothing here yet",
  emptyMessage = "No records match.",
  filter,
  onFilterChange,
  filterPlaceholder = "Search…",
  filterDebounceMs = 300,
  onNextPage,
  onPrevPage,
  hasNextPage,
  hasPrevPage,
  isFetching,
}: ResourceProps<T>): ReactElement {
  const [query, setQuery] = useState(filter ?? "");

  // Keep local input in sync when the parent resets the filter externally.
  useEffect(() => {
    setQuery(filter ?? "");
  }, [filter]);

  // Debounce filter changes so each keystroke doesn't fire an RPC.
  useEffect(() => {
    if (!onFilterChange || query === (filter ?? "")) {
      return;
    }
    const handle = setTimeout(() => onFilterChange(query), filterDebounceMs);
    return () => clearTimeout(handle);
  }, [query, filter, onFilterChange, filterDebounceMs]);

  const showFooter = Boolean(onNextPage || onPrevPage);
  const colCount = columns.length;
  const captionText = title ?? "Results";

  return (
    <section className="flex flex-col gap-4">
      {(title || actions) && (
        <header className="flex items-center justify-between gap-4">
          {title ? (
            <h1 className="text-xl font-semibold text-ink">{title}</h1>
          ) : (
            <span />
          )}
          {actions ? (
            <div className="flex items-center gap-2">{actions}</div>
          ) : null}
        </header>
      )}

      {onFilterChange ? (
        <input
          type="search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder={filterPlaceholder}
          aria-label={title ? `Search ${title.toLowerCase()}` : "Search"}
          className="w-full max-w-sm rounded-md border border-border bg-surface px-3 py-2 text-sm text-ink shadow-sm focus:border-accent focus:outline-none focus:ring-1 focus:ring-accent"
        />
      ) : null}

      <div className="overflow-x-auto rounded-lg border border-border bg-surface">
        <table className="min-w-full divide-y divide-border text-sm">
          <caption className="sr-only">{captionText}</caption>
          <thead className="bg-surface-muted">
            <tr>
              {columns.map((col, i) => (
                <th
                  key={i}
                  scope="col"
                  className={`px-4 py-2.5 text-left font-medium text-ink-muted ${
                    col.className ?? ""
                  }`}
                >
                  {col.header}
                </th>
              ))}
            </tr>
          </thead>
          <tbody className="divide-y divide-border">
            {status === "loading" ? (
              <SkeletonRows rows={5} cols={colCount} />
            ) : status === "error" ? (
              <tr>
                <td colSpan={colCount} className="px-4 py-10">
                  <ErrorState error={error} onRetry={onRetry} />
                </td>
              </tr>
            ) : !data || data.length === 0 ? (
              <tr>
                <td colSpan={colCount} className="px-4 py-12">
                  <EmptyState title={emptyTitle} message={emptyMessage} />
                </td>
              </tr>
            ) : (
              data.map((row) => (
                <tr
                  key={rowKey(row)}
                  onClick={onRowClick ? () => onRowClick(row) : undefined}
                  // Keyboard-operable when the row navigates: Enter/Space
                  // activate it, and it takes focus in tab order.
                  role={onRowClick ? "button" : undefined}
                  tabIndex={onRowClick ? 0 : undefined}
                  onKeyDown={
                    onRowClick
                      ? (e) => {
                          if (e.key === "Enter" || e.key === " ") {
                            e.preventDefault();
                            onRowClick(row);
                          }
                        }
                      : undefined
                  }
                  className={
                    onRowClick
                      ? "cursor-pointer transition-colors hover:bg-surface-muted focus:bg-surface-muted focus:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent"
                      : undefined
                  }
                >
                  {columns.map((col, i) => (
                    <td
                      key={i}
                      className={`px-4 py-2.5 text-ink ${col.className ?? ""}`}
                    >
                      {col.cell(row)}
                    </td>
                  ))}
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>

      {showFooter ? (
        <footer className="flex items-center justify-end gap-2">
          {isFetching ? (
            <span className="mr-auto text-xs text-ink-subtle">Loading…</span>
          ) : null}
          <button
            type="button"
            onClick={onPrevPage}
            disabled={!hasPrevPage}
            className="rounded-md border border-border px-3 py-1.5 text-sm font-medium text-ink-muted hover:bg-surface-muted disabled:cursor-not-allowed disabled:opacity-40"
          >
            Previous
          </button>
          <button
            type="button"
            onClick={onNextPage}
            disabled={!hasNextPage}
            className="rounded-md border border-border px-3 py-1.5 text-sm font-medium text-ink-muted hover:bg-surface-muted disabled:cursor-not-allowed disabled:opacity-40"
          >
            Next
          </button>
        </footer>
      ) : null}
    </section>
  );
}

// SkeletonRows is the loading rung of the tristate ladder: a shimmer the
// width of the table while the first page is in flight.
//
// It is ONE row with a colSpan cell, matching the error and empty rungs
// above — not `rows` × `cols` real table cells. That shape is what makes it
// accessible, and the reasoning is worth keeping:
//
//   - The shimmer is pure decoration. It carries no data, so it is wrapped
//     in aria-hidden and the cell carries one sr-only "Loading…" instead.
//     Assistive tech hears the state once, not once per placeholder.
//   - A grid of empty <td>s announced as a table of blank cells, and read
//     as a real 5-row result set by anything counting rows. The placeholder
//     geometry is now plain <div>s inside the single cell, so the table has
//     exactly the structure it claims to have at every moment.
//   - Empty cells also tripped jsx-a11y/control-has-associated-label under
//     the eslint config forge scaffolds. That was the symptom that surfaced
//     this; the row-shape mismatch above is the actual defect.
//
// aria-hidden goes on the inner <div>, never on the <td> or <tr>: jsx-a11y
// treats table elements as focusable, so hiding one is
// no-aria-hidden-on-focusable — correctly, since a hidden focusable node is
// a focus trap for screen-reader users.
function SkeletonRows({ rows, cols }: { rows: number; cols: number }) {
  return (
    <tr>
      <td colSpan={cols} className="p-0">
        <span className="sr-only">Loading…</span>
        <div aria-hidden="true" className="divide-y divide-border">
          {Array.from({ length: rows }).map((_, r) => (
            <div key={r} className="flex items-center gap-4 px-4 py-3">
              {Array.from({ length: cols }).map((__, c) => (
                <div
                  key={c}
                  className="h-4 flex-1 animate-pulse motion-reduce:animate-none rounded bg-surface-muted"
                />
              ))}
            </div>
          ))}
        </div>
      </td>
    </tr>
  );
}

// ErrorState shows userMessage(error), never error.message: a ConnectError's
// message carries the "[code] " transport framing, which is not copy a user
// should read. It is body text in the empty rung's style, not a monospace
// debug readout.
function ErrorState({
  error,
  onRetry,
}: {
  error?: Error | null;
  onRetry?: () => void;
}) {
  return (
    <div role="alert" className="flex flex-col items-center gap-2 text-center">
      <p className="text-sm font-medium text-danger-ink">
        Couldn&apos;t load data
      </p>
      {error ? (
        <p className="max-w-md text-sm text-ink-muted">{userMessage(error)}</p>
      ) : null}
      {onRetry ? (
        <button
          type="button"
          onClick={onRetry}
          className="mt-1 rounded-md bg-danger px-3 py-1.5 text-sm font-medium text-on-danger hover:bg-danger-hover"
        >
          Try again
        </button>
      ) : null}
    </div>
  );
}

function EmptyState({ title, message }: { title: string; message: string }) {
  return (
    <div className="flex flex-col items-center gap-1 text-center">
      <p className="text-sm font-medium text-ink">{title}</p>
      <p className="text-sm text-ink-muted">{message}</p>
    </div>
  );
}
