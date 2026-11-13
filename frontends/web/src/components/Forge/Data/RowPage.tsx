// Copyright (c) 2025 Reliant Labs

/**
 * ONE ROW, addressed by its primary key (`row=`): every column, each foreign
 * key a link to the row it references, and RELATED — the tables whose foreign
 * keys point at this row, each with a count and a link to its grid filtered
 * to this row. Related is the reverse direction a plain table viewer lacks:
 * from a chat to its messages, from a user to everything that is theirs.
 *
 * Each Related count is its own BrowseTable (`limit 1`, `count ESTIMATED`),
 * fired through one gate of RELATED_CONCURRENCY so a table referenced by
 * thirty others does not send thirty audited reads at once.
 */

import { useEffect, useMemo, useState } from "react";
import { ArrowLeft, ArrowRight, KeyRound, Link2, SearchX } from "lucide-react";

import Card from "@/components/forge-ui/card";
import Link from "@/components/forge-ui/link";
import type { BrowseInput, DataAccessConfirmation, DbCell, DbForeignKey, DbTable } from "@/services/controlPlane/observability/appDatabaseApi";
import { dataBrowserErrorKind } from "@/services/controlPlane/observability/errors";

import { CopyButton, CutNote, FullValue } from "./CellValue";
import { cellDisplay } from "./dataCells-model";
import { identityTarget } from "./identityLinks-model";
import { IdentityRowLink } from "./IdentityRowLink";
import {
  RELATED_CONCURRENCY,
  createLimiter,
  findTable,
  foreignKeyId,
  foreignKeysOf,
  foreignKeyTarget,
  qualifiedName,
  relatedCountLabel,
  relatedFilters,
  relatedTarget,
  rowFilters,
  rowLabel,
  rowRecord,
  rowValuesOf,
} from "./dataLinks-model";
import { DataError, DataLoading, DataNote } from "./DataStates";
import type { DataSearch } from "./dataUrl-model";
import { useBrowseTable, useRelatedCount } from "./useDataBrowser";

export interface RowPageProps {
  env: { id: string; name: string };
  database: string;
  table: DbTable;
  tables: DbTable[];
  /** The `row` param. */
  rowParam: string;
  access?: DataAccessConfirmation;
  /** Back to the grid, as it was. */
  gridHref: string;
  linkTo: (target: DataSearch) => string;
  onConfirmationLost: () => void;
}

export function RowPage({ env, database, table, tables, rowParam, access, gridHref, linkTo, onConfirmationLost }: RowPageProps) {
  const name = qualifiedName(table);
  const values = rowValuesOf(table, rowParam);
  const request: BrowseInput | null = useMemo(
    () =>
      values
        ? {
            environmentId: env.id,
            database,
            table: name,
            columns: [],
            filters: rowFilters(table, values),
            sort: [],
            limit: 1,
            offset: 0,
            count: "none",
            ...(access ? { access } : {}),
          }
        : null,
    // eslint-disable-next-line react-hooks/exhaustive-deps -- `values` is derived from rowParam
    [env.id, database, name, table, rowParam, access]
  );
  const query = useBrowseTable(request);
  useEffect(() => {
    if (query.error && dataBrowserErrorKind(query.error) === "confirmation") onConfirmationLost();
  }, [query.error, onConfirmationLost]);

  const back = (
    <Link href={gridHref} className="inline-flex items-center gap-1 text-xs text-ink-muted hover:text-ink" data-testid="data-row-back">
      <ArrowLeft className="h-3 w-3" aria-hidden="true" />
      <span className="font-mono">{name}</span>
    </Link>
  );

  if (!values) {
    return (
      <section className="space-y-3" data-testid="data-row-page" data-state="bad-address">
        {back}
        <DataNote testId="data-row-bad-address" icon={<KeyRound className="h-4 w-4" />} title="This link doesn't address a row">
          <p>
            {table.primaryKey.length === 0 ? (
              <>
                <span className="font-mono text-ink">{name}</span> has no primary key, so its rows have no address to link to.
              </>
            ) : (
              <>
                A row of <span className="font-mono text-ink">{name}</span> is addressed by {table.primaryKey.length === 1 ? "its" : "all of its"} key
                column{table.primaryKey.length === 1 ? "" : "s"} (<span className="font-mono text-ink">{table.primaryKey.join(", ")}</span>), and the
                link has a different number of values.
              </>
            )}
          </p>
        </DataNote>
      </section>
    );
  }

  const title = rowLabel(table, values);
  const row = query.data?.rows[0];
  const record = row && query.data ? rowRecord(query.data.columns, row) : null;

  return (
    <section className="space-y-4" data-testid="data-row-page" data-state={record ? "ready" : query.error ? "error" : query.data ? "missing" : "loading"}>
      <div className="space-y-1">
        {back}
        <h2 className="font-mono text-base font-medium text-ink" data-testid="data-row-title">
          {title}
        </h2>
      </div>

      {query.error ? (
        <DataError error={query.error} env={env.name} subject={name} onRetry={() => void query.refetch()} retrying={query.isFetching} testId="data-row-error" />
      ) : !query.data ? (
        <Card padding="none">
          <DataLoading rows={6} testId="data-row-loading" label={`Loading ${title}`} />
        </Card>
      ) : !record ? (
        <DataNote testId="data-row-missing" icon={<SearchX className="h-4 w-4" />} title={`No row in ${name} has ${title}`}>
          <p>It may have been deleted since the link was made, or the link is from another environment.</p>
        </DataNote>
      ) : (
        <>
          <Card padding="none" data-testid="data-row-fields">
            <dl className="divide-y divide-border/60">
              {query.data.columns.map((column, i) => (
                <Field
                  key={column.name}
                  env={env}
                  table={table}
                  tables={tables}
                  column={column.name}
                  resultType={column.pgType}
                  flaggedCut={query.data!.truncatedCells[0]?.includes(i) ?? false}
                  value={row![i] ?? null}
                  record={record}
                  linkTo={linkTo}
                />
              ))}
            </dl>
          </Card>
          <Related env={env} database={database} table={table} tables={tables} record={record} access={access} linkTo={linkTo} />
        </>
      )}
    </section>
  );
}

