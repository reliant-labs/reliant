// Copyright (c) 2026 Reliant Labs
import { useMemo, useState } from "react";
import { Network, ScrollText } from "lucide-react";
import Button from "@/components/forge-ui/button";
import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";
import type { RPCCatalog, RPCMethod } from "@/services/controlPlane/observability/graphTypes";
import { catalogMethods, parseDeclaredCatalog, schemaText, type DeclaredCatalog } from "./catalogModel";
import { errorFraction, fmtCount, fmtErrorRate, fmtLatency, histogramQuantile, shortService } from "./graphStats";

/** One row per full RPC identity: observed traffic joined to declared metadata. */
export type MethodRow = {
  id: string; service: string; method: string;
  requests: bigint; faults: bigint; userOutcomes: bigint;
  p50?: number; p95?: number; observed: boolean;
  /** Declared metadata (types, streaming, auth), when a catalog supplied it. */
  meta?: RPCMethod;
};

export function methodRows(catalog: RPCCatalog, service = ""): MethodRow[] {
  const rows = new Map<string, MethodRow>();
  for (const m of catalogMethods(catalog, service)) {
    const row = rows.get(m.id) ?? { id: m.id, service: m.service, method: m.method, requests: 0n, faults: 0n, userOutcomes: 0n, observed: false };
    if (m.evidence === "observed") {
      row.observed = true;
      row.requests += m.observedCount;
      row.faults += m.faults;
      row.userOutcomes += m.userOutcomes;
      row.p50 = histogramQuantile(m.duration, 0.5);
      row.p95 = histogramQuantile(m.duration, 0.95);
    }
    // The server merges declared fields into the observed entry when both exist.
    if (m.evidence === "declared" || m.inputType || m.authRequired !== undefined) row.meta = m;
    rows.set(m.id, row);
  }
  return [...rows.values()];
}

type SortKey = "method" | "requests" | "errors" | "p50" | "p95";
const compareBig = (a: bigint, b: bigint) => (a > b ? 1 : a < b ? -1 : 0);
function sortRows(rows: MethodRow[], key: SortKey, dir: "asc" | "desc"): MethodRow[] {
  const sign = dir === "asc" ? 1 : -1;
  const value: Record<SortKey, (a: MethodRow, b: MethodRow) => number> = {
    method: (a, b) => a.id.localeCompare(b.id),
    requests: (a, b) => compareBig(a.requests, b.requests),
    errors: (a, b) => errorFraction(a.faults, a.requests) - errorFraction(b.faults, b.requests),
    p50: (a, b) => (a.p50 ?? -1) - (b.p50 ?? -1),
    p95: (a, b) => (a.p95 ?? -1) - (b.p95 ?? -1),
  };
  return [...rows].sort((a, b) => sign * value[key](a, b) || a.id.localeCompare(b.id));
}

export function RPCExplorer({ catalog, service, rpcId, envNames, onSelect, onGraph, onTraces }: {
  catalog: RPCCatalog; service: string; rpcId: string; envNames: ReadonlyMap<string, string>;
  onSelect: (rpc: string) => void; onGraph: (rpc: string) => void; onTraces: (rpc: string) => void;
}) {
  const [sort, setSort] = useState<{ key: SortKey; dir: "asc" | "desc" }>({ key: "requests", dir: "desc" });
  const all = useMemo(() => methodRows(catalog, service), [catalog, service]);
  const rows = useMemo(() => sortRows(all, sort.key, sort.dir), [all, sort]);
  const selected = all.find((m) => m.id === rpcId);
  const columns: DataTableColumn<MethodRow>[] = [
    { key: "method", header: "Method", sortable: true, render: (_, r) => <button type="button" title={r.id} onClick={() => onSelect(r.id)}
      className="max-w-full truncate text-left hover:underline"><span className="text-ink-muted">{shortService(r.service)} / </span><span className="font-medium text-ink">{r.method}</span></button> },
    { key: "requests", header: "Requests", numeric: true, sortable: true, render: (_, r) => r.observed ? fmtCount(r.requests) : <span className="text-ink-muted">—</span> },
    { key: "errors", header: "Error rate", numeric: true, sortable: true, render: (_, r) => <span className={r.faults > 0n ? "text-danger-ink" : undefined}>{fmtErrorRate(r.faults, r.requests)}</span> },
    { key: "p50", header: "p50", numeric: true, sortable: true, render: (_, r) => fmtLatency(r.p50) },
    { key: "p95", header: "p95", numeric: true, sortable: true, render: (_, r) => fmtLatency(r.p95) },
    { key: "traces", header: "Traces", srHeader: true, interactive: true, width: "6rem", render: (_, r) => r.observed &&
      <Button variant="ghost" size="sm" onClick={() => onTraces(r.id)} aria-label={`Traces for ${r.id}`}>Traces</Button> },
  ];
  return <div className="space-y-4" data-testid="rpc-explorer">
    {selected && <MethodDetail row={selected} catalog={catalog} envNames={envNames} onGraph={onGraph} onTraces={onTraces} onClose={() => onSelect("")} />}
    <DataTable<MethodRow> ariaLabel="RPC methods" columns={columns} data={rows} compact getRowKey={(r) => r.id}
      getRowProps={(r) => ({ className: r.id === rpcId ? "bg-accent-surface" : undefined, "data-testid": `rpc-row-${r.id}` })}
      onSort={(key, dir) => setSort({ key: key as SortKey, dir })}
      emptyMessage={<div className="py-6 text-center"><p className="font-medium text-ink">No RPC traffic in this window</p>
        <p className="mt-1 text-sm text-ink-muted">{service ? "This service received no requests. Pick another service or widen the time range." : "Widen the time range, or check that your services export traces."}</p></div>} />
  </div>;
}

