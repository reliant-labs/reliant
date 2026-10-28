// Copyright (c) 2026 Reliant Labs
import { useMemo, useRef, useState } from "react";
import { ReactFlow, Background, Controls, Handle, Position, MarkerType, type NodeProps, type Node, type Edge } from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { cn } from "@/lib/utils";
import type { GraphNode } from "@/services/controlPlane/observability/graphTypes";
import { unitLabel, type GraphLink, type NodeStats, type PositionedNode } from "./graphModel";
import { errorFraction, fmtCount, fmtErrorRate, fmtLatency, histogramQuantile, shortService } from "./graphStats";

const KIND_LABEL: Record<GraphNode["kind"], string> = {
  frontend: "frontend", process: "service", api: "api", service: "service", rpc: "rpc", method: "method",
  database: "database", external: "external", broker: "queue", workflow: "workflow", unknown: "unknown",
};
// Runtime SVG/marker colours are the styling-contract exception; tokens only.
const STROKE = "hsl(var(--muted-foreground))";
const STROKE_ERROR = "hsl(var(--destructive))";
const STROKE_ACTIVE = "hsl(var(--primary))";

type ProcessFlowNode = Node<{ node: PositionedNode; stats?: NodeStats; envLabel?: string; selected: boolean; dimmed: boolean }, "process">;
function ProcessNode({ data }: NodeProps<ProcessFlowNode>) {
  const { node, stats, envLabel } = data;
  const p95 = histogramQuantile(stats?.duration, 0.95);
  const errors = stats ? errorFraction(stats.faults, stats.requests) : 0;
  return <div className={cn("w-56 rounded-lg border bg-surface px-3 py-2 text-ink shadow-sm transition-opacity",
    data.selected ? "border-primary ring-2 ring-ring" : errors >= 0.05 ? "border-danger-border" : "border-border",
    data.dimmed && "opacity-40")}>
    <Handle type="target" position={Position.Left} className="!border-0 !bg-transparent" />
    <div className="flex items-center gap-2">
      <span className="min-w-0 flex-1 truncate text-sm font-medium" title={node.label}>{node.label || "Unknown"}</span>
      <span className="shrink-0 rounded px-1.5 py-px text-[0.6875rem] leading-4 text-ink-muted ring-1 ring-border">{KIND_LABEL[node.kind]}</span>
    </div>
    {envLabel && <p className="truncate text-xs text-ink-muted">{envLabel}</p>}
    {stats && stats.requests > 0n && <p className="mt-0.5 text-xs tabular-nums text-ink-muted">
      {fmtCount(stats.requests)} req
      {stats.faults > 0n && <> · <span className="text-danger-ink">{fmtErrorRate(stats.faults, stats.requests)} err</span></>}
      {p95 !== undefined && <> · p95 {fmtLatency(p95)}</>}
    </p>}
    <Handle type="source" position={Position.Right} className="!border-0 !bg-transparent" />
  </div>;
}
const nodeTypes = { process: ProcessNode };

interface Hover { link: GraphLink; x: number; y: number }