function Field({
  env,
  table,
  tables,
  column,
  resultType,
  flaggedCut,
  value,
  record,
  linkTo,
}: {
  env: { id: string; name: string };
  table: DbTable;
  tables: DbTable[];
  column: string;
  /** The result column's internal type: how the value reads. The label shows the described type instead. */
  resultType: string;
  flaggedCut: boolean;
  value: DbCell;
  record: ReadonlyMap<string, DbCell>;
  linkTo: (target: DataSearch) => string;
}) {
  const meta = table.columns.find((c) => c.name === column);
  const display = cellDisplay(value, resultType, flaggedCut);
  const identity = identityTarget(meta, value, flaggedCut);
  const references = foreignKeysOf(table, column).flatMap((fk) => {
    const target = foreignKeyTarget(fk, tables, record);
    return target ? [{ fk, href: linkTo(target) }] : [];
  });
  const long = display.full.length > 80 || display.full.includes("\n") || !!display.serverCut;
  const copyLabel = display.serverCut ? "Copy what's shown" : "Copy";
  return (
    <div className="grid grid-cols-1 gap-x-4 gap-y-1 px-4 py-2.5 sm:grid-cols-[minmax(10rem,14rem)_minmax(0,1fr)]" data-testid={`data-field-${column}`}>
      <dt className="min-w-0">
        <span className="flex items-center gap-1.5">
          {meta?.isPrimaryKey && <KeyRound className="h-3 w-3 shrink-0 text-ink-muted" aria-label="primary key" />}
          <span className="truncate font-mono text-xs font-medium text-ink">{column}</span>
        </span>
        <span className="font-mono text-2xs text-ink-muted">
          {meta?.pgType ?? ""}
          {meta && !meta.nullable ? " · not null" : ""}
        </span>
        {meta?.comment && <span className="block text-2xs text-ink-muted">{meta.comment}</span>}
      </dt>
      <dd className="min-w-0 space-y-1">
        {display.kind === "null" || !long ? (
          <div className="flex min-w-0 items-center gap-2">
            {display.kind === "null" ? (
              <FullValue display={display} />
            ) : references.length > 0 ? (
              <Link href={references[0]!.href} className="break-all font-mono text-xs text-primary-ink hover:underline" data-testid="data-field-link">
                {display.full}
              </Link>
            ) : (
              <span className="break-all font-mono text-xs text-ink">{display.full === "" ? <span className="font-sans italic text-ink-subtle">empty string</span> : display.full}</span>
            )}
            {display.kind !== "null" && (
              <span className="opacity-60 hover:opacity-100">
                <CopyButton text={display.full} label="Copy" />
              </span>
            )}
          </div>
        ) : (
          <>
            <FullValue display={display} />
            <CutNote display={display} />
            <CopyButton text={display.full} label={copyLabel} />
          </>
        )}
        {identity && <IdentityRowLink env={env} target={identity} />}
        {references.map(({ fk, href }) => (
          <Link key={fk.constraint} href={href} className="inline-flex items-center gap-1 text-2xs text-ink-muted hover:text-ink" data-testid="data-field-ref">
            <Link2 className="h-3 w-3" aria-hidden="true" />
            references <span className="font-mono">{`${fk.refSchema}.${fk.refTable}`}</span>
          </Link>
        ))}
      </dd>
    </div>
  );
}

