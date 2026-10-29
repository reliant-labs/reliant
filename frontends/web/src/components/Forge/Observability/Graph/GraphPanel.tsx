// Copyright (c) 2026 Reliant Labs
import { useCallback, useMemo, useState } from "react";
import { X } from "lucide-react";
import Button from "@/components/forge-ui/button";
import { AskReliantButton } from "../AskReliant/AskReliant";
import { useObsAskContext } from "../AskReliant/askReliantContext";
import { traceEnvName } from "../AskReliant/askReliantPrompt";
import type { GraphEdge, GraphNode, ServiceGraph } from "@/services/controlPlane/observability/graphTypes";
import type { ObsScope } from "@/services/controlPlane/observability/types";
import type { ObsNavigate } from "../obsSearch";
import { authoritativeCohort, graphModel, unitLabel, type GraphFilters, type GraphLink, type NodeStats } from "./graphModel";
import { fmtCount, fmtErrorRate, fmtLatency, histogramQuantile, shortService, sumHistograms } from "./graphStats";
import { ProcessGraph } from "./ProcessGraph";

export function GraphPanel({ graph, filters, scope, navigate, onRPC, envNames, onShowUnresolved }: {
  graph: ServiceGraph; filters: GraphFilters; scope: ObsScope;
  navigate: ObsNavigate; onRPC: (rpc: string) => void; envNames: ReadonlyMap<string, string>;
  onShowUnresolved?: () => void;
}) {
  const model = useMemo(() => graphModel(graph, filters), [graph, filters]);
  const [selection, setSelection] = useState<{ kind: "node" | "link"; id: string }>();
  const node = selection?.kind === "node" ? model.nodes.find((n) => n.id === selection.id) : undefined;
  const link = selection?.kind === "link" ? model.links.find((l) => l.id === selection.id) : undefined;
  const label = useCallback((id: string) => graph.nodes.find((n) => n.id === id)?.label ?? "Unknown", [graph.nodes]);
  // A node's environment is named only where it disambiguates: the same
  // service drawn once per environment. Never the environment's id.
  const envLabel = useMemo(() => {
    const envsByLabel = new Map<string, Set<string>>();
    for (const n of model.nodes) envsByLabel.set(n.label, (envsByLabel.get(n.label) ?? new Set()).add(n.envId));
    return (n: GraphNode) => (envsByLabel.get(n.label)?.size ?? 0) > 1 ? envNames.get(n.envId) ?? "other environment" : undefined;
  }, [model.nodes, envNames]);
  if (model.nodes.length === 0) {
    return <div className="rounded-lg border border-border bg-surface p-10 text-center" data-testid="graph-empty">
      <h3 className="font-medium text-ink">No traffic in this window</h3>
      <p className="mt-1 text-sm text-ink-muted">{filters.rpcId || filters.errorsOnly ? "Nothing matches these filters. Clear them or widen the time range." : "Widen the time range, or check that your services export traces."}</p>
    </div>;
  }
  return <div className="space-y-3">
    <ProcessGraph nodes={model.nodes} links={model.links} stats={model.stats} envLabel={envLabel} selectedId={node?.id}
      onNode={(n) => setSelection({ kind: "node", id: n.id })} onLink={(l) => setSelection({ kind: "link", id: l.id })} />
    <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-ink-muted" aria-label="Graph legend">
      <span className="inline-flex items-center gap-1.5"><svg aria-hidden width="22" height="8"><line x1="0" y1="4" x2="22" y2="4" stroke="currentColor" strokeWidth="1.5" /></svg>Line width = traffic</span>
      <span className="inline-flex items-center gap-1.5 text-danger-ink"><svg aria-hidden width="22" height="8"><line x1="0" y1="4" x2="22" y2="4" stroke="currentColor" strokeWidth="2" /></svg>Errors</span>
      {filters.declared && <span className="inline-flex items-center gap-1.5"><svg aria-hidden width="22" height="8"><line x1="0" y1="4" x2="22" y2="4" stroke="currentColor" strokeWidth="1.5" strokeDasharray="5 3" /></svg>Declared, not observed</span>}
      <span>Hover a connection for details; click to pin.</span>
      {model.hiddenUnresolved > 0 && onShowUnresolved && <span>{model.hiddenUnresolved} unresolved caller/peer node{model.hiddenUnresolved === 1 ? "" : "s"} hidden · <button type="button" className="text-primary hover:underline" onClick={onShowUnresolved}>Show</button></span>}
      {(model.omittedNodes > 0 || model.omittedEdges > 0) && <span role="status">{model.omittedNodes + model.omittedEdges} smaller connections not drawn; filter by service to see them.</span>}
    </div>
    {(node || link) && <section aria-label="Graph detail" className="rounded-lg border border-border bg-surface p-4">
      <div className="mb-3 flex items-start justify-between gap-3">
        <h3 className="min-w-0 break-words font-medium text-ink">{node ? node.label : `${label(link!.source)} → ${label(link!.target)}`}</h3>
        <Button variant="ghost" size="sm" icon={<X />} aria-label="Close detail" onClick={() => setSelection(undefined)} />
      </div>
      {node && <NodeDetail node={node} stats={model.stats.get(node.id)} edges={model.edges} label={label} envName={envNames.get(node.envId)} onRPC={onRPC} />}
      {link && <LinkDetail link={link} navigate={navigate} onRPC={onRPC} />}
      <AskReference scope={scope} graph={graph} node={node} link={link} />
    </section>}
    <details className="text-sm">
      <summary className="cursor-pointer text-xs text-ink-muted">Connections as a list ({model.links.length})</summary>
      <ul className="mt-2 space-y-1">{model.links.map((l) => <li key={l.id}><button className="text-left text-xs text-primary hover:underline" onClick={() => setSelection({ kind: "link", id: l.id })}>
        {label(l.source)} → {label(l.target)} · {fmtCount(l.count)} {unitLabel(l.unit)}{l.faults > 0n ? ` · ${fmtErrorRate(l.faults, l.count)} errors` : ""}
      </button></li>)}</ul>
    </details>
  </div>;
}

