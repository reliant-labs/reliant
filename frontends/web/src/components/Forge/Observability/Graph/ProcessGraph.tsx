// Copyright (c) 2026 Reliant Labs
import { useMemo } from "react";
import { ReactFlow, Background, Controls, Handle, Position, MarkerType, type NodeProps, type Node, type Edge } from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { cn } from "@/lib/utils";
import type { GraphNode, GraphEdge } from "@/services/controlPlane/observability/graphTypes";
import { edgeLabel, type PositionedNode } from "./graphModel";

const nodeTypes = { process: ProcessNode };
type ProcessFlowNode = Node<{ node: GraphNode; selected: boolean }, "process">;
function ProcessNode({ data }: NodeProps<ProcessFlowNode>) {
  return <div className={cn("w-52 rounded-lg border bg-surface px-3 py-2 text-ink shadow-sm", data.selected ? "border-primary ring-2 ring-ring" : "border-border")}>
    <Handle type="target" position={Position.Left} />
    <p className="text-xs uppercase tracking-wide text-ink-muted">{data.node.kind} · {data.node.envId || "unknown environment"}</p>
    <p className="truncate text-sm font-medium" title={data.node.label}>{data.node.label || "Unknown identity"}</p>
    <Handle type="source" position={Position.Right} />
  </div>;
}

/** Renderer only. Query/tenant authority and aggregation never live here. */
export function ProcessGraph({ nodes, edges, selectedId, onNode, onEdge }: {
  nodes: PositionedNode[]; edges: GraphEdge[]; selectedId?: string;
  onNode: (node: GraphNode) => void; onEdge: (edge: GraphEdge) => void;
}) {
  const flowNodes = useMemo<ProcessFlowNode[]>(() => nodes.map((n) => ({ id: n.id, type: "process", position: n.position,
    data: { node: n, selected: selectedId === n.id }, draggable: false })), [nodes, selectedId]);
  const flowEdges = useMemo<Edge[]>(() => edges.map((e) => ({ id: e.id, source: e.source, target: e.target,
    label: e.rpcId ? e.rpcId.split("/").at(-1) : e.kind, ariaLabel: `${e.rpcId || e.kind}: ${edgeLabel(e)}`,
    // Runtime SVG geometry/colors are the styling-contract exception. No brand literals.
    style: { stroke: e.evidence === "observed" && e.faults > 0n ? "hsl(var(--destructive))" : "hsl(var(--foreground))",
      strokeDasharray: e.evidence === "declared" ? "7 5" : e.evidence === "unknown" ? "2 5" : undefined },
    labelStyle: { fill: "hsl(var(--foreground))", fontSize: 11 },
    labelBgStyle: { fill: "hsl(var(--card))" },
    markerEnd: { type: MarkerType.ArrowClosed },
  })), [edges]);
  return <div className="h-[460px] overflow-hidden rounded-lg border border-border bg-surface-sunken" aria-label="Process dependency graph" data-testid="process-graph">
    <ReactFlow nodes={flowNodes} edges={flowEdges} nodeTypes={nodeTypes} fitView minZoom={0.2} maxZoom={1.6}
      nodesConnectable={false} nodesDraggable={false} elementsSelectable={false}
      onNodeClick={(_, n) => onNode(n.data.node)}
      onEdgeClick={(_, e) => { const edge = edges.find((candidate) => candidate.id === e.id); if (edge) onEdge(edge); }}
      proOptions={{ hideAttribution: false }}>
      <Background /><Controls showInteractive={false} />
    </ReactFlow>
  </div>;
}