function Related({
  env,
  database,
  table,
  tables,
  record,
  access,
  linkTo,
}: {
  env: { id: string; name: string };
  database: string;
  table: DbTable;
  tables: DbTable[];
  record: ReadonlyMap<string, DbCell>;
  access?: DataAccessConfirmation;
  linkTo: (target: DataSearch) => string;
}) {
  // One gate per row page: its counts share it (DataBrowser keys the page by row, so a new row starts a new one).
  const [gate] = useState(() => createLimiter(RELATED_CONCURRENCY));
  const entries = table.referencedBy;
  return (
    <Card padding="none" data-testid="data-related">
      <div className="px-4 pb-2 pt-3">
        <h3 className="text-sm font-medium text-ink">Related</h3>
        <p className="text-xs text-ink-muted">
          {entries.length === 0
            ? `No readable table has a foreign key to ${qualifiedName(table)}.`
            : `Rows in other tables whose foreign keys point at this one.`}
        </p>
      </div>
      {entries.length > 0 && (
        <ul className="divide-y divide-border/60 border-t border-border/60">
          {entries.map((fk) => (
            <RelatedEntry key={foreignKeyId(fk)} env={env} database={database} fk={fk} tables={tables} record={record} access={access} gate={gate} linkTo={linkTo} />
          ))}
        </ul>
      )}
    </Card>
  );
}

function RelatedEntry({
  env,
  database,
  fk,
  tables,
  record,
  access,
  gate,
  linkTo,
}: {
  env: { id: string; name: string };
  database: string;
  fk: DbForeignKey;
  tables: DbTable[];
  record: ReadonlyMap<string, DbCell>;
  access?: DataAccessConfirmation;
  gate: <T>(task: () => Promise<T>) => Promise<T>;
  linkTo: (target: DataSearch) => string;
}) {
  const referencing = `${fk.schema}.${fk.table}`;
  const filters = relatedFilters(fk, record);
  const target = relatedTarget(fk, record);
  const readable = !!findTable(tables, referencing);
  const request: BrowseInput | null = useMemo(
    () =>
      filters && readable
        ? {
            environmentId: env.id,
            database,
            table: referencing,
            columns: [fk.columns[0]!],
            filters,
            sort: [],
            limit: 1,
            offset: 0,
            // Exact, capped by the server at 10,000 (then "10,000+"): an estimate knows nothing of one foreign-key value.
            count: "exact",
            ...(access ? { access } : {}),
          }
        : null,
    // eslint-disable-next-line react-hooks/exhaustive-deps -- filters derive from fk and record
    [env.id, database, referencing, fk, record, readable, access]
  );
  const count = useRelatedCount(request, gate);
  const label = count.data ? relatedCountLabel(count.data) : null;

  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-1 px-4 py-2" data-testid={`data-related-${referencing}.${fk.constraint}`}>
      <div className="min-w-0 flex-1">
        <p className="truncate font-mono text-xs font-medium text-ink">{referencing}</p>
        <p className="truncate font-mono text-2xs text-ink-muted">
          {fk.columns.map((column, i) => `${column} → ${fk.refColumns[i]}`).join(", ")}
        </p>
      </div>
      <span className="shrink-0 text-xs tabular-nums" data-testid="data-related-count">
        {!filters ? (
          <span className="text-ink-muted">none (NULL key)</span>
        ) : count.error ? (
          <span className="text-ink-muted" title={String((count.error as Error).message ?? "")}>
            couldn&apos;t count
          </span>
        ) : label ? (
          <span className={label.none ? "text-ink-muted" : "text-ink"}>{label.none ? "none" : `${label.label} ${label.n === 1 ? "row" : "rows"}`}</span>
        ) : (
          <span className="inline-block h-3 w-10 animate-pulse rounded bg-border/60 align-middle motion-reduce:animate-none" aria-label="Counting" />
        )}
      </span>
      {target && (
        <Link href={linkTo(target)} className="inline-flex shrink-0 items-center gap-1 text-xs text-primary-ink hover:underline" data-testid="data-related-link">
          View rows
          <ArrowRight className="h-3 w-3" aria-hidden="true" />
        </Link>
      )}
    </li>
  );
}
