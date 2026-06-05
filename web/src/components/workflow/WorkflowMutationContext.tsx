/**
 * WorkflowMutationContext
 * -----------------------
 * Centralizes workflow-graph mutation primitives so config panels can call
 * them directly instead of receiving prop-drilled handlers from
 * `WorkflowBuilder`.
 *
 * Design notes:
 * - The context owns *mutations only*; nodes/edges/workflow state still lives
 *   in `WorkflowBuilder` (single source of truth).
 * - Every primitive does the bookkeeping each existing handler did (mark
 *   dirty, take undo snapshot, clear selection on delete, reconcile dangling
 *   switch edges, etc.) so callers can forget about it.
 * - Provider value identity is *stable across the WorkflowBuilder lifetime*:
 *   callbacks read state/setters through refs that are updated on each
 *   render, but the memoized value object is created once with `[]` deps.
 *   This prevents every config panel from re-rendering on every keystroke
 *   the builder receives.
 */
import {
  createContext,
  useContext,
  useEffect,
  useMemo,
  useRef,
  type ReactNode,
} from "react";
import type { Edge, Node } from "@xyflow/react";
import {
  getFlowNodeType,
  type FlowNodeData,
} from "../../lib/workflow-flow";
import {
  getStepType,
  mergeStepUpdate,
  type Step,
} from "../../types/workflow";
import type { SwitchNodeData } from "./nodes/SwitchNode";

export interface WorkflowMutations {
  /**
   * Patch a step-bearing node. The caller passes the updated `step`; the
   * primitive merges with the existing step (preserving fields the caller
   * didn't touch) and recomputes the flow-node type. Marks the workflow
   * dirty. No undo snapshot — config edits are intentionally excluded from
   * undo, matching the legacy `handleStepUpdate` behaviour.
   */
  updateStep: (nodeId: string, step: Step) => void;

  /**
   * Patch a switch node's data (cases/label). When `cases` is provided, any
   * edge leaving this switch whose `sourceHandle` no longer maps to a case
   * is removed so `buildWorkflow` never serializes a dangling route.
   */
  updateSwitchNode: (nodeId: string, data: Partial<SwitchNodeData>) => void;

  /** Patch an edge's `data` (label, sourceEvent, etc.). Marks dirty. */
  updateEdge: (edgeId: string, data: Record<string, unknown>) => void;

  /**
   * Rename a node and rewrite every edge that references the old id (source,
   * target, edge-id substring). Keeps the current selection pointing at the
   * renamed node. Takes an undo snapshot before mutating.
   */
  renameNode: (oldId: string, newId: string) => void;

  /** Delete a node. Takes a snapshot; clears node selection. */
  removeNode: (nodeId: string) => void;

  /** Delete an edge. Takes a snapshot; clears edge selection. */
  removeEdge: (edgeId: string) => void;
}

const WorkflowMutationContext = createContext<WorkflowMutations | null>(null);

/** Hook used by config panels to obtain the mutation primitives. */
export function useWorkflowMutations(): WorkflowMutations {
  const ctx = useContext(WorkflowMutationContext);
  if (!ctx) {
    throw new Error(
      "useWorkflowMutations must be used inside <WorkflowMutationProvider> " +
        "(see WorkflowBuilder.tsx).",
    );
  }
  return ctx;
}

/**
 * Dependencies the provider needs from `WorkflowBuilder`. Passed every
 * render — but internally stashed into refs so the produced context value is
 * referentially stable.
 */
export interface WorkflowMutationProviderProps {
  nodes: Node[];
  edges: Edge[];
  setNodes: (updater: (nds: Node[]) => Node[]) => void;
  setEdges: (updater: (eds: Edge[]) => Edge[]) => void;
  setHasModifications: (dirty: boolean) => void;
  takeSnapshot: (nodes: Node[], edges: Edge[]) => void;
  setSelectedNodeId: (
    updater: string | null | ((current: string | null) => string | null),
  ) => void;
  setSelectedEdgeId: (id: string | null) => void;
  children: ReactNode;
}

