// Copyright (c) 2025 Reliant Labs

/**
 * One table's rows: the columns the URL chose, filtered, sorted and paged by
 * the server (BrowseTable compiles it into one parameterised SELECT, so the
 * grid never sorts or filters rows itself — a page is all it ever holds).
 *
 *   - A header click sorts by that column (asc, desc, back to the default:
 *     the primary key ascending); shift-click adds it as the next key.
 *   - Paging is by offset. `has_more` decides whether there is a next page;
 *     the total is the planner's estimate (`~1.2k`) until the reader asks for
 *     an exact count, which the server caps (`10,000+`).
 *   - A cell opens a popover with its whole value (JSON pretty-printed) and
 *     Copy. A foreign-key cell's value is a link to the row it references.
 *   - A table with a primary key gives each row a link to its row page.
 */

import { useEffect, useMemo, useState, type MouseEvent } from "react";
import { ArrowDown, ArrowUp, ChevronLeft, ChevronRight, KeyRound, Link2, PanelRightOpen, RefreshCw, SearchX, Table2 } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Link from "@/components/forge-ui/link";
import Tag from "@/components/forge-ui/tag";
import type { BrowseCountMode, BrowsePage, DataAccessConfirmation, DbCell, DbTable } from "@/services/controlPlane/observability/appDatabaseApi";
import { dataBrowserErrorKind } from "@/services/controlPlane/observability/errors";
import { cn } from "@/lib/utils";

import { CONTROL_FIELD } from "../controlStyles";
import { CellPopover, CellPreview, CutMark, type OpenCell } from "./CellValue";
import { ColumnPicker } from "./ColumnPicker";
import { cellDisplay, isNumericType, shortTypeLabel } from "./dataCells-model";
import { foreignKeysOf, foreignKeyTarget, qualifiedName, rowParamOf, rowRecord, totalLabel } from "./dataLinks-model";
import { DataError, DataLoading, DataNote } from "./DataStates";
import {
  DATA_PAGE_SIZES,
  browseRequestOf,
  effectiveSort,
  encodeFilter,
  encodeSort,
  filtersOf,
  nextSort,
  offsetOf,
  pageSizeOf,
  patchSearch,
  previousOffset,
  servable,
  visibleColumns,
  type DataSearch,
} from "./dataUrl-model";
import { FilterBar } from "./FilterBar";
import { useBrowseTable } from "./useDataBrowser";

export interface DataGridProps {
  env: { id: string; name: string };
  database: string;
  table: DbTable;
  tables: DbTable[];
  search: DataSearch;
  access?: DataAccessConfirmation;
  /** Apply a change to the URL state (DataBrowser resets the page where it must). */
  onSearch: (patch: Partial<DataSearch>) => void;
  /** A link to another view of this database. */
  linkTo: (target: DataSearch) => string;
  onConfirmationLost: () => void;
}

const KIND_TAG: Partial<Record<DbTable["kind"], string>> = { view: "view", materialized_view: "materialized view", foreign_table: "foreign table" };

