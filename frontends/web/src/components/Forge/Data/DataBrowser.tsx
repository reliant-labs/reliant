// Copyright (c) 2025 Reliant Labs

/**
 * THE DATA BROWSER for one environment: its databases, the tables of the
 * chosen one, and a table's grid or one row's page. Read-only, and exactly
 * what the environment's read-only role reads (control-plane
 * docs/data-browser.md); no SQL is written here or sent.
 *
 * Free of the console: the environment, the URL state and the way to change
 * it are props, so DataPage mounts it in the console and the preview and
 * tests mount it on fixtures.
 *
 * In order, before any row is read:
 *   1. ListBrowsableDatabases names the databases and each one's policy and
 *      reachability (it reads no rows and writes no audit row). One is
 *      chosen: the URL's, else the first that can be read.
 *   2. A database that cannot be reached, or whose policy is deny, says so.
 *   3. An AUDITED one asks for a reason once per tab session
 *      (dataAccess-model.ts), and every read then carries it.
 *   4. DescribeDatabase lists the tables; the table and row come from the URL.
 */

import { useCallback, useEffect, useMemo, useState } from "react";
import { Database, MousePointerClick, ShieldCheck } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Card from "@/components/forge-ui/card";
import type { BrowsableDb, DataAccessConfirmation } from "@/services/controlPlane/observability/appDatabaseApi";
import { dataBrowserErrorKind } from "@/services/controlPlane/observability/errors";

import { AccessReasonDialog } from "./AccessReasonDialog";
import { clearAccess, isDenied, needsConfirmation, readAccess, writeAccess } from "./dataAccess-model";
import { DatabaseSelector, PolicyTag } from "./DatabaseSelector";
import { DataGrid } from "./DataGrid";
import { findTable } from "./dataLinks-model";
import { appDataTargets, parseAppDataSubject } from "./identityLinks-model";
import Link from "@/components/forge-ui/link";
import { DataDenied, DataError, DataLoading, DataNote } from "./DataStates";
import { patchSearch, type DataSearch } from "./dataUrl-model";
import { RowPage } from "./RowPage";
import { TableRail } from "./TableRail";
import { useBrowsableDatabases, useDescribeDatabase } from "./useDataBrowser";

export interface DataBrowserProps {
  /** The control-plane environment (deploy_environments.id) and its name. */
  env: { id: string; name: string };
  search: DataSearch;
  /** Move to another view. `replace` for a correction (an auto-chosen database) rather than a step. */
  onNavigate: (next: DataSearch, options?: { replace?: boolean }) => void;
  /** The URL of a view, for links (a table, a row, a foreign key). */
  href: (next: DataSearch) => string;
}

/** The database a bare link opens: the first that can be read, else the first. */
export function defaultDatabase(databases: readonly BrowsableDb[]): BrowsableDb | null {
  return databases.find((db) => db.reachable && !isDenied(db.policy)) ?? databases[0] ?? null;
}

