// Copyright (c) 2025 Reliant Labs

import { useEffect, useState, type ReactNode } from "react";
import Button from "@/components/forge-ui/button";
import Link from "@/components/forge-ui/link";
import type { DataAccessConfirmation, DbTable } from "@/services/controlPlane/observability/appDatabaseApi";
import type { Panel } from "@/services/controlPlane/observability/types";
import { dataBrowserErrorKind } from "@/services/controlPlane/observability/errors";
import { AccessReasonDialog } from "../../AuditedAccess/AccessReasonDialog";
import { isCurrentReader, useReaderSession } from "../../AuditedAccess/readerSession";
import { clearAccess, isDenied, needsConfirmation, readAccess, writeAccess } from "../../Data/dataAccess-model";
import { DataError } from "../../Data/DataStates";
import { findTable } from "../../Data/dataLinks-model";
import { DataGrid } from "../../Data/DataGrid";
import { patchSearch, type DataSearch } from "../../Data/dataUrl-model";
import { useBrowsableDatabases, useDescribeDatabase } from "../../Data/useDataBrowser";
import { browseSearch, linkablePanel, panelDataTarget, resultRecord } from "./dataPanelModel";
import { PanelBody, type PanelBodyProps } from "./PanelBody";

export function DataPanelBody({ context, ...props }: PanelBodyProps & { context?: PanelDataContext }) {
  if (props.panel.kind === "browse") return <BrowsePanel panel={props.panel} context={context} />;
  const link = props.panel.link;
  if (!link || !linkablePanel(props.panel)) return <PanelBody {...props} />;
  if (typeof link.database !== "string" || typeof link.table !== "string") return <PanelBody {...props} links={{ ...props.links, dataRowHref: () => null }} />;
  if (!context || !props.query.data) return <PanelBody {...props} links={{ ...props.links, dataRowHref: () => null }} />;
  const result = props.query.data;
  return <DataPanelGate key={`${context.env.id}/${link.database}`} context={context} database={link.database} tableName={link.table}>
    {({ table }) => <PanelBody {...props} links={{ ...props.links, dataRowHref: (row) => {
      const target = panelDataTarget(link, resultRecord(result, row), table);
      return target ? context.href(target) : null;
    } }} />}
  </DataPanelGate>;
}

export interface PanelDataContext {
  env: { id: string; name: string };
  href: (search: DataSearch) => string;
  params: Record<string, string | undefined>;
}

interface DataSchema {
  table: DbTable | null;
  tables: DbTable[];
  access?: DataAccessConfirmation;
  onConfirmationLost: () => void;
}