export function DataGrid({ env, database, table, tables, search, access, onSearch, linkTo, onConfirmationLost }: DataGridProps) {
  const name = qualifiedName(table);
  const filters = useMemo(() => filtersOf(search, table), [search, table]);
  // An exact count is asked for, per table and filter set; a new question goes back to the estimate.
  const countScope = `${name}|${filters.map(encodeFilter).join("&")}`;
  const [exactFor, setExactFor] = useState<string | null>(null);
  const count: BrowseCountMode = exactFor === countScope ? "exact" : "estimated";

  const request = useMemo(
    () => browseRequestOf(search, table, { environmentId: env.id, database, count, ...(access ? { access } : {}) }),
    [search, table, env.id, database, count, access]
  );
  const query = useBrowseTable(request, { keepPrevious: true });
  // A placeholder from another table is not this table's rows.
  const page = query.data && query.data.table === name ? query.data : undefined;

  useEffect(() => {
    if (query.error && dataBrowserErrorKind(query.error) === "confirmation") onConfirmationLost();
  }, [query.error, onConfirmationLost]);

  const visible = visibleColumns(search, table);
  const sort = effectiveSort(search, table);
  const size = pageSizeOf(search);
  const offset = offsetOf(search);

  return (
    <section className="flex min-w-0 flex-col gap-3" data-testid="data-grid" data-table={name} data-state={page ? "ready" : query.error ? "error" : "loading"}>
      <header className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0 space-y-0.5">
          <h2 className="flex items-center gap-2 text-base font-medium text-ink">
            <Table2 className="h-4 w-4 shrink-0 text-ink-muted" aria-hidden="true" />
            <span className="truncate font-mono">{name}</span>
            {KIND_TAG[table.kind] && <Tag>{KIND_TAG[table.kind]}</Tag>}
          </h2>
          {table.comment && <p className="text-xs text-ink-muted">{table.comment}</p>}
          {table.primaryKey.length === 0 && (
            <p className="text-xs text-ink-muted" data-testid="data-no-key">
              No primary key: rows have no stable address, so they open no row page and pages may shift between reads.
            </p>
          )}
        </div>
        <Button
          variant="ghost"
          size="sm"
          onClick={() => void query.refetch()}
          loading={query.isFetching}
          disabled={query.isFetching}
          leftIcon={<RefreshCw className="h-3 w-3" />}
          data-testid="data-refresh"
        >
          Refresh
        </Button>
      </header>

      <div className="flex flex-wrap items-center justify-between gap-2">
        <FilterBar columns={table.columns} filters={filters} onChange={(next) => onSearch({ f: next.map(encodeFilter) })} />
        <ColumnPicker columns={table.columns} visible={visible} onChange={(cols) => onSearch({ cols })} />
      </div>

      {query.error && !page ? (
        <DataError
          error={query.error}
          env={env.name}
          subject={name}
          onRetry={() => void query.refetch()}
          retrying={query.isFetching}
          testId="data-grid-error"
          actions={
            filters.length > 0 && dataBrowserErrorKind(query.error) === "invalid" ? (
              <Button variant="ghost" size="sm" onClick={() => onSearch({ f: [] })}>
                Clear filters
              </Button>
            ) : undefined
          }
        />
      ) : !page ? (
        <div className="rounded-lg border border-border bg-surface">
          <DataLoading rows={Math.min(size, 10)} testId="data-grid-loading" label={`Loading ${name}`} />
        </div>
      ) : page.rows.length === 0 ? (
        <EmptyPage name={name} filtered={filters.length > 0} offset={offset} onClearFilters={() => onSearch({ f: [] })} onFirstPage={() => onSearch({ offset: undefined })} />
      ) : (
        <>
          <GridTable
            page={page}
            table={table}
            tables={tables}
            visible={visible}
            sort={sort}
            stale={query.isPlaceholderData || (query.isFetching && !query.isPending)}
            onSort={(column, additive) => onSearch({ sort: nextSort(sort, column, table, additive).map(encodeSort) })}
            linkTo={linkTo}
            // A row opens on the grid's whole view, so the row page's Back returns to it.
            rowHref={(row) => linkTo(patchSearch(search, { row }))}
          />
          {page.truncated && (
            <p className="text-xs text-ink-muted" data-testid="data-page-cut">
              The server cut this page at {page.rows.length.toLocaleString("en-US")} of {size.toLocaleString("en-US")} rows to keep the response small. Next
              continues from row {(page.nextOffset + 1).toLocaleString("en-US")}, so nothing is skipped; fewer columns fit more rows on a page.
            </p>
          )}
        </>
      )}

      {page && (page.rows.length > 0 || offset > 0) && (
        <Pager
          page={page}
          size={size}
          count={count}
          stale={query.isPlaceholderData}
          onExact={() => setExactFor(countScope)}
          onOffset={(next) => onSearch({ offset: next })}
          onSize={(next) => onSearch({ size: next })}
        />
      )}
    </section>
  );
}

