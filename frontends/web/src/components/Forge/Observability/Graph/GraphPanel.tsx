// Copyright (c) 2026 Reliant Labs
import { useMemo, useState } from "react";
import Button from "@/components/forge-ui/button";
import { AskReliantButton } from "../AskReliant/AskReliant";
import { useObsAskContext } from "../AskReliant/askReliantContext";
import { traceEnvName } from "../AskReliant/askReliantPrompt";
import type { GraphEdge, GraphNode, RPCCatalog, ServiceGraph } from "@/services/controlPlane/observability/graphTypes";
import type { ObsScope } from "@/services/controlPlane/observability/types";
import type { ObsNavigate } from "../obsSearch";
import { graphModel, edgeLabel, type GraphFilters } from "./graphModel";
import { GraphQuality } from "./GraphQuality";
import { ProcessGraph } from "./ProcessGraph";

export function GraphPanel({ graph, catalog, filters, scope, navigate, onRPC }: {
  graph: ServiceGraph; catalog?: RPCCatalog; filters: GraphFilters; scope: ObsScope;
  navigate: ObsNavigate; onRPC: (rpc: string) => void;
}) {
  const model = useMemo(() => graphModel(graph, filters), [graph, filters]);
  const [selection, setSelection] = useState<{ kind: "node" | "edge"; id: string }>();
  const node = selection?.kind === "node" ? model.nodes.find((n) => n.id === selection.id) : undefined;
  const edge = selection?.kind === "edge" ? model.edges.find((e) => e.id === selection.id) : undefined;
  return <div className="space-y-3">
    <GraphQuality quality={graph.quality} window={graph.window} />
    {filters.declared && !graph.edges.some((e) => e.evidence === "declared") && <p role="status" className="text-sm">Declared dependency overlay unavailable in this scoped result. Only verified same-process bindings can appear; remote targets are never inferred from service names.</p>}
    <div className="flex flex-wrap gap-4 text-xs text-ink-muted" aria-label="Graph legend">
      <span>━ Observed</span><span>┄ Declared only</span><span>··· Unknown evidence</span>
      <span>Async: publish / delivery, not requests</span><span>Virtual peers may be unresolved</span>
    </div>
    {(model.omittedNodes > 0 || model.omittedEdges > 0 || model.inputTruncated) && <p role="status" className="text-sm">Display truncated: {model.omittedNodes} nodes / {model.omittedEdges} edges omitted. Narrow the RPC filter or time window.</p>}
    {(model.conflicts > 0 || model.dangling > 0) && <p role="status" className="text-sm">Excluded {model.conflicts} conflicting IDs and {model.dangling} unmatched or out-of-scope endpoints. No inferred connections were added.</p>}
    {model.nodes.length === 0 ? <div className="rounded-lg border border-border bg-surface p-8 text-center" data-testid="graph-empty"><h3 className="font-medium">No graph observations match</h3><p className="mt-2 text-sm text-ink-muted">Try another interval or RPC. No evidence does not establish zero traffic or a broken dependency.</p></div> : <>
      <ProcessGraph nodes={model.nodes} edges={model.edges} selectedId={node?.id} onNode={(n) => setSelection({ kind: "node", id: n.id })} onEdge={(e) => setSelection({ kind: "edge", id: e.id })} />
      <details className="rounded-lg border border-border bg-surface p-3"><summary className="cursor-pointer text-sm font-medium">Accessible node and connection list ({model.nodes.length} / {model.edges.length})</summary>
        <ul className="mt-2 space-y-1">{model.nodes.map((n) => <li key={n.id}><button className="text-sm text-primary underline" onClick={() => setSelection({ kind: "node", id: n.id })}>{n.label || "Unknown identity"} · {n.kind}</button></li>)}</ul>
        <ul className="mt-2 space-y-1">{model.edges.map((e) => <li key={e.id}><button className="text-left text-xs text-primary underline" onClick={() => setSelection({ kind: "edge", id: e.id })}>{graph.nodes.find((n) => n.id === e.source)?.label} → {graph.nodes.find((n) => n.id === e.target)?.label} · {e.rpcId || e.kind} · {edgeLabel(e)}</button></li>)}</ul>
      </details>
    </>}
    {(node || edge) && <section aria-label="Graph detail" className="space-y-3 rounded-lg border border-border bg-surface p-4">
      <div className="flex justify-between gap-3"><h3 className="break-all font-medium">{node?.label || edge?.rpcId || edge?.kind}</h3><Button variant="ghost" size="sm" onClick={() => setSelection(undefined)}>Close detail</Button></div>
      {node && <NodeDetail node={node} graph={graph} catalog={catalog} onRPC={onRPC} />}
      {edge && <EdgeDetail edge={edge} navigate={navigate} onRPC={onRPC} />}
      <AskReference scope={scope} graph={graph} node={node} edge={edge} />
    </section>}
  </div>;
}
function NodeDetail({ node, graph, catalog, onRPC }: { node: GraphNode; graph: ServiceGraph; catalog?: RPCCatalog; onRPC: (rpc: string) => void }) {
  const children = graph.nodes.filter((n) => n.parentId === node.id);
  const methods = catalog?.methods.filter((m) => m.processIds.includes(node.id)) ?? [];
  return <div className="space-y-2 text-sm">
    <p>{node.kind} · environment {node.envId || "unknown"}</p>
    <p className="text-ink-muted">Source: {node.sourceUnavailableReason || "Exact source metadata unavailable"}</p>
    <h4 className="font-medium">Nested RPC / method evidence</h4>
    {!children.length && !methods.length && <p className="text-ink-muted">No nested method metadata supplied for this process.</p>}
    {children.map((n) => <p key={n.id}>{n.kind}: {n.label}{n.rpcId && <button className="ml-2 text-primary underline" onClick={() => onRPC(n.rpcId)}>Filter full RPC</button>}</p>)}
    {methods.map((m) => <p key={m.id}><button className="break-all text-primary underline" onClick={() => onRPC(m.id)}>{m.id}</button> · {m.evidence}</p>)}
    <p className="text-xs text-ink-muted">Registration and composition describe capability, not proof every method was used.</p>
  </div>;
}
function EdgeDetail({ edge, navigate, onRPC }: { edge: GraphEdge; navigate: ObsNavigate; onRPC: (rpc: string) => void }) {
  return <div className="space-y-2 text-sm">
    <p>{edgeLabel(edge)} · {edge.kind} · {edge.cohort || "Unknown cohort"}</p>
    {edge.evidence === "observed" ? <><p>Server faults: {edge.faults.toString()} · user outcomes: {edge.userOutcomes.toString()} · cancelled: {edge.cancelled.toString()} · unknown status: {edge.unknownStatus.toString()}</p>
    <p>Matched: {edge.matched.toString()} · unmatched: {edge.unmatched.toString()}</p>
    {edge.firstSeenMs > 0 && edge.lastSeenMs > 0 && <p className="text-xs text-ink-muted">First / last seen: {new Date(edge.firstSeenMs).toISOString()} / {new Date(edge.lastSeenMs).toISOString()}</p>}
    {edge.duration?.version === 1 && edge.duration.buckets.length === edge.duration.boundsSeconds.length + 1 ? <details><summary className="cursor-pointer text-sm">Received duration histogram (seconds)</summary><p className="text-xs text-ink-muted">{edge.duration.count.toString()} eligible durations · sum {edge.duration.sumSeconds} seconds. Noncumulative buckets; no averaged percentiles.</p><ul className="text-xs">{edge.duration.buckets.map((count, i) => <li key={i}>Upper bound {edge.duration!.boundsSeconds[i] ?? "+Inf"}s: {count.toString()}</li>)}</ul></details> : <p className="text-xs text-ink-muted">Duration histogram unavailable or unsupported. No latency inferred.</p>}</> : <p className="text-ink-muted">Traffic, errors and latency: not applicable to declared/unknown evidence. This is not a recorded call.</p>}
    {edge.rpcId && <button className="text-primary underline" onClick={() => onRPC(edge.rpcId)}>Filter {edge.rpcId}</button>}
    <h4 className="font-medium">Representative traces, not a complete list</h4>
    {edge.exemplars.length === 0 && <p className="text-ink-muted">No trace reference supplied.</p>}
    {edge.exemplars.map((x) => <div key={`${x.traceId}/${x.spanId}`}><button className="break-all text-primary underline" onClick={() => navigate({ view: "traces", trace: x.traceId, span: x.spanId || undefined, q: undefined })}>Open span waterfall · {x.traceId}</button>{x.availabilityUnknown && <p className="text-xs text-ink-muted">Aggregate retained; trace may not have survived sampling.</p>}</div>)}
  </div>;
}
function AskReference({ scope, graph, node, edge }: { scope: ObsScope; graph: ServiceGraph; node?: GraphNode; edge?: GraphEdge }) {
  const askContext = useObsAskContext();
  return <div className="border-t border-border pt-3"><AskReliantButton subject={{
    kind: "graph", env: traceEnvName(scope.envIds, askContext.environments, undefined),
    orgId: scope.orgId, projectId: scope.projectId, envIds: scope.envIds,
    startMs: graph.window?.startMs, endMs: graph.window?.endMs,
    nodeId: node?.id, edgeId: edge?.id, rpcId: edge?.rpcId || node?.rpcId,
    traceIds: edge?.exemplars.map((x) => x.traceId) ?? [],
    origin: graph.quality?.origin || "unknown", truncated: graph.quality?.truncated ?? true,
    provisional: graph.quality?.provisional ?? true,
  }} /><p className="mt-1 text-xs text-ink-muted">References only. Opens an unsent investigation draft on its own workspace.</p></div>;
}
