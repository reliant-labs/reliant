/**
 * Pure inverse of `workflowToFlowElements`.
 *
 * Takes ReactFlow nodes/edges + workflow-level metadata and produces a
 * `Workflow` value. Extracted from `WorkflowBuilder.tsx`'s `buildWorkflow`
 * callback so callers can compose it without rebuilding via a `useCallback`
 * tied to the builder component's render cycle.
 *
 * Behavior is intentionally identical to the original `buildWorkflow` — this
 * is a mechanical extraction, not a refactor. If the original had quirks
 * (e.g. always emitting `ui.positions`, omitting `workflowEntry` if visual
 * edges from "workflow" exist), those quirks are preserved here.
 */

import type { Edge, Node } from "@xyflow/react";
import type {
  Workflow,
  WorkflowStep,
  Edge as WorkflowEdge,
  Param,
  SwitchMetadata,
} from "../types/workflow";
import { getSwitchNodeId } from "../types/workflow";
import {
  deriveWorkflowEntryFromEdges,
  sanitizeWorkflowReferences,
} from "../components/workflow/workflowRef";
import { directCel } from "./celAdapter";
import type { FlowNodeData } from "./workflow-flow";

/**
 * Workflow-level metadata that lives outside of nodes/edges in the builder.
 * Passed in by the caller (the builder owns these as separate state today).
 */
export interface NodesEdgesToWorkflowMeta {
  name: string;
  description: string;
  inputs: Record<string, Param>;
  outputs: Record<string, string>;
  entry: string | string[] | undefined;
  tag: string | undefined;
  presetDefault: string | undefined;
  apiVersion: string | undefined;
  isLocked: boolean;
}

/**
 * Convert ReactFlow nodes/edges + metadata back into a `Workflow` value.
 *
 * This is the inverse of `workflowToFlowElements`. It:
 * - extracts steps from non-event, non-switch nodes;
 * - persists positions for all non-switch nodes in `ui.positions`;
 * - persists switch metadata in `ui.switches`, keyed by `getSwitchNodeId`;
 * - rewrites edges going through switch nodes into condition-bearing cases;
 * - emits "started" edges as the `entry` field, not as `from: started`
 *   pseudo-edges.
 */