function EmptyPage({
  name,
  filtered,
  offset,
  onClearFilters,
  onFirstPage,
}: {
  name: string;
  filtered: boolean;
  offset: number;
  onClearFilters: () => void;
  onFirstPage: () => void;
}) {
  if (offset > 0) {
    return (
      <DataNote
        testId="data-empty-page"
        icon={<SearchX className="h-4 w-4" />}
        title={`Nothing past row ${offset.toLocaleString("en-US")}`}
        actions={
          <Button variant="outline" size="sm" onClick={onFirstPage}>
            Back to the first rows
          </Button>
        }
      >
        <p>The rows end before this page. Rows may have been deleted since the link was made.</p>
      </DataNote>
    );
  }
  if (filtered) {
    return (
      <DataNote
        testId="data-empty-filtered"
        icon={<SearchX className="h-4 w-4" />}
        title="No rows match these filters"
        actions={
          <Button variant="outline" size="sm" onClick={onClearFilters}>
            Clear filters
          </Button>
        }
      >
        <p>Every filter must hold for a row to show. A value is compared as the column&apos;s type, so &ldquo;42&rdquo; matches 42 but &ldquo;042&rdquo; does not match a text 42.</p>
      </DataNote>
    );
  }
  return (
    <DataNote testId="data-empty-table" icon={<Table2 className="h-4 w-4" />} title={`${name} is empty`}>
      <p>It has no rows yet.</p>
    </DataNote>
  );
}

function GridTable({
  page,
  table,
  tables,
  visible,
  sort,
  stale,
  onSort,
  linkTo,
  rowHref,
}: {
  page: BrowsePage;
  table: DbTable;
  tables: DbTable[];
  visible: string[];
  sort: { column: string; desc: boolean }[];
  stale: boolean;
  onSort: (column: string, additive: boolean) => void;
  linkTo: (target: DataSearch) => string;
  rowHref: (row: string) => string;
}) {
  const [open, setOpen] = useState<OpenCell | null>(null);
  const meta = useMemo(() => new Map(table.columns.map((c) => [c.name, c])), [table.columns]);
  // A column's place in the result, and its RESULT type (the internal name, "int8"): what a value reads as.
  // The header shows the DESCRIBED type ("bigint"). The two spellings are never compared.
  const index = useMemo(() => new Map(page.columns.map((c, i) => [c.name, i])), [page.columns]);
  const resultType = (name: string) => page.columns[index.get(name)!]?.pgType ?? "";
  const shown = visible.filter((name) => index.has(name));
  const hasKey = table.primaryKey.length > 0;
  const fkColumns = useMemo(() => new Set(table.foreignKeys.flatMap((fk) => fk.columns)), [table.foreignKeys]);

  const openCell = (event: MouseEvent<HTMLElement>, cell: Omit<OpenCell, "anchor">) => {
    const rect = event.currentTarget.getBoundingClientRect();
    setOpen({ ...cell, anchor: { x: rect.left, y: rect.bottom } });
  };

  return (
    <div
      className={cn("max-h-[70vh] overflow-auto rounded-lg border border-border bg-surface transition-opacity", stale && "opacity-60")}
      aria-busy={stale}
      data-testid="data-grid-scroll"
    >
      <table className="min-w-full border-separate border-spacing-0 text-sm" aria-label={qualifiedName(table)} aria-rowcount={-1}>
        <thead>
          <tr>
            {hasKey && (
              <th scope="col" className="sticky left-0 top-0 z-20 w-8 border-b border-border bg-surface">
                <span className="sr-only">Open row</span>
              </th>
            )}
            {shown.map((name) => {
              const column = meta.get(name);
              const at = sort.findIndex((s) => s.column === name);
              const direction = at >= 0 ? (sort[at]!.desc ? "descending" : "ascending") : undefined;
              const numeric = isNumericType(resultType(name));
              const described = column?.pgType ?? "";
              return (
                <th
                  key={name}
                  scope="col"
                  aria-sort={at === 0 ? direction : undefined}
                  className="sticky top-0 z-10 whitespace-nowrap border-b border-border bg-surface px-0 text-left align-bottom font-normal"
                >
                  <button
                    type="button"
                    onClick={(event) => onSort(name, event.shiftKey)}
                    title={`${name} · ${described}${column?.comment ? `\n${column.comment}` : ""}\nSort by ${name}${sort.length > 0 ? " (shift-click to add)" : ""}`}
                    className={cn(
                      "flex w-full items-center gap-1.5 px-3 py-2 hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring",
                      numeric && "justify-end"
                    )}
                    data-testid={`data-sort-${name}`}
                  >
                    {column?.isPrimaryKey && <KeyRound className="h-3 w-3 shrink-0 text-ink-muted" aria-label="primary key" />}
                    {fkColumns.has(name) && <Link2 className="h-3 w-3 shrink-0 text-ink-muted" aria-label="foreign key" />}
                    <span className="font-mono text-xs font-medium text-ink">{name}</span>
                    <span className="font-mono text-2xs text-ink-muted">{shortTypeLabel(described)}</span>
                    {direction && (
                      <span className="inline-flex items-center text-primary-ink" aria-hidden="true">
                        {direction === "ascending" ? <ArrowUp className="h-3 w-3" /> : <ArrowDown className="h-3 w-3" />}
                        {sort.length > 1 && <span className="text-2xs">{at + 1}</span>}
                      </span>
                    )}
                  </button>
                </th>
              );
            })}
          </tr>
        </thead>
        <tbody>
          {page.rows.map((row, rowIndex) => {
            const record = rowRecord(page.columns, row);
            const cutCells = new Set(page.truncatedCells[rowIndex] ?? []);
            const rowParam = hasKey ? rowParamOf(table, record) : null;
            return (
              <tr key={rowParam ?? rowIndex} className="group hover:bg-ink/5" data-testid="data-row">
                {hasKey && (
                  <td className="sticky left-0 z-[1] h-8 border-b border-border/60 bg-surface px-1 group-hover:bg-surface-sunken">
                    {rowParam !== null && (
                      <Link
                        href={rowHref(rowParam)}
                        aria-label={`Open row ${table.primaryKey.map((k) => `${k} ${record.get(k)}`).join(", ")}`}
                        className="flex h-6 w-6 items-center justify-center rounded text-ink-muted hover:bg-ink/5 hover:text-ink"
                        data-testid="data-row-open"
                      >
                        <PanelRightOpen className="h-3.5 w-3.5" aria-hidden="true" />
                      </Link>
                    )}
                  </td>
                )}
                {shown.map((name) => (
                  <GridCell
                    key={name}
                    name={name}
                    resultType={resultType(name)}
                    flaggedCut={cutCells.has(index.get(name)!)}
                    value={row[index.get(name)!] ?? null}
                    target={fkColumns.has(name) ? fkTarget(table, tables, name, record) : null}
                    linkTo={linkTo}
                    onOpen={(event, link) =>
                      openCell(event, {
                        key: `${rowIndex}:${name}`,
                        column: name,
                        resultType: resultType(name),
                        describedType: meta.get(name)?.pgType ?? "",
                        flaggedCut: cutCells.has(index.get(name)!),
                        value: row[index.get(name)!] ?? null,
                        ...(link ? { link } : {}),
                      })
                    }
                  />
                ))}
              </tr>
            );
          })}
        </tbody>
      </table>
      <CellPopover cell={open} onClose={() => setOpen(null)} />
    </div>
  );
}

