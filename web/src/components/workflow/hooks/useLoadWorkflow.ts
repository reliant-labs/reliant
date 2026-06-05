/**
 * useLoadWorkflow
 *
 * Owns the initial-workflow-load effect: seeding nodes/edges from
 * `initialWorkflow`, swapping the workflow metadata state, kicking off
 * backend validation, clearing undo history, and resetting the inline-edit
 * stack.
 *
 * Post-Wave-4: collapsed from 10 metadata setters down to a single
 * `setWorkflow` setter — the workflow state lives as one object in the
 * builder. Load gating uses `loadedWorkflowName`: it's initialized to
 * `undefined`, so the first effect run mismatches `initialWorkflow?.name`
 * and triggers load. Subsequent re-renders no-op until `initialWorkflow.name`
 * changes (different workflow opened).
 *
 * Dependencies — the hook DOES NOT independently read from the builder
 * context. The caller must pass:
 *   - `initialWorkflow`, `initialName`: load inputs.
 *   - `loadedWorkflowName` + `setLoadedWorkflowName`: name-change tracker
 *     used to gate re-load on prop changes.
 *   - `canDragNodes`: passed through to `workflowToFlowElements` and to the
 *     synthetic workflow-start node when creating a fresh workflow.
 *   - `setWorkflow`: single setter for all top-level metadata.
 *   - `setNodes` / `setEdges`: ReactFlow array setters.
 *   - `currentProjectId`: passed to validation RPC (when truthy).
 *   - `setLoopEditStack`: reset to [] on every load.
 */

import { useEffect } from "react";
import type { Edge, Node } from "@xyflow/react";
import type { Workflow } from "../../../types/workflow";
import { workflowToFlowElements } from "../../../lib/workflow-flow";
import { workflowGrpc, type ValidationError } from "../../../api/workflow-grpc";
import type { ValidationStatus } from "../ValidationStatusBadge";
import type { InlineEditContext } from "./useInlineEditStack";

export interface UseLoadWorkflowArgs {
  initialWorkflow: Workflow | undefined;
  initialName: string | undefined;
  /** `null` sentinel = "not yet loaded"; `string | undefined` = loaded name. */
  loadedWorkflowName: string | null | undefined;
  setLoadedWorkflowName: (name: string | undefined) => void;

  // Canvas setters
  setNodes: (nodes: Node[]) => void;
  setEdges: (edges: Edge[]) => void;

  // Single workflow-state setter (replaces 10 scalar setters)
  setWorkflow: (workflow: Workflow) => void;

  // Builder state
  setIsViewReady: (ready: boolean) => void;
  setHasModifications: (dirty: boolean) => void;
  setValidationStatus: (status: ValidationStatus) => void;
  setValidationErrors: (errors: ValidationError[]) => void;
  setLoopEditStack: (stack: InlineEditContext[]) => void;

  // Misc
  clearHistory: () => void;
  canDragNodes: boolean;
  currentProjectId: string | undefined;
}

export function useLoadWorkflow({
  initialWorkflow,
  initialName,
  loadedWorkflowName,
  setLoadedWorkflowName,
  setNodes,
  setEdges,
  setWorkflow,
  setIsViewReady,
  setHasModifications,
  setValidationStatus,
  setValidationErrors,
  setLoopEditStack,
  clearHistory,
  canDragNodes,
  currentProjectId,
}: UseLoadWorkflowArgs): void {
  // Load initial workflow into nodes and edges.
  // Triggers when `initialWorkflow?.name` differs from `loadedWorkflowName`.
  // `loadedWorkflowName === null` is the "not loaded yet" sentinel — the
  // first effect run always mismatches and loads, including the
  // `initialWorkflow === undefined` (new workflow) case.
  useEffect(() => {
    const incomingName = initialWorkflow?.name;
    if (loadedWorkflowName !== null && incomingName === loadedWorkflowName) {
      return;
    }

    // Hide canvas while loading (will show after fit view is applied)
    setIsViewReady(false);

    let loadedWorkflow: Workflow;
    let loadedNodes: Node[];
    let loadedEdges: Edge[];

    if (initialWorkflow?.nodes) {
      // Convert workflow to ReactFlow elements via shared helper.
      // Handles: 3-tier position chain, workflow start node, synthetic entry edges,
      // switch node generation, and overlap resolution.
      const { nodes: resolvedNodes, edges: resolvedEdges } = workflowToFlowElements(
        initialWorkflow,
        { draggable: canDragNodes },
      );
      loadedNodes = resolvedNodes as Node[];
      loadedEdges = resolvedEdges as Edge[];
      loadedWorkflow = {
        ...initialWorkflow,
        name: initialWorkflow.name || initialName || "New Workflow",
      };
    } else {
      // New workflow - only create workflow entry point node
      const workflowNode: Node = {
        id: "workflow",
        type: "eventNode",
        position: { x: 50, y: 200 },
        data: {
          eventType: "started",
          label: "Workflow Start",
        },
        draggable: canDragNodes,
        deletable: false, // Cannot delete the workflow entry point
      };
      loadedNodes = [workflowNode];
      loadedEdges = [];
      loadedWorkflow = {
        name: initialName || "New Workflow",
      } as Workflow;
    }

    setNodes(loadedNodes);
    setEdges(loadedEdges);
    setWorkflow(loadedWorkflow);

    // Reset dirty flag on load - everything that was just loaded is clean.
    setHasModifications(false);

    setLoadedWorkflowName(incomingName);

    // Reset validation state when loading a new workflow
    setValidationStatus("validating");
    setValidationErrors([]);

    // Validate on open
    if (currentProjectId && initialWorkflow) {
      workflowGrpc
        .validateWorkflow(currentProjectId, initialWorkflow)
        .then((result) => {
          setValidationErrors(result.errors);
          setValidationStatus(result.valid ? "valid" : "invalid");
        })
        .catch((error) => {
          console.error("Failed to validate workflow on load:", error);
          setValidationStatus("unknown");
        });
    }

    // Clear history when loading a new workflow
    clearHistory();

    // Reset loop edit stack when loading a new workflow
    setLoopEditStack([]);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- canDragNodes is derived from isBuiltinWorkflow and isLocked which are stable during workflow load
  }, [
    initialWorkflow,
    initialName,
    loadedWorkflowName,
    setNodes,
    setEdges,
    clearHistory,
  ]);
}