function MethodDetail({ row, catalog, envNames, onGraph, onTraces, onClose }: {
  row: MethodRow; catalog: RPCCatalog; envNames: ReadonlyMap<string, string>;
  onGraph: (rpc: string) => void; onTraces: (rpc: string) => void; onClose: () => void;
}) {
  const artifacts = useMemo(() => catalog.declaredCatalogs.map((a) => ({ ...a, ...parseDeclaredCatalog(a.json) })), [catalog.declaredCatalogs]);
  const d = row.meta;
  return <section aria-label="RPC method detail" className="space-y-4 rounded-lg border border-border bg-surface p-4">
    <div className="flex flex-wrap items-start justify-between gap-3">
      <div className="min-w-0">
        <p className="text-xs text-ink-muted">{row.service}</p>
        <h3 className="break-words text-base font-medium text-ink">{row.method}</h3>
      </div>
      <div className="flex gap-2">
        <Button variant="outline" size="sm" leftIcon={<Network />} onClick={() => onGraph(row.id)}>Show in graph</Button>
        {row.observed && <Button variant="outline" size="sm" leftIcon={<ScrollText />} onClick={() => onTraces(row.id)}>View traces</Button>}
        <Button variant="ghost" size="sm" onClick={onClose}>Close</Button>
      </div>
    </div>
    <dl className="flex flex-wrap gap-x-8 gap-y-2 text-sm">
      {[["Requests", row.observed ? fmtCount(row.requests) : "—"], ["Error rate", fmtErrorRate(row.faults, row.requests)],
        ["Client errors", row.observed ? fmtCount(row.userOutcomes) : "—"], ["p50", fmtLatency(row.p50)], ["p95", fmtLatency(row.p95)],
        ...(d ? [["Type", d.streaming || "unary"], ["Auth", d.authRequired === undefined ? "—" : d.authRequired ? "required" : "public"]] : [])]
        .map(([label, value]) => <div key={label}><dt className="text-xs text-ink-muted">{label}</dt><dd className="tabular-nums text-ink">{value}</dd></div>)}
    </dl>
    {artifacts.map((artifact) => artifact.catalog && <DeclaredSchemas key={`${artifact.envId}/${artifact.bundleDigest}`} catalog={artifact.catalog}
      envName={artifacts.length > 1 ? envNames.get(artifact.envId) : undefined} service={row.service} rpcId={row.id} />)}
  </section>;
}

function DeclaredSchemas({ catalog, envName, service, rpcId }: { catalog: DeclaredCatalog; envName?: string; service: string; rpcId: string }) {
  const svc = catalog.services.find((s) => s.name === service);
  const method = svc?.methods.find((m) => m.id === rpcId);
  if (!svc || !method) return null;
  const blocks: [string, string, string | null][] = [["Request", method.input, schemaText(svc.descriptor, method.input)], ["Response", method.output, schemaText(svc.descriptor, method.output)]];
  return <div className="space-y-2 border-t border-border pt-3">
    <p className="text-xs text-ink-muted">{envName ? `${envName} · ` : ""}{svc.proto_file}{method.idempotency ? ` · ${method.idempotency.toLowerCase().replace(/_/g, " ")}` : ""}</p>
    <div className="grid gap-3 lg:grid-cols-2">{blocks.map(([label, type, body]) => <div key={label} className="rounded-md border border-border/60 bg-surface-sunken p-3">
      <p className="text-sm font-medium text-ink">{label} <span className="font-mono text-xs font-normal text-ink-muted">{type}</span></p>
      {body ? <pre className="mt-2 max-h-72 overflow-auto whitespace-pre-wrap break-all text-xs">{body}</pre> : <p className="mt-1 text-xs text-ink-muted">Schema not included in the catalog.</p>}
    </div>)}</div>
  </div>;
}

/** Notes for the info popover: declared catalogs that failed to parse, by environment name. */
export function catalogNotes(catalog: RPCCatalog | undefined, envNames: ReadonlyMap<string, string>): string[] {
  if (!catalog) return [];
  const notes = catalog.declaredCatalogs.flatMap((a) => {
    const { reason } = parseDeclaredCatalog(a.json);
    return reason ? [`The service catalog for ${envNames.get(a.envId) ?? "one environment"} could not be read (${reason.toLowerCase()}).`] : [];
  });
  if (catalog.declaredStatus !== "available") notes.push(catalog.unavailableReason ? `Request and response schemas: ${catalog.unavailableReason}` : "Request and response schemas are unavailable.");
  return notes;
}