/** Renderer only. Query/tenant authority and aggregation never live here. */
export function ProcessGraph({ nodes, links, stats, envLabel, selectedId, onNode, onLink }: {
  nodes: PositionedNode[]; links: GraphLink[]; stats: Map<string, NodeStats>;
  envLabel: (node: GraphNode) => string | undefined; selectedId?: string;
  onNode: (node: GraphNode) => void; onLink: (link: GraphLink) => void;
}) {
  const container = useRef<HTMLDivElement>(null);
  const [hover, setHover] = useState<Hover>();
  const [hoverNode, setHoverNode] = useState<string>();
  const focus = hoverNode ?? selectedId;
  const neighbours = useMemo(() => {
    if (!focus) return undefined;
    const set = new Set([focus]);
    for (const l of links) if (l.source === focus || l.target === focus) { set.add(l.source); set.add(l.target); }
    return set;
  }, [focus, links]);
  const byId = useMemo(() => new Map(nodes.map((n) => [n.id, n])), [nodes]);
  const flowNodes = useMemo<ProcessFlowNode[]>(() => nodes.map((n) => ({ id: n.id, type: "process", position: n.position,
    data: { node: n, stats: stats.get(n.id), envLabel: envLabel(n), selected: selectedId === n.id, dimmed: !!neighbours && !neighbours.has(n.id) },
    draggable: false })), [nodes, stats, envLabel, selectedId, neighbours]);
  const max = useMemo(() => links.reduce((m, l) => (l.count > m ? l.count : m), 1n), [links]);
  const flowEdges = useMemo<Edge[]>(() => links.map((l) => {
    const active = !!focus && (l.source === focus || l.target === focus);
    const color = active ? STROKE_ACTIVE : l.faults > 0n ? STROKE_ERROR : STROKE;
    const width = l.evidence === "observed" ? 1.25 + 4.75 * Math.sqrt(Number(l.count) / Number(max)) : 1.25;
    return { id: l.id, source: l.source, target: l.target, interactionWidth: 18,
      ariaLabel: `${byId.get(l.source)?.label} to ${byId.get(l.target)?.label}: ${fmtCount(l.count)} ${unitLabel(l.unit)}`,
      style: { stroke: color, strokeWidth: width, opacity: focus && !active ? 0.25 : 0.85,
        strokeDasharray: l.evidence === "declared" ? "7 5" : l.evidence === "unknown" ? "2 5" : undefined },
      markerEnd: { type: MarkerType.ArrowClosed, color, width: 14, height: 14 },
    };
  }), [links, max, focus, byId]);
  const linkById = useMemo(() => new Map(links.map((l) => [l.id, l])), [links]);
  const point = (event: React.MouseEvent) => {
    const box = container.current?.getBoundingClientRect();
    return { x: event.clientX - (box?.left ?? 0), y: event.clientY - (box?.top ?? 0) };
  };
  // Remount (and so re-fit) whenever the node set changes, not on hover.
  const signature = useMemo(() => nodes.map((n) => n.id).join("|"), [nodes]);
  return <div ref={container} className="relative h-[560px] overflow-hidden rounded-lg border border-border bg-surface-sunken" aria-label="Service dependency graph" data-testid="process-graph">
    <ReactFlow key={signature} nodes={flowNodes} edges={flowEdges} nodeTypes={nodeTypes}
      fitView fitViewOptions={{ padding: 0.15, maxZoom: 1 }} minZoom={0.15} maxZoom={1.6}
      nodesConnectable={false} nodesDraggable={false} elementsSelectable={false}
      onNodeClick={(_, n) => onNode(n.data.node)}
      onNodeMouseEnter={(_, n) => setHoverNode(n.id)} onNodeMouseLeave={() => setHoverNode(undefined)}
      onEdgeClick={(_, e) => { const link = linkById.get(e.id); if (link) onLink(link); }}
      onEdgeMouseEnter={(event, e) => { const link = linkById.get(e.id); if (link) setHover({ link, ...point(event) }); }}
      onEdgeMouseMove={(event, e) => { const link = linkById.get(e.id); if (link) setHover({ link, ...point(event) }); }}
      onEdgeMouseLeave={() => setHover(undefined)}
      proOptions={{ hideAttribution: false }}>
      <Background gap={24} size={1} /><Controls showInteractive={false} />
    </ReactFlow>
    {hover && <LinkTooltip hover={hover} from={byId.get(hover.link.source)} to={byId.get(hover.link.target)} />}
  </div>;
}

function LinkTooltip({ hover: { link, x, y }, from, to }: { hover: Hover; from?: GraphNode; to?: GraphNode }) {
  const p95 = histogramQuantile(link.duration, 0.95);
  const top = [...link.edges].filter((e) => e.rpcId).sort((a, b) => (b.count > a.count ? 1 : b.count < a.count ? -1 : 0)).slice(0, 3);
  return <div role="tooltip" data-testid="graph-link-tooltip" style={{ left: x + 14, top: y + 14 }}
    className="pointer-events-none absolute z-10 w-72 rounded-md border border-border bg-surface p-3 text-xs text-ink shadow-lg">
    <p className="truncate font-medium">{from?.label ?? "?"} → {to?.label ?? "?"}</p>
    {link.evidence === "observed" ? <p className="mt-1 tabular-nums text-ink-muted">
      {fmtCount(link.count)} {unitLabel(link.unit)}
      {link.faults > 0n && <> · <span className="text-danger-ink">{fmtErrorRate(link.faults, link.count)} errors</span></>}
      {p95 !== undefined && <> · p95 {fmtLatency(p95)}</>}
    </p> : <p className="mt-1 text-ink-muted">Declared dependency, no traffic observed</p>}
    {top.length > 0 && <ul className="mt-2 space-y-0.5">{top.map((e) => {
      const [svc, method] = e.rpcId.split("/");
      return <li key={e.id} className="flex justify-between gap-2 tabular-nums"><span className="truncate">{shortService(svc)}/{method}</span><span className="text-ink-muted">{fmtCount(e.count)}</span></li>;
    })}</ul>}
    {link.edges.filter((e) => e.rpcId).length > 3 && <p className="mt-1 text-ink-muted">+{link.edges.filter((e) => e.rpcId).length - 3} more methods · click for details</p>}
  </div>;
}