/** The first foreign key through `column` that leads somewhere readable. */
function fkTarget(table: DbTable, tables: DbTable[], column: string, record: ReadonlyMap<string, DbCell>): { target: DataSearch; label: string } | null {
  for (const fk of foreignKeysOf(table, column)) {
    const target = foreignKeyTarget(fk, tables, record);
    if (target) return { target, label: `Open in ${fk.refSchema}.${fk.refTable}` };
  }
  return null;
}

function GridCell({
  name,
  resultType,
  flaggedCut,
  value,
  target,
  linkTo,
  onOpen,
}: {
  name: string;
  resultType: string;
  flaggedCut: boolean;
  value: DbCell;
  target: { target: DataSearch; label: string } | null;
  linkTo: (target: DataSearch) => string;
  onOpen: (event: MouseEvent<HTMLElement>, link?: { href: string; label: string }) => void;
}) {
  const display = cellDisplay(value, resultType, flaggedCut);
  const numeric = isNumericType(resultType);
  const href = target ? linkTo(target.target) : null;
  return (
    <td className="max-w-[24rem] border-b border-border/60 px-0 py-0 align-top" data-column={name} data-server-cut={display.serverCut ? "true" : undefined}>
      {href ? (
        <div className="flex h-8 items-center gap-1 px-3">
          <Link href={href} className="min-w-0 truncate font-mono text-xs text-primary-ink hover:underline" title={target!.label} data-testid="data-fk-link">
            {display.short}
          </Link>
          <button
            type="button"
            aria-label={`Show ${name} value`}
            onClick={(event) => onOpen(event, { href, label: target!.label })}
            className="shrink-0 rounded px-1 text-2xs text-ink-muted opacity-0 hover:bg-ink/5 hover:text-ink focus-visible:opacity-100 group-hover:opacity-100"
          >
            …
          </button>
        </div>
      ) : display.kind === "null" ? (
        <div className="flex h-8 items-center px-3">
          <CellPreview display={display} />
        </div>
      ) : (
        <button
          type="button"
          onClick={(event) => onOpen(event)}
          aria-label={`${name}: ${display.short}${display.serverCut ? " (cut by the server)" : ""}`}
          className="flex h-8 w-full min-w-0 items-center gap-1.5 px-3 text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
          data-testid="data-cell"
        >
          <CellPreview display={display} numeric={numeric} />
          {display.serverCut && <CutMark />}
        </button>
      )}
    </td>
  );
}

