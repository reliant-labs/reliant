/**
 * Topological node ordering for the mobile step-list view.
 *
 * `lib/workflow-layout.ts` computes the same BFS layering as part of a much
 * heavier x/y auto-layout pass built for a 2D canvas — pulling it in here
 * would drag in positioning logic no list view needs. This extracts just the
 * layer-assignment half: given a workflow's `entry` field and edges, produce
 * a stable read order (breadth-first from the start node, ties broken by
 * declaration order) that mirrors what the desktop graph would show
 * left-to-right, top-to-bottom.
 */

import type { Workflow } from "../../types/workflow";

function toTargetArray(target: string[] | string | undefined): string[] {
  if (!target) return [];
  return Array.isArray(target) ? target : [target];
}

/**
 * Node IDs from `workflow.nodes`, ordered by BFS distance from the entry
 * point(s). Nodes unreachable from `entry` (or when there is no entry/edges
 * at all) fall back to declaration order, appended after every reachable
 * node.
 */
export function orderedWorkflowNodeIds(workflow: Workflow): string[] {
  const nodes = workflow.nodes ?? [];
  const declarationOrder = nodes
    .map((n) => n.id)
    .filter((id): id is string => !!id);
  const idSet = new Set(declarationOrder);

  const adjacency = new Map<string, string[]>();
  const addEdge = (from: string, to: string) => {
    if (!idSet.has(to)) return;
    const list = adjacency.get(from);
    if (list) list.push(to);
    else adjacency.set(from, [to]);
  };

  const entryTargets = toTargetArray(workflow.entry as string[] | string | undefined);
  for (const target of entryTargets) addEdge("__entry__", target);

  for (const edge of workflow.edges ?? []) {
    if (!edge.from) continue;
    const sourceId = edge.from.split(".")[0];
    const targets = [
      ...(edge.cases ?? []).flatMap((c) => toTargetArray(c.to)),
      ...toTargetArray(edge.default),
    ];
    for (const target of targets) addEdge(sourceId, target);
  }

  const visited = new Set<string>();
  const ordered: string[] = [];
  const queue: string[] = [...(adjacency.get("__entry__") ?? [])];

  while (queue.length > 0) {
    const current = queue.shift()!;
    if (visited.has(current)) continue;
    visited.add(current);
    ordered.push(current);
    for (const next of adjacency.get(current) ?? []) {
      if (!visited.has(next)) queue.push(next);
    }
  }

  // Nodes never reached from entry (disconnected, or no entry declared at
  // all) still need to appear — append them in declaration order.
  for (const id of declarationOrder) {
    if (!visited.has(id)) ordered.push(id);
  }

  return ordered;
}