export function nodesEdgesToWorkflow(
  nodes: Node[],
  edges: Edge[],
  meta: NodesEdgesToWorkflowMeta,
): Workflow {
  // Extract steps from nodes (excluding event nodes and switch nodes - they're UI-only)
  const steps = nodes
    .filter((node) => node.type !== "eventNode" && node.type !== "switchNode")
    .map((node) => {
      const step = node.data.step as WorkflowStep;
      // Remove triggers and position from steps (deprecated, now using edges and ui.positions)
      const {
        triggers: _triggers,
        position: _position,
        ...cleanStep
      } = step as any;
      return cleanStep;
    });

  // Build UI metadata - include positions for all nodes (excluding switch nodes, handled separately)
  const positions: Record<string, { x: number; y: number }> = {};
  nodes.forEach((node) => {
    if (node.position && node.type !== "switchNode") {
      positions[node.id] = node.position;
    }
  });

  // Build set of valid node IDs (including event node IDs like 'workflow')
  const validNodeIds = new Set(nodes.map((n) => n.id));

  // Build a map of switch nodes for resolving edges through switches
  const switchNodes = new Map<
    string,
    {
      cases: Array<{ id: string; condition: string; label?: string }>;
      position: { x: number; y: number };
    }
  >();
  for (const node of nodes) {
    if (node.type === "switchNode") {
      const nodeData = node.data as FlowNodeData;
      switchNodes.set(node.id, {
        cases: (nodeData.cases || []) as Array<{
          id: string;
          condition: string;
          label?: string;
        }>,
        position: node.position,
      });
    }
  }

  // Find edges going INTO switch nodes (to know the source of the switch)
  const switchInputs = new Map<string, string>(); // switchNodeId -> sourceNodeId
  for (const edge of edges) {
    if (switchNodes.has(edge.target)) {
      switchInputs.set(edge.target, edge.source);
    }
  }

  const sourceBySwitchId = new Map<string, string>();
  for (const edge of edges) {
    if (!switchNodes.has(edge.target)) {
      continue;
    }
    const sourceEvent = (edge.data as { sourceEvent?: string } | undefined)
      ?.sourceEvent;
    const source =
      edge.source === "workflow"
        ? "started"
        : sourceEvent && sourceEvent !== "completed"
          ? `${edge.source}.${sourceEvent}`
          : edge.source;
    sourceBySwitchId.set(edge.target, source);
  }

  // Build switch metadata for UI persistence
  const switches: Record<string, SwitchMetadata> = {};
  for (const [switchId, switchData] of switchNodes) {
    const sourceNode = switchInputs.get(switchId);
    const sourceFrom = sourceBySwitchId.get(switchId);
    if (sourceNode && sourceFrom) {
      const persistedSwitchId = getSwitchNodeId(sourceFrom);
      switches[persistedSwitchId] = {
        sourceNode,
        position: switchData.position,
        cases: switchData.cases.map((c) => ({
          id: c.id,
          condition: c.condition ? directCel(c.condition) : undefined,
          label: c.label ?? "",
        })),
      };
    }
  }

  // Convert React Flow edges to workflow edges
  // Group by source, handling switch nodes specially
  const edgesBySource = new Map<
    string,
    Array<{
      target: string;
      condition?: string;
      label?: string;
      sourceEvent?: string;
    }>
  >();

  for (const edge of edges) {
    const edgeData = (edge.data || {}) as {
      sourceEvent?: string;
      label?: string;
    };
    const { sourceEvent, label } = edgeData;

    // Skip edges going TO switch nodes (handled below)
    if (switchNodes.has(edge.target)) {
      continue;
    }

    // Filter out orphaned edges
    if (!validNodeIds.has(edge.source) || !validNodeIds.has(edge.target)) {
      continue;
    }

    let from: string;
    let condition: string | undefined;
    let edgeLabel: string | undefined = label;

    // Check if this edge is coming FROM a switch node
    if (switchNodes.has(edge.source)) {
      const switchNode = switchNodes.get(edge.source)!;
      const switchSource = sourceBySwitchId.get(edge.source);

      if (!switchSource) {
        console.warn(`Switch node ${edge.source} has no input edge`);
        continue;
      }

      // Find which case this edge is for (by sourceHandle)
      const caseId = edge.sourceHandle;
      const caseIndex = switchNode.cases.findIndex((c) => c.id === caseId);
      const caseData = caseIndex >= 0 ? switchNode.cases[caseIndex] : null;

      // Use the switch's input source as the "from"
      // Preserves event-scoped sources like "node.failed".
      from = switchSource;

      // Get condition from the case
      if (caseData) {
        condition = caseData.condition || undefined;
        edgeLabel = caseData.label || edgeLabel;
      }
    } else {
      // Regular edge (not from a switch)
      // Format: "source-id" or "source-id.event-name"
      if (edge.source === "workflow") {
        // Workflow start node always maps to "started" in the definition
        from = "started";
      } else if (sourceEvent && sourceEvent !== "completed") {
        from = `${edge.source}.${sourceEvent}`;
      } else {
        from = edge.source;
      }
    }

    if (!edgesBySource.has(from)) {
      edgesBySource.set(from, []);
    }
    edgesBySource.get(from)!.push({
      target: edge.target,
      condition,
      label: edgeLabel,
      sourceEvent,
    });
  }

  // Convert grouped edges to Edge format with cases
  // Separate out "started" edges to use entry field instead
  const workflowEdges: WorkflowEdge[] = [];
  const entryTargets: string[] = [];

  for (const [from, cases] of edgesBySource) {
    if (from === "started") {
      // Extract entry targets instead of creating "from: started" edges
      for (const c of cases) {
        entryTargets.push(c.target);
      }
    } else {
      // Separate conditional cases from default targets
      const conditionalCases = cases.filter((c) => c.condition);
      const defaultCases = cases.filter((c) => !c.condition);

      // Create separate edges for each default target (don't merge into arrays)
      for (const c of defaultCases) {
        workflowEdges.push({ from, default: [c.target] });
      }

      // Create edge for conditional cases if any
      if (conditionalCases.length > 0) {
        workflowEdges.push({
          from,
          cases: conditionalCases.map((c) => ({
            to: [c.target],
            condition: c.condition!,
            label: c.label,
          })),
        });
      }
    }
  }

  // Compute entry field from visual edges. Connections from the workflow start node
  // should take precedence over any previously edited workflowEntry state.
  let computedEntry = deriveWorkflowEntryFromEdges(meta.entry, edges);
  if (!computedEntry && entryTargets.length > 0) {
    computedEntry = entryTargets;
  }

  const { entry: sanitizedEntry, outputs: sanitizedOutputs } =
    sanitizeWorkflowReferences(
      computedEntry,
      meta.outputs,
      steps.map((step) => step.id),
    );

  return {
    name: meta.name,
    description: meta.description || undefined,
    presets:
      meta.tag || meta.presetDefault
        ? {
            tag: meta.tag,
            default: meta.presetDefault,
          }
        : undefined,
    // Note: workflow-level thread config removed (not in proto)
    apiVersion: meta.apiVersion || undefined,
    nodes: steps,
    edges: workflowEdges.length > 0 ? workflowEdges : undefined, // Only include if non-empty
    entry: sanitizedEntry, // Use entry field instead of "from: started" edges
    inputs:
      Object.keys(meta.inputs).length > 0
        ? (meta.inputs as Workflow["inputs"])
        : undefined,

    outputs: sanitizedOutputs,
    ui: {
      positions,
      ...(Object.keys(switches).length > 0 && { switches }),
      ...(meta.isLocked && { locked: true }),
    },
  };
}