/** Same admission as the browser, with an EXACT database selection: never fall back to a different DB. */
interface DataPanelGateProps {
  context: PanelDataContext;
  database: string;
  tableName: string;
  children: (schema: DataSchema) => ReactNode;
}
export function DataPanelGate(props: DataPanelGateProps) {
  const reader = useReaderSession();
  if (!reader.principalId) return <p role="status">Sign in to read Data links and saved views.</p>;
  return <ReaderDataPanelGate key={`${reader.epoch}/${props.context.env.id}/${props.database}`} {...props} />;
}
function ReaderDataPanelGate({ context, database, tableName, children }: DataPanelGateProps) {
  const reader = useReaderSession();
  const { env } = context;
  const databases = useBrowsableDatabases(env.id);
  const selected = databases.data?.find((db) => db.name === database);
  const [access, setAccess] = useState(() => readAccess(env.id, reader));
  const [asking, setAsking] = useState(false);
  const [confirmationRequired, setConfirmationRequired] = useState(false);
  const audited = (!!selected && needsConfirmation(selected.policy)) || confirmationRequired;
  const permitted = !!selected && selected.reachable && !isDenied(selected.policy) && (!audited || !!access);
  const sentAccess = audited && access ? access : undefined;
  const describe = useDescribeDatabase(env.id, selected?.name ?? null, sentAccess, permitted);
  const confirmationLost = () => {
    if (!isCurrentReader(reader)) return;
    clearAccess(env.id, reader); setAccess(null); setConfirmationRequired(true); setAsking(true);
  };
  useEffect(() => {
    if (isCurrentReader(reader) && describe.error && dataBrowserErrorKind(describe.error) === "confirmation") {
      clearAccess(env.id, reader); setAccess(null); setConfirmationRequired(true);
    }
  }, [describe.error, env.id, reader]);
  const tables = permitted && !describe.error ? describe.data?.tables ?? [] : [];
  const table = findTable(tables, tableName);
  const error = databases.error || describe.error;
  const note = !databases.data ? "Loading Data links…" : !selected ? `${database} is unavailable in ${env.name}.`
    : !selected.reachable ? `${database} is unreachable.` : isDenied(selected.policy) ? `Data access is denied in ${env.name}.`
    : audited && !access ? `Data reads in ${env.name} require a reason.` : !describe.data ? "Loading table description…"
    : !table ? `${tableName} is not readable in ${env.name}.` : null;
  return <>
    {error ? <DataError error={error} env={env.name} subject={database} onRetry={() => void (databases.error ? databases.refetch() : describe.refetch())} /> : note && <p className="text-xs text-ink-muted" role="status">{note}</p>}
    {selected && audited && !access && <Button size="sm" variant="outline" onClick={() => setAsking(true)}>Give a Data access reason</Button>}
    {asking && audited && <AccessReasonDialog open env={env.name} subject="data" testIdPrefix="data" description="Every saved Data view read is recorded with this reader's reason." initialReason={access?.reason ?? ""} onCancel={() => setAsking(false)} onConfirm={(reason) => { if (!isCurrentReader(reader)) return; setAccess(writeAccess(env.id, reason, reader)); setAsking(false); }} />}
    {children({ table, tables, access: sentAccess, onConfirmationLost: confirmationLost })}
  </>;
}

export function BrowsePanel({ panel, context }: { panel: Panel; context?: PanelDataContext }) {
  if (!context) return <p className="text-xs text-ink-muted">Choose one project environment to read this saved view.</p>;
  if (!panel.browse || typeof panel.browse.database !== "string" || typeof panel.browse.table !== "string") return <p role="alert" className="text-xs text-danger-ink">Saved view is unavailable: invalid descriptor.</p>;
  return <DataPanelGate key={`${context.env.id}/${panel.browse.database}`} context={context} database={panel.browse.database} tableName={panel.browse.table}>
    {(schema) => schema.table ? <BoundBrowseGrid key={JSON.stringify([context.env.id, panel.browse, context.params])} panel={panel} context={context} schema={schema as DataSchema & { table: DbTable }} /> : null}
  </DataPanelGate>;
}

function BoundBrowseGrid({ panel, context, schema }: { panel: Panel; context: PanelDataContext; schema: DataSchema & { table: DbTable } }) {
  let initial: DataSearch;
  try { initial = browseSearch(panel.browse!, context.params, schema.table); }
  catch (error) { return <p role="alert" className="text-xs text-danger-ink">{error instanceof Error ? error.message : "Saved view is unavailable"}</p>; }
  return <SavedGrid initial={initial} context={context} schema={schema} />;
}

function SavedGrid({ initial, context, schema }: { initial: DataSearch; context: PanelDataContext; schema: DataSchema & { table: DbTable } }) {
  const [search, setSearch] = useState(initial);
  return <div className="space-y-3" data-testid="saved-browse-panel">
    <Link href={context.href(search)} className="text-xs text-primary-ink">Open in Data</Link>
    <DataGrid key={context.env.id} env={context.env} database={initial.db!} table={schema.table} tables={schema.tables} search={search} access={schema.access}
      onSearch={(patch) => setSearch((current) => patchSearch(current, patch))} linkTo={(target) => context.href({ db: initial.db, ...target })} onConfirmationLost={schema.onConfirmationLost} />
  </div>;
}
