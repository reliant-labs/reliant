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
//
// Layout, top to bottom: a header (title + primary action), a toolbar row
// (search + the page's `toolbar` facets) that WRAPS, the table, and a footer
// (the record count + the pager). Filters used to share the header row with
// the title and the create button in one non-wrapping flex row, which pushed
// the button off-canvas on a phone; they now have a row of their own.
import {
  useEffect,
  useState,
  type KeyboardEvent,
  type MouseEvent,
  type ReactElement,
  type ReactNode,
} from "react";

import { userMessage } from "./errors.js";

export interface ResourceColumn<T> {
  header: string;
  cell: (row: T) => ReactNode;
  /** Extra classes for the cell + header (alignment, width). */
  className?: string;
  /**
   * Makes the column sortable: the server-side sort key (the List request's
   * `order_by` value, a column name) this header sorts by. The header renders
   * a button and `aria-sort`; the page owns the state through
   * `sort` / `onSortChange`.
   */
  sortKey?: string;
}

/** The active server-side sort: an `order_by` key and its direction. */
export interface ResourceSort {
  key: string;
  descending: boolean;
}

export type ResourceStatus = "loading" | "error" | "success";

export interface ResourceProps<T> {
  status: ResourceStatus;
  data: T[] | undefined;
  columns: ResourceColumn<T>[];
  rowKey: (row: T) => string;

  title?: string;
  /** Header-right slot: the page's primary action, e.g. a "New" button. */
  actions?: ReactNode;
  /**
   * Filter controls, rendered in their own wrapping row beside the search
   * box — never in the header, so a page with several facets still fits a
   * phone. Pass a filter bar, selects, pickers.
   */
  toolbar?: ReactNode;
  error?: Error | null;
  onRetry?: () => void;
  /**
   * Clicking anywhere on a row calls it; a click that lands on a link or
   * another control inside the row is left to that control. Without
   * `rowsHaveLinks` the row is also the keyboard path to the record: it takes
   * a tab stop, `role="button"`, and Enter/Space call this.
   */
  onRowClick?: (row: T) => void;
  /**
   * Declares that every row renders a real link to its record (in the
   * primary cell), so `onRowClick` is a POINTER convenience only: the row
   * keeps table semantics (no button role, no tab stop) and keyboard and
   * screen-reader users reach the record through the link. Set it only when
   * the link is there — without it, a row with `onRowClick` stays keyboard
   * operable itself, so a page that predates the link is never stranded.
   */
  rowsHaveLinks?: boolean;

  emptyTitle?: string;
  emptyMessage?: string;

  // Server-side filter (debounced). Omit onFilterChange to hide the search box.
  filter?: string;
  onFilterChange?: (value: string) => void;
  filterPlaceholder?: string;
  filterDebounceMs?: number;

  // Server-side sort, driven by columns that declare a sortKey. Each click
  // on a header cycles ascending → descending → unsorted.
  sort?: ResourceSort | null;
  onSortChange?: (sort: ResourceSort | null) => void;

  // Cursor pagination. Omit the handlers to hide the pager.
  onNextPage?: () => void;
  onPrevPage?: () => void;
  hasNextPage?: boolean;
  hasPrevPage?: boolean;
  /** True during a background refetch (page change / filter change). */
  isFetching?: boolean;
  /**
   * The total number of matching records (the List response's total_count),
   * shown in the footer: "1–25 of 112". Omit when the API does not report it.
   */
  totalCount?: number;
  /** Zero-based index of this page's first row, for the footer's range. */
  pageStart?: number;
}

// The controls a row click must not hijack: a click on any of these inside a
// row belongs to that control (and a link inside the row already navigates).
const INTERACTIVE =
  "a, button, input, select, textarea, label, [role='button'], [role='combobox']";

function nextSort(
  current: ResourceSort | null | undefined,
  key: string,
): ResourceSort | null {
  if (current?.key !== key) return { key, descending: false };
  if (!current.descending) return { key, descending: true };
  return null;
}