function Stat({ label, value, danger }: { label: string; value: string; danger?: boolean }) {
  return <div><dt className="text-xs text-ink-muted">{label}</dt><dd className={danger ? "tabular-nums text-danger-ink" : "tabular-nums text-ink"}>{value}</dd></div>;
}

interface MethodRow { rpcId: string; count: bigint; faults: bigint; p95?: number }
function byMethod(edges: GraphEdge[]): MethodRow[] {
  const rows = new Map<string, GraphEdge[]>();
  for (const e of edges) if (e.rpcId && e.evidence === "observed") rows.set(e.rpcId, [...(rows.get(e.rpcId) ?? []), e]);
  // One viewpoint per method, as for a link: a client attempt and the server
  // request it became are the same call.
  return [...rows].map(([rpcId, all]) => { const es = authoritativeCohort(all).counted; return { rpcId,
    count: es.reduce((s, e) => s + e.count, 0n), faults: es.reduce((s, e) => s + e.faults, 0n),
    p95: histogramQuantile(sumHistograms(es.map((e) => e.duration)), 0.95) }; })
    .sort((a, b) => (b.count > a.count ? 1 : b.count < a.count ? -1 : a.rpcId.localeCompare(b.rpcId)));
}

function MethodTable({ rows, onRPC }: { rows: MethodRow[]; onRPC: (rpc: string) => void }) {
  if (!rows.length) return null;
  return <table className="w-full text-left text-sm">
    <thead className="text-xs text-ink-muted"><tr><th className="py-1 font-normal">Method</th><th className="py-1 text-right font-normal">Requests</th><th className="py-1 text-right font-normal">Errors</th><th className="py-1 text-right font-normal">p95</th></tr></thead>
    <tbody>{rows.slice(0, 25).map((r) => { const [svc, m] = r.rpcId.split("/"); return <tr key={r.rpcId} className="border-t border-border/60">
      <td className="py-1"><button className="text-left text-primary hover:underline" title={r.rpcId} onClick={() => onRPC(r.rpcId)}>{shortService(svc)}/{m}</button></td>
      <td className="py-1 text-right tabular-nums">{fmtCount(r.count)}</td>
      <td className={r.faults > 0n ? "py-1 text-right tabular-nums text-danger-ink" : "py-1 text-right tabular-nums"}>{fmtErrorRate(r.faults, r.count)}</td>
      <td className="py-1 text-right tabular-nums">{fmtLatency(r.p95)}</td>
    </tr>; })}</tbody>
  </table>;
}