function Pager({
  page,
  size,
  count,
  stale,
  onExact,
  onOffset,
  onSize,
}: {
  page: BrowsePage;
  size: number;
  count: BrowseCountMode;
  /** The page on screen is the previous request's, kept while the next loads: paging from it would page from the wrong place. */
  stale: boolean;
  onExact: () => void;
  onOffset: (offset: number) => void;
  onSize: (size: number) => void;
}) {
  // Where the page on screen starts, from the page itself (next_offset is its
  // offset plus its rows), so a placeholder kept while the next page loads
  // still says its own rows.
  const start = page.nextOffset - page.rows.length;
  const total = totalLabel(page, start);
  const first = page.rows.length > 0 ? start + 1 : 0;
  const last = page.nextOffset;
  const atCap = !servable(page.nextOffset);
  return (
    <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-ink-muted" data-testid="data-pager">
      <p className="flex flex-wrap items-center gap-x-2" aria-live="polite">
        <span data-testid="data-range">
          {first > 0 ? (
            <>
              Rows <span className="tabular-nums text-ink">{first.toLocaleString("en-US")}</span>–<span className="tabular-nums text-ink">{last.toLocaleString("en-US")}</span>
            </>
          ) : (
            "No rows"
          )}
          {total && (
            <>
              {" "}
              of <span className="tabular-nums text-ink" data-testid="data-total">{total}</span>
            </>
          )}
        </span>
        {page.hasMore && count === "estimated" && (
          <button type="button" onClick={onExact} className="text-primary-ink hover:underline" data-testid="data-count-exact">
            Count exactly
          </button>
        )}
        {atCap && page.hasMore && <span>Rows past 1,000,000 can&apos;t be read: narrow with a filter or a sort.</span>}
      </p>
      <div className="flex items-center gap-2">
        <label className="flex items-center gap-1.5">
          Rows per page
          <select
            value={size}
            onChange={(event) => onSize(Number(event.target.value))}
            className={cn(CONTROL_FIELD, "h-7 px-1.5 text-xs")}
            data-testid="data-page-size"
          >
            {DATA_PAGE_SIZES.map((option) => (
              <option key={option} value={option}>
                {option}
              </option>
            ))}
          </select>
        </label>
        <Button
          variant="outline"
          size="xs"
          onClick={() => onOffset(previousOffset(start, size))}
          disabled={stale || start <= 0}
          leftIcon={<ChevronLeft className="h-3 w-3" />}
          data-testid="data-page-prev"
        >
          Previous
        </Button>
        {/* Next starts where this page ended: next_offset, which is not offset + size when the server cut the page short. */}
        <Button
          variant="outline"
          size="xs"
          onClick={() => onOffset(page.nextOffset)}
          disabled={stale || !page.hasMore || atCap}
          rightIcon={<ChevronRight className="h-3 w-3" />}
          data-testid="data-page-next"
        >
          Next
        </Button>
      </div>
    </div>
  );
}