export function DataBrowser({ env, search, onNavigate, href }: DataBrowserProps) {
  const databases = useBrowsableDatabases(env.id);
  const list = useMemo(() => databases.data ?? [], [databases.data]);
  const named = list.find((db) => db.name === search.db) ?? null;
  const selected = named ?? defaultDatabase(list);

  // A bare link, or one naming a database this environment does not have, opens the default.
  // The rest of the link stays: a table that exists there too still opens.
  useEffect(() => {
    if (databases.data && selected && search.db !== selected.name) {
      onNavigate({ ...search, db: selected.name }, { replace: true });
    }
  }, [databases.data, selected, search, onNavigate]);

  const [access, setAccess] = useState<DataAccessConfirmation | null>(() => readAccess(env.id));
  const [asking, setAsking] = useState(false);
  const confirmationLost = useCallback(() => {
    clearAccess(env.id);
    setAccess(null);
    setAsking(true);
  }, [env.id]);

  const audited = !!selected && needsConfirmation(selected.policy);
  const waitingForReason = audited && !access;
  useEffect(() => {
    if (waitingForReason) setAsking(true);
  }, [waitingForReason]);

  const readable = !!selected && selected.reachable && !isDenied(selected.policy);
  const sentAccess = audited && access ? access : undefined;
  const describe = useDescribeDatabase(env.id, selected?.name ?? null, sentAccess, readable && !waitingForReason);
  useEffect(() => {
    if (describe.error && dataBrowserErrorKind(describe.error) === "confirmation") confirmationLost();
  }, [describe.error, confirmationLost]);

  const go = useCallback((patch: Partial<DataSearch>) => onNavigate(patchSearch(search, patch)), [onNavigate, search]);
  const linkTo = useCallback((target: DataSearch) => href({ db: selected?.name, ...target }), [href, selected?.name]);

  if (databases.error) {
    return <DataError error={databases.error} env={env.name} subject={`${env.name}'s databases`} onRetry={() => void databases.refetch()} retrying={databases.isFetching} testId="data-databases-error" />;
  }
  if (!databases.data) {
    return (
      <Card padding="none">
        <DataLoading rows={4} testId="data-databases-loading" label={`Loading ${env.name}'s databases`} />
      </Card>
    );
  }
  if (list.length === 0) {
    return (
      <DataNote testId="data-no-databases" icon={<Database className="h-4 w-4" />} title={`${env.name} has no databases`}>
        <p>
          The browser shows the databases an environment declares: each <code className="font-mono text-ink">forge.ManagedDatabase</code> and{" "}
          <code className="font-mono text-ink">forge.ExternalDatabase</code> in its last recorded release. {env.name}&apos;s declares none.
        </p>
      </DataNote>
    );
  }

  const tables = describe.data?.tables ?? [];
  const table = findTable(tables, search.table);
  const subject = parseAppDataSubject(search.subject, env.id);
  const subjectTargets = subject ? appDataTargets(tables, subject) : [];

  return (
    <div className="space-y-4" data-testid="data-browser" data-env={env.name} data-db={selected?.name ?? ""}>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <DatabaseSelector databases={list} selected={selected?.name ?? null} onSelect={(name) => go({ db: name })} />
        {selected && (
          <div className="flex flex-wrap items-center gap-2 text-xs text-ink-muted">
            <PolicyTag policy={selected.policy} />
            {audited && access && (
              <span className="flex min-w-0 items-center gap-1.5" data-testid="data-reason">
                <span className="max-w-[24rem] truncate" title={access.reason}>
                  Reading as: <span className="text-ink">{access.reason}</span>
                </span>
                <button type="button" className="text-primary-ink hover:underline" onClick={() => setAsking(true)} data-testid="data-reason-change">
                  Change
                </button>
              </span>
            )}
          </div>
        )}
      </div>

      {asking && audited && (
        <AccessReasonDialog
          open
          env={env.name}
          initialReason={access?.reason ?? ""}
          onConfirm={(reason) => {
            setAccess(writeAccess(env.id, reason));
            setAsking(false);
          }}
          onCancel={() => setAsking(false)}
        />
      )}

      {!selected ? null : !selected.reachable ? (
        <DataNote testId="data-unreachable" title={`${selected.name} can't be browsed from Reliant`}>
          <p>{selected.unreachableReason || "The control plane has no route to it."}</p>
        </DataNote>
      ) : isDenied(selected.policy) ? (
        <DataDenied env={env.name} database={selected.name} unknown={selected.policy === "unknown"} />
      ) : waitingForReason ? (
        <DataNote
          testId="data-needs-reason"
          icon={<ShieldCheck className="h-4 w-4" />}
          title={`${env.name} is audited: reads need a reason`}
          actions={
            <Button variant="primary" size="sm" onClick={() => setAsking(true)} data-testid="data-give-reason">
              Give a reason
            </Button>
          }
        >
          <p>Nothing has been read yet. Each table and page you open is recorded with the reason you give, once, for this tab.</p>
        </DataNote>
      ) : describe.error ? (
        <DataError
          error={describe.error}
          env={env.name}
          subject={`the ${selected.name} database`}
          onRetry={() => void describe.refetch()}
          retrying={describe.isFetching}
          testId="data-describe-error"
        />
      ) : (
        <div className="grid gap-4 lg:grid-cols-[15rem_minmax(0,1fr)]">
          <aside className="lg:sticky lg:top-4 lg:max-h-[calc(100dvh-10rem)] lg:self-start">
            {describe.data ? (
              <TableRail tables={tables} selected={table ? search.table! : null} tableHref={(name) => linkTo({ table: name })} />
            ) : (
              <DataLoading rows={10} testId="data-tables-loading" label="Loading tables" />
            )}
          </aside>
          <div className="min-w-0">
            {!describe.data ? (
              <Card padding="none">
                <DataLoading rows={8} testId="data-describe-loading" label={`Reading ${selected.name}'s tables`} />
              </Card>
            ) : tables.length === 0 ? (
              <DataNote testId="data-no-tables" title={`Nothing in ${selected.name} is readable`}>
                <p>The read-only role can SELECT no table or view here. Forge grants it every table but the ones marked forge:secret.</p>
              </DataNote>
            ) : search.subject && !search.table ? (
              <DataNote testId="data-identity-tables" title="App data">
                {!subject ? <p>This Identity link belongs to another environment or is invalid. Open Identity in this environment to choose a user.</p> : <>
                  <p>Direct marked columns in {selected.name}. Choose another database above to inspect it. Up to 25 links from the first 200 tables and 200 columns per table; no rows or counts are read until you open one. Use Related on a row for descendants.</p>
                  {subjectTargets.length === 0 ? <p>No explicit identity-subject columns found in this bounded description.</p> : <ul className="space-y-2">{subjectTargets.map((target) => <li key={target.label}><Link href={linkTo(target.search)} className="text-primary-ink hover:underline">{target.label}</Link></li>)}</ul>}
                </>}
              </DataNote>
            ) : !search.table ? (
              <DataNote testId="data-pick-table" icon={<MousePointerClick className="h-4 w-4" />} title="Choose a table">
                <p>
                  {tables.length} {tables.length === 1 ? "table" : "tables and views"} in <span className="font-mono text-ink">{selected.name}</span>. Open one to
                  page through its rows; open a row to see what points at it.
                </p>
              </DataNote>
            ) : !table ? (
              <DataNote testId="data-table-missing" title={`${search.table} isn't readable in ${selected.name}`}>
                <p>
                  It does not exist in {env.name}, or the read-only role cannot SELECT it. Links carry the table across environments, and the
                  schemas can differ.
                </p>
              </DataNote>
            ) : search.row !== undefined ? (
              <RowPage
                key={`${search.table}|${search.row}`}
                env={env}
                database={selected.name}
                table={table}
                tables={tables}
                rowParam={search.row}
                access={sentAccess}
                gridHref={href({ ...search, row: undefined })}
                linkTo={linkTo}
                onConfirmationLost={confirmationLost}
              />
            ) : (
              <DataGrid
                env={env}
                database={selected.name}
                table={table}
                tables={tables}
                search={search}
                access={sentAccess}
                onSearch={go}
                linkTo={linkTo}
                onConfirmationLost={confirmationLost}
              />
            )}
          </div>
        </div>
      )}
    </div>
  );
}