export function WorkflowMutationProvider({
  nodes,
  edges,
  setNodes,
  setEdges,
  setHasModifications,
  takeSnapshot,
  setSelectedNodeId,
  setSelectedEdgeId,
  children,
}: WorkflowMutationProviderProps) {
  // One ref per dep; refreshed every render. Reading through `.current`
  // inside callbacks keeps them closed over the *latest* values without
  // forcing the memoized context value to change identity.
  const nodesRef = useRef(nodes);
  const edgesRef = useRef(edges);
  const setNodesRef = useRef(setNodes);
  const setEdgesRef = useRef(setEdges);
  const setHasModificationsRef = useRef(setHasModifications);
  const takeSnapshotRef = useRef(takeSnapshot);
  const setSelectedNodeIdRef = useRef(setSelectedNodeId);
  const setSelectedEdgeIdRef = useRef(setSelectedEdgeId);

  // Sync after every render — `useEffect` (not `useLayoutEffect`) is fine
  // because primitives are invoked in response to user input, never during
  // render. By the time any consumer calls one, the effect has flushed.
  useEffect(() => {
    nodesRef.current = nodes;
    edgesRef.current = edges;
    setNodesRef.current = setNodes;
    setEdgesRef.current = setEdges;
    setHasModificationsRef.current = setHasModifications;
    takeSnapshotRef.current = takeSnapshot;
    setSelectedNodeIdRef.current = setSelectedNodeId;
    setSelectedEdgeIdRef.current = setSelectedEdgeId;
  });

  // Built ONCE — `[]` deps. Identity is therefore stable for the provider's
  // lifetime, so config panels (which depend on this value via useContext)
  // do not re-render on every keystroke in `WorkflowBuilder`.
  const value = useMemo<WorkflowMutations>(
    () => ({
      updateStep(nodeId, updatedStep) {
        if (!updatedStep.type) {
          console.error(
            "[WorkflowMutations] updateStep: refusing to apply step with no type:",
            nodeId,
          );
          return;
        }
        if (!updatedStep.id) {
          console.error(
            "[WorkflowMutations] updateStep: refusing to apply step with no id",
          );
          return;
        }
        setHasModificationsRef.current(true);
        setNodesRef.current((nds) =>
          nds.map((node) => {
            if (node.id !== nodeId) return node;
            const currentStep = (node.data as FlowNodeData).step;
            const mergedStep = currentStep
              ? mergeStepUpdate(currentStep, updatedStep)
              : updatedStep;
            const nodeType = getStepType(mergedStep);
            return {
              ...node,
              id: updatedStep.id!,
              type: getFlowNodeType(nodeType),
              data: {
                step: mergedStep,
                label: updatedStep.id!,
              },
            };
          }),
        );
      },

      updateSwitchNode(nodeId, data) {
        setHasModificationsRef.current(true);
        setNodesRef.current((nds) =>
          nds.map((node) =>
            node.id === nodeId
              ? { ...node, data: { ...node.data, ...data } }
              : node,
          ),
        );

        // Reconcile dangling switch edges when case IDs change.
        if (Array.isArray(data.cases)) {
          const validHandles = new Set(data.cases.map((c) => c.id));
          setEdgesRef.current((eds) =>
            eds.filter((edge) => {
              if (edge.source !== nodeId) return true;
              if (!edge.sourceHandle) return true;
              return validHandles.has(edge.sourceHandle);
            }),
          );
        }
      },

      updateEdge(edgeId, data) {
        setHasModificationsRef.current(true);
        setEdgesRef.current((eds) =>
          eds.map((edge) =>
            edge.id === edgeId
              ? { ...edge, data: { ...edge.data, ...data } }
              : edge,
          ),
        );
      },

      renameNode(oldId, newId) {
        if (oldId === newId) return;
        takeSnapshotRef.current(nodesRef.current, edgesRef.current);
        setHasModificationsRef.current(true);

        setNodesRef.current((nds) =>
          nds.map((node) => {
            if (node.id !== oldId) return node;
            const step = (node.data as { step: Step }).step;
            const updatedStep = { ...step, id: newId };
            return {
              ...node,
              id: newId,
              data: {
                ...node.data,
                step: updatedStep,
                label: newId,
              },
            };
          }),
        );

        setEdgesRef.current((eds) =>
          eds.map((edge) => {
            let updated = false;
            const newEdge = { ...edge };
            if (edge.source === oldId) {
              newEdge.source = newId;
              newEdge.id = newEdge.id.replace(
                new RegExp(`^${oldId}-`),
                `${newId}-`,
              );
              updated = true;
            }
            if (edge.target === oldId) {
              newEdge.target = newId;
              newEdge.id = newEdge.id.replace(
                new RegExp(`-${oldId}$`),
                `-${newId}`,
              );
              updated = true;
            }
            return updated ? newEdge : edge;
          }),
        );

        setSelectedNodeIdRef.current((current) =>
          current === oldId ? newId : current,
        );
      },

      removeNode(nodeId) {
        takeSnapshotRef.current(nodesRef.current, edgesRef.current);
        setHasModificationsRef.current(true);
        setNodesRef.current((nds) => nds.filter((node) => node.id !== nodeId));
        setSelectedNodeIdRef.current(null);
        // Orphaned edges are cleaned up by handleNodesChange's remove path.
      },

      removeEdge(edgeId) {
        takeSnapshotRef.current(nodesRef.current, edgesRef.current);
        setHasModificationsRef.current(true);
        setEdgesRef.current((eds) => eds.filter((e) => e.id !== edgeId));
        setSelectedEdgeIdRef.current(null);
      },
    }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [],
  );

  return (
    <WorkflowMutationContext.Provider value={value}>
      {children}
    </WorkflowMutationContext.Provider>
  );
}
