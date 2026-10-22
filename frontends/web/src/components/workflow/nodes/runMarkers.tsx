// Copyright (c) 2025 Reliant Labs

/**
 * A test run drawn ON the canvas, the way problemMarkers draws validation:
 * without any node component knowing about runs.
 *
 * `withBuilderRun` puts each step's state on its data (running/done/failed use
 * the node's own status styling; loops get their iterations), a class on the
 * React Flow node for the states nodes do not style themselves (skipped,
 * waiting, not run), and the state at the end of its accessible name.
 * `withBuilderRunEdges` marks the edges the run went along. `withRunMarker`
 * wraps a node component so it renders the state's badge.
 */
import type { ComponentType } from "react";
import type { Edge, Node, NodeProps } from "@xyflow/react";

import { cn } from "../../../lib/utils";
import type { NodeExecutionStatus } from "../../../lib/workflow-flow";
import { RUN_STATUS_LABEL, type BuilderRunView, type RunNodeStatus } from "../run/builderRun";

export interface RunMarkerData {
  runBadge?: RunNodeStatus;
}

export const RUN_NODE_CLASS: Partial<Record<RunNodeStatus | "unreached", string>> = {
  skipped: "wf-node--run-skipped",
  waiting: "wf-node--run-waiting",
  unreached: "wf-node--run-unreached",
};

/** Run states the node component styles itself, through its execution status. */
const OWN_STYLING: Partial<Record<RunNodeStatus, NodeExecutionStatus>> = {
  running: "running",
  completed: "completed",
  failed: "failed",
};

/** Steps get a run state; the start node and switches are scenery. */
function isStep(node: Node): boolean {
  return !!(node.data as { step?: unknown } | undefined)?.step;
}

/** The canvas's nodes with a run painted on. Without a run, the same array. */
export function withBuilderRun<T extends Node>(nodes: T[], run: BuilderRunView | null): T[] {
  if (!run) return nodes;
  return nodes.map((node) => {
    const state = run.nodes[node.id];
    if (!state) {
      if (!run.ended || !isStep(node)) return node;
      return {
        ...node,
        className: cn(node.className, RUN_NODE_CLASS.unreached),
        ariaLabel: `${node.ariaLabel ?? node.id}, not run`,
      };
    }
    const loop = state.loop;
    return {
      ...node,
      className: cn(node.className, RUN_NODE_CLASS[state.status]),
      ariaLabel: `${node.ariaLabel ?? node.id}, ${RUN_STATUS_LABEL[state.status]}`,
      data: {
        ...node.data,
        executionStatus: OWN_STYLING[state.status],
        runBadge: state.status === "running" || state.status === "completed" ? undefined : state.status,
        ...(loop && {
          currentIteration: loop.currentIteration,
          completedIterations: loop.completedIterations,
          maxIterations: loop.maxIterations,
          iterationStatuses: loop.iterationStatuses,
        }),
      },
    };
  });
}

/** The edges the run went along, coloured by where they lead. Without a run, the same array. */
export function withBuilderRunEdges<T extends Edge>(edges: T[], run: BuilderRunView | null): T[] {
  if (!run || run.takenEdges.size === 0) return edges;
  return edges.map((edge) => {
    if (!run.takenEdges.has(edge.id)) return edge;
    const target = run.nodes[edge.target]?.status;
    const executionStatus: NodeExecutionStatus =
      target === "failed" ? "failed" : target === "running" || target === "waiting" ? "running" : "completed";
    return { ...edge, data: { ...edge.data, executionStatus } };
  });
}

const BADGE_TEXT: Partial<Record<RunNodeStatus, string>> = {
  failed: "Failed",
  skipped: "Skipped",
  waiting: "Waiting for you",
};

export function RunBadge({ status }: { status?: RunNodeStatus }) {
  const text = status ? BADGE_TEXT[status] : undefined;
  if (!status || !text) return null;
  return (
    <span
      data-testid="node-run-badge"
      data-status={status}
      className={cn(
        "pointer-events-none absolute -left-1 -top-2.5 z-10 rounded-full border px-1.5 py-px text-xs font-semibold shadow",
        status === "failed" && "border-destructive bg-destructive text-destructive-foreground",
        status === "waiting" && "border-warning bg-warning text-black",
        status === "skipped" && "border-border bg-card text-muted-foreground",
      )}
    >
      {text}
    </span>
  );
}

export function withRunMarker<P extends NodeProps>(Inner: ComponentType<P>): ComponentType<P> {
  function NodeWithRunMarker(props: P) {
    return (
      <>
        <Inner {...props} />
        <RunBadge status={(props.data as RunMarkerData | undefined)?.runBadge} />
      </>
    );
  }
  NodeWithRunMarker.displayName = `WithRunMarker(${Inner.displayName || Inner.name || "Node"})`;
  return NodeWithRunMarker;
}

/** Every node type, each able to show its run state. */
export function withRunMarkers<T extends Record<string, ComponentType<any>>>(types: T): T {
  return Object.fromEntries(Object.entries(types).map(([type, component]) => [type, withRunMarker(component)])) as T;
}