function NodeDetail({ node, stats, edges, label, envName, onRPC }: { node: GraphNode; stats?: NodeStats; edges: GraphEdge[]; label: (id: string) => string; envName?: string; onRPC: (rpc: string) => void }) {
  const served = byMethod(edges.filter((e) => e.target === node.id && e.unit === "server_request"));
  const calls = new Map<string, bigint>();
  for (const e of edges) if (e.source === node.id && e.evidence === "observed") calls.set(e.target, (calls.get(e.target) ?? 0n) + e.count);
  return <div className="space-y-4 text-sm">
    <dl className="flex flex-wrap gap-x-8 gap-y-2">
      {envName && <Stat label="Environment" value={envName} />}
      <Stat label="Requests" value={stats ? fmtCount(stats.requests) : "—"} />
      <Stat label="Error rate" value={stats ? fmtErrorRate(stats.faults, stats.requests) : "—"} danger={!!stats && stats.faults > 0n} />
      <Stat label="p50" value={fmtLatency(histogramQuantile(stats?.duration, 0.5))} />
      <Stat label="p95" value={fmtLatency(histogramQuantile(stats?.duration, 0.95))} />
    </dl>
    {served.length > 0 && <div><h4 className="mb-1 text-xs font-medium text-ink-muted">Methods served</h4><MethodTable rows={served} onRPC={onRPC} /></div>}
    {calls.size > 0 && <div><h4 className="mb-1 text-xs font-medium text-ink-muted">Calls</h4>
      <p className="text-ink">{[...calls].sort((a, b) => (b[1] > a[1] ? 1 : -1)).map(([id, n]) => `${label(id)} (${fmtCount(n)})`).join(" · ")}</p></div>}
  </div>;
}

function LinkDetail({ link, navigate, onRPC }: { link: GraphLink; navigate: ObsNavigate; onRPC: (rpc: string) => void }) {
  const exemplars = link.edges.flatMap((e) => e.exemplars).slice(0, 3);
  return <div className="space-y-4 text-sm">
    {link.evidence === "observed" ? <dl className="flex flex-wrap gap-x-8 gap-y-2">
      <Stat label={unitLabel(link.unit).replace(/^./, (c) => c.toUpperCase())} value={fmtCount(link.count)} />
      <Stat label="Error rate" value={fmtErrorRate(link.faults, link.count)} danger={link.faults > 0n} />
      <Stat label="p50" value={fmtLatency(histogramQuantile(link.duration, 0.5))} />
      <Stat label="p95" value={fmtLatency(histogramQuantile(link.duration, 0.95))} />
    </dl> : <p className="text-ink-muted">Declared dependency; no traffic observed in this window.</p>}
    <MethodTable rows={byMethod(link.edges)} onRPC={onRPC} />
    {exemplars.length > 0 && <div className="flex flex-wrap items-center gap-2">
      <span className="text-xs text-ink-muted">Example traces</span>
      {exemplars.map((x, i) => <Button key={`${x.traceId}/${x.spanId}`} variant="outline" size="sm"
        onClick={() => navigate({ view: "traces", trace: x.traceId, span: x.spanId || undefined, q: undefined })}>Trace {i + 1}</Button>)}
    </div>}
  </div>;
}

function AskReference({ scope, graph, node, link }: { scope: ObsScope; graph: ServiceGraph; node?: GraphNode; link?: GraphLink }) {
  const askContext = useObsAskContext();
  const top = link?.edges.find((e) => e.rpcId);
  return <div className="mt-4 border-t border-border pt-3"><AskReliantButton subject={{
    kind: "graph", env: traceEnvName(scope.envIds, askContext.environments, undefined),
    orgId: scope.orgId, projectId: scope.projectId, envIds: scope.envIds,
    startMs: graph.window?.startMs, endMs: graph.window?.endMs,
    nodeId: node?.id, edgeId: top?.id, rpcId: top?.rpcId || node?.rpcId,
    traceIds: link?.edges.flatMap((e) => e.exemplars.map((x) => x.traceId)).slice(0, 10) ?? [],
    origin: graph.quality?.origin || "unknown", truncated: graph.quality?.truncated ?? true,
    provisional: graph.quality?.provisional ?? true,
  }} /></div>;
}