export function Resource<T>({
  status,
  data,
  columns,
  rowKey,
  title,
  actions,
  toolbar,
  error,
  onRetry,
  onRowClick,
  rowsHaveLinks = false,
  emptyTitle = "Nothing here yet",
  emptyMessage = "No records match.",
  filter,
  onFilterChange,
  filterPlaceholder = "Search…",
  filterDebounceMs = 300,
  sort,
  onSortChange,
  onNextPage,
  onPrevPage,
  hasNextPage,
  hasPrevPage,
  isFetching,
  totalCount,
  pageStart,
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

  const colCount = columns.length;
  const captionText = title ?? "Results";
  const rows = status === "success" && data ? data.length : 0;
  const showPager =
    Boolean(onNextPage || onPrevPage) && Boolean(hasNextPage || hasPrevPage);
  const countText = describeCount(rows, totalCount, pageStart);
  const showFooter = showPager || countText !== "" || Boolean(isFetching);
  const showToolbar = Boolean(onFilterChange || toolbar);

  return (
    <section className="flex flex-col gap-4">
      {(title || actions) && (
        <header className="flex flex-wrap items-center justify-between gap-3">
          {title ? (
            <h1 className="text-xl font-semibold text-ink">{title}</h1>
          ) : (
            <span />
          )}
          {actions ? (
            <div className="flex flex-wrap items-center gap-2">{actions}</div>
          ) : null}
        </header>
      )}

      {showToolbar ? (
        <div className="flex flex-wrap items-start gap-2">
          {onFilterChange ? (
            <input
              type="search"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder={filterPlaceholder}
              aria-label={title ? `Search ${title.toLowerCase()}` : "Search"}
              className="h-9 w-full min-w-0 rounded-md border border-border-strong bg-surface px-3 text-sm text-ink placeholder:text-ink-subtle focus:border-accent focus:outline-none focus:ring-1 focus:ring-accent sm:w-64"
            />
          ) : null}
          {toolbar ? <div className="min-w-0 flex-1">{toolbar}</div> : null}
        </div>
      ) : null}

      <div className="overflow-x-auto rounded-lg border border-border bg-surface">
        <table className="min-w-full divide-y divide-border text-sm">
          <caption className="sr-only">{captionText}</caption>
          <thead className="bg-surface-muted">
            <tr>
              {columns.map((col, i) => {
                const sortable = Boolean(col.sortKey && onSortChange);
                const active = sortable && sort?.key === col.sortKey;
                return (
                  <th
                    key={i}
                    scope="col"
                    aria-sort={
                      active
                        ? sort?.descending
                          ? "descending"
                          : "ascending"
                        : undefined
                    }
                    className={`px-4 py-2.5 text-left font-medium text-ink-muted ${
                      col.className ?? ""
                    }`}
                  >
                    {sortable ? (
                      <button
                        type="button"
                        onClick={() =>
                          onSortChange?.(nextSort(sort, col.sortKey ?? ""))
                        }
                        className="-mx-1 inline-flex items-center gap-1 rounded px-1 hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
                      >
                        {col.header}
                        <span
                          aria-hidden="true"
                          className={active ? "text-ink" : "text-ink-subtle"}
                        >
                          {active ? (sort?.descending ? "↓" : "↑") : "↕"}
                        </span>
                      </button>
                    ) : (
                      col.header
                    )}
                  </th>
                );
              })}
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
                  {...rowInteraction(row, onRowClick, rowsHaveLinks)}
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
        <footer className="flex flex-wrap items-center justify-end gap-2">
          <span
            aria-live="polite"
            className="mr-auto text-sm text-ink-muted tabular-nums"
          >
            {isFetching ? "Loading…" : countText}
          </span>
          {showPager ? (
            <>
              <button
                type="button"
                onClick={onPrevPage}
                disabled={!hasPrevPage}
                className="h-9 rounded-md border border-border-strong px-3 text-sm font-medium text-ink hover:bg-surface-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent disabled:cursor-not-allowed disabled:opacity-40"
              >
                Previous
              </button>
              <button
                type="button"
                onClick={onNextPage}
                disabled={!hasNextPage}
                className="h-9 rounded-md border border-border-strong px-3 text-sm font-medium text-ink hover:bg-surface-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent disabled:cursor-not-allowed disabled:opacity-40"
              >
                Next
              </button>
            </>
          ) : null}
        </footer>
      ) : null}
    </section>
  );
}

/** The props rowInteraction puts on a body row. */
export interface RowInteraction {
  onClick?: (e: MouseEvent<HTMLTableRowElement>) => void;
  onKeyDown?: (e: KeyboardEvent<HTMLTableRowElement>) => void;
  role?: "button";
  tabIndex?: number;
  className?: string;
}

/**
 * rowInteraction is how a body row responds to its onRowClick. A row with
 * no onRowClick is inert data. A row whose cells link to the record
 * (`rowsHaveLinks`) gets a pointer-only click and keeps table semantics. A
 * row with onRowClick and no link is the only way to its record, so it is
 * keyboard operable itself: a tab stop, a button role, Enter/Space. Exported
 * for its tests; not part of the package's index.
 */
export function rowInteraction<T>(
  row: T,
  onRowClick: ((row: T) => void) | undefined,
  rowsHaveLinks: boolean,
): RowInteraction {
  if (!onRowClick) return {};
  const onClick = (e: MouseEvent<HTMLTableRowElement>) => {
    // Leave clicks on the row's own controls (the primary-cell link, a
    // menu) to those controls, and never turn a text selection into a
    // navigation. Only a control INSIDE the row counts: a keyboard-operable
    // row is itself role="button", and closest() matches the row too.
    const control = (e.target as Element).closest(INTERACTIVE);
    if (
      control &&
      control !== e.currentTarget &&
      e.currentTarget.contains(control)
    ) {
      return;
    }
    if (window.getSelection()?.toString()) return;
    onRowClick(row);
  };
  if (rowsHaveLinks) {
    return {
      onClick,
      className:
        "cursor-pointer transition-colors hover:bg-surface-muted focus-within:bg-surface-muted",
    };
  }
  return {
    onClick,
    role: "button",
    tabIndex: 0,
    onKeyDown: (e) => {
      // Only the row's own keys: Enter on a control inside the row is
      // that control's.
      if (e.target !== e.currentTarget) return;
      if (e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        onRowClick(row);
      }
    },
    className:
      "cursor-pointer transition-colors hover:bg-surface-muted focus:bg-surface-muted focus:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent",
  };
}

/**
 * describeCount is the footer's record count: "1–25 of 112" with a known page
 * offset, "112 results" without one, and nothing while no row is shown.
 */
function describeCount(
  rows: number,
  total?: number,
  pageStart?: number,
): string {
  if (total === undefined || rows === 0) return "";
  if (pageStart === undefined) {
    return `${total.toLocaleString()} ${total === 1 ? "result" : "results"}`;
  }
  const first = (pageStart + 1).toLocaleString();
  const last = (pageStart + rows).toLocaleString();
  return `${first}–${last} of ${total.toLocaleString()}`;
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
