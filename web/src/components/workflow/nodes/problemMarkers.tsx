/**
 * Validation problems drawn ON the step they are about: a count badge on the
 * node's corner and a ring around it, for every node type, without each node
 * component having to know about validation.
 *
 * `withFindingMarkers` puts the counts on the nodes' data (and a class on the
 * React Flow node); `withProblemMarker` wraps a node component so it renders
 * the badge. The badge is a sibling of the node's own markup, positioned
 * against React Flow's node wrapper, so the node's layout and handles are
 * untouched.
 */
import type { ComponentType } from "react";
import type { Node, NodeProps } from "@xyflow/react";

import type { LocatedFinding } from "../workflowFindings";

export interface ProblemMarkerData {
  problemCount?: number;
  warningCount?: number;
}

export const NODE_PROBLEM_CLASS = "wf-node--has-problems";
export const NODE_WARNING_CLASS = "wf-node--has-warnings";

/** Decorate nodes with their findings. Nodes without any keep their identity. */
export function withFindingMarkers<T extends Node>(nodes: T[], byNode: ReadonlyMap<string, LocatedFinding[]>): T[] {
  if (byNode.size === 0) return nodes;
  return nodes.map((node) => {
    const findings = byNode.get(node.id);
    if (!findings || findings.length === 0) return node;
    const problemCount = findings.filter((f) => !f.warning).length;
    const warningCount = findings.length - problemCount;
    const marker = problemCount > 0 ? NODE_PROBLEM_CLASS : NODE_WARNING_CLASS;
    const summary =
      problemCount > 0
        ? `${problemCount} problem${problemCount === 1 ? "" : "s"}`
        : `${warningCount} warning${warningCount === 1 ? "" : "s"}`;
    return {
      ...node,
      className: [node.className, marker].filter(Boolean).join(" "),
      // Appended to the step's own name (canvas/insertPlacement's
      // withNodeAriaLabels: "Call LLM, call_llm, not connected").
      ariaLabel: `${node.ariaLabel ?? node.id}, ${summary}`,
      data: { ...node.data, problemCount, warningCount },
    };
  });
}

export function ProblemBadge({ problemCount = 0, warningCount = 0 }: ProblemMarkerData) {
  if (problemCount === 0 && warningCount === 0) return null;
  const isProblem = problemCount > 0;
  const count = isProblem ? problemCount : warningCount;
  return (
    <span
      data-testid="node-problem-badge"
      title={`${count} ${isProblem ? "problem" : "warning"}${count === 1 ? "" : "s"} — select the step to see ${count === 1 ? "it" : "them"}`}
      className={
        "pointer-events-none absolute -right-2 -top-2 z-10 flex h-5 min-w-5 items-center justify-center rounded-full px-1 text-2xs font-semibold shadow " +
        (isProblem ? "bg-destructive text-destructive-foreground" : "bg-warning text-black")
      }
    >
      {count}
    </span>
  );
}

export function withProblemMarker<P extends NodeProps>(Inner: ComponentType<P>): ComponentType<P> {
  function NodeWithProblemMarker(props: P) {
    const data = props.data as ProblemMarkerData;
    return (
      <>
        <Inner {...props} />
        <ProblemBadge problemCount={data?.problemCount} warningCount={data?.warningCount} />
      </>
    );
  }
  NodeWithProblemMarker.displayName = `WithProblemMarker(${Inner.displayName || Inner.name || "Node"})`;
  return NodeWithProblemMarker;
}

/** Every node type, each able to show its problems. */
export function withProblemMarkers<T extends Record<string, ComponentType<any>>>(types: T): T {
  return Object.fromEntries(
    Object.entries(types).map(([type, component]) => [type, withProblemMarker(component)]),
  ) as T;
}
