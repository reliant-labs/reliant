/**
 * useInlineEditStack
 *
 * Owns the enter/exit handlers for navigating into and out of inline-loop /
 * inline-workflow bodies. When the user clicks a Loop or Workflow node's
 * expand button, we:
 *
 *   1. snapshot the parent workflow (nodes, edges, top-level metadata) onto
 *      a stack;
 *   2. replace the canvas with the inline body's nodes/edges;
 *   3. swap workflow metadata (name, description, inputs, entry) to the
 *      inline body's values.
 *
 * On exit (save or discard) we pop the stack and restore the parent, with
 * the loop/workflow body folded back into the originating step.
 *
 * Post-Wave-4: workflow metadata is owned by a single `workflow` state in
 * the builder, so this hook receives one `setWorkflow` instead of 4 scalar
 * setters. Selection is by id; the hook clears selection ids on enter/exit.
 *
 * Wave-6a: the node→builder bridge moved from `document.dispatchEvent` to a
 * typed `WorkflowNodeCallbacksContext`. The builder wires `enterLoopEdit` /
 * `enterWorkflowEdit` directly into that provider, so this hook no longer
 * subscribes to DOM CustomEvents.
 *
 * Why does the caller pass `loopEditStack` / `setLoopEditStack` in rather
 * than the hook owning them outright?
 *   - `isEditingLoop` (derived from `loopEditStack.length`) is read by
 *     `useFitViewWithPanels`, which is declared earlier in the component.
 *   - The load effect resets the stack on workflow-name change.
 *   - The chat-update path also mutates the stack to keep the parent
 *     workflow in sync with agent edits.
 */

import { useCallback } from "react";
import type { Edge, Node } from "@xyflow/react";
import { toast } from "sonner";
import type {
  LoopStep,
  Step,
  Workflow,
  WorkflowStep,
} from "../../../types/workflow";
import {
  getStepInline,
  getStepRef,
  isLoopStep,
  isWorkflowStep,
  withLoopArgs,
  withWorkflowArgs,
} from "../../../types/workflow";
import { workflowToFlowElements } from "../../../lib/workflow-flow";

/**
 * Navigation context for editing inline workflows (loops or workflow nodes
 * with inline definitions). One entry per nesting level.
 */
export interface InlineEditContext {
  parentWorkflow: Workflow;
  stepId: string;
  stepType: "loop" | "workflow";
  parentNodes: Node[];
  parentEdges: Edge[];
}

// Convert a step with inline workflow to a pseudo Workflow for editing.
// Works for both loop and workflow nodes that have inline definitions.
function inlineStepToWorkflow(
  step: LoopStep | WorkflowStep,
  label: string,
): Workflow {
  const inlineWorkflow = getStepInline(step) || {
    nodes: [],
    edges: [],
    entry: [],
    description: undefined,
    inputs: undefined,
    outputs: undefined,
    ui: undefined,
  };
  // Normalize entry to array format
  const entryArray = inlineWorkflow.entry
    ? Array.isArray(inlineWorkflow.entry)
      ? inlineWorkflow.entry
      : [inlineWorkflow.entry]
    : [];
  return {
    name: `${step.id} (${label})`,
    description:
      inlineWorkflow.description ||
      `Inline ${label.toLowerCase()} for ${step.id}`,
    nodes: inlineWorkflow.nodes || [],
    edges: inlineWorkflow.edges || [],
    inputs: inlineWorkflow.inputs,
    outputs: inlineWorkflow.outputs,
    entry: entryArray,
    ui: inlineWorkflow.ui,
  };
}

// Convert edited workflow back to inline definition.
// Returns the updated inline workflow definition to set on step.inline.
function workflowToInlineDefinition(workflow: Workflow): Workflow {
  return {
    name: workflow.name,
    description: workflow.description,
    nodes: workflow.nodes,
    edges: workflow.edges,
    outputs: workflow.outputs,
    inputs: workflow.inputs,
    entry: workflow.entry || [],
    ui: workflow.ui,
  };
}

export interface UseInlineEditStackArgs {
  /** Current navigation stack (owned by the caller). */
  loopEditStack: InlineEditContext[];
  /** Stack setter (owned by the caller). */
  setLoopEditStack: React.Dispatch<React.SetStateAction<InlineEditContext[]>>;
  nodes: Node[];
  edges: Edge[];
  buildWorkflow: () => Workflow;
  setNodes: (nodes: Node[]) => void;
  setEdges: (edges: Edge[]) => void;
  /** Single workflow-state setter (replaces 4 scalar setters). */
  setWorkflow: React.Dispatch<React.SetStateAction<Workflow>>;
  setSelectedNodeId: (id: string | null) => void;
  setSelectedEdgeId: (id: string | null) => void;
  setIsViewReady: (ready: boolean) => void;
  clearHistory: () => void;
  canDragNodes: boolean;
}

export interface UseInlineEditStackResult {
  enterInlineEdit: (
    step: LoopStep | WorkflowStep,
    stepType: "loop" | "workflow",
  ) => void;
  enterLoopEdit: (loopStep: LoopStep) => void;
  enterWorkflowEdit: (workflowStep: WorkflowStep) => void;
  exitLoopEdit: (saveChanges?: boolean) => void;
}

export function useInlineEditStack({
  loopEditStack,
  setLoopEditStack,
  nodes,
  edges,
  buildWorkflow,
  setNodes,
  setEdges,
  setWorkflow,
  setSelectedNodeId,
  setSelectedEdgeId,
  setIsViewReady,
  clearHistory,
  canDragNodes,
}: UseInlineEditStackArgs): UseInlineEditStackResult {
  // Enter inline editing mode - navigate into an inline workflow (loop or workflow node)
  const enterInlineEdit = useCallback(
    (step: LoopStep | WorkflowStep, stepType: "loop" | "workflow") => {
      // Steps must have an ID to be editable
      if (!step.id) {
        toast.error("Cannot edit step without an ID");
        return;
      }
      // If the step uses a workflow reference (not inline), we can't edit it here
      if (getStepRef(step) && !getStepInline(step)) {
        toast.info(
          "This step references an external workflow. Open that workflow to edit it.",
        );
        return;
      }

      // Save current state to the stack
      const currentWorkflow = buildWorkflow();
      const context: InlineEditContext = {
        parentWorkflow: currentWorkflow,
        stepId: step.id,
        stepType,
        parentNodes: [...nodes],
        parentEdges: [...edges],
      };

      setLoopEditStack((prev) => [...prev, context]);

      // Hide canvas while loading inline body (will show after fit view is applied)
      setIsViewReady(false);

      // Convert step to editable workflow (inline is directly on the step)
      const label = stepType === "loop" ? "Loop Body" : "Inline Workflow";
      const inlineWorkflow = inlineStepToWorkflow(step, label);

      // Convert the inline body via the shared helper.
      // Preserve the "Loop Start" label when entering a loop body.
      // Note: switch metadata from the parent context is intentionally not passed
      // (inline bodies own their own switch state).
      const startLabel = stepType === "loop" ? "Loop Start" : "Workflow Start";
      const { nodes: builtNodes, edges: loadedEdges } = workflowToFlowElements(
        { ...inlineWorkflow, ui: { ...inlineWorkflow.ui, switches: {} } },
        { draggable: canDragNodes, workflowStartLabel: startLabel },
      );

      setNodes(builtNodes as Node[]);
      setEdges(loadedEdges as Edge[]);
      // Swap workflow metadata to the inline body's. Clear entry so that
      // buildWorkflow derives entry from the visual edges.
      setWorkflow((prev) => ({
        ...prev,
        name: inlineWorkflow.name || "",
        description: inlineWorkflow.description || "",
        inputs: inlineWorkflow.inputs || {},
        entry: undefined,
      }));
      setSelectedNodeId(null);
      setSelectedEdgeId(null);
      clearHistory();
    },
    [
      nodes,
      edges,
      buildWorkflow,
      setNodes,
      setEdges,
      setLoopEditStack,
      setWorkflow,
      setSelectedNodeId,
      setSelectedEdgeId,
      setIsViewReady,
      clearHistory,
      canDragNodes,
    ],
  );

  const enterLoopEdit = useCallback(
    (loopStep: LoopStep) => {
      enterInlineEdit(loopStep, "loop");
    },
    [enterInlineEdit],
  );

  const enterWorkflowEdit = useCallback(
    (workflowStep: WorkflowStep) => {
      enterInlineEdit(workflowStep, "workflow");
    },
    [enterInlineEdit],
  );

  // Exit inline editing mode - save changes back to parent and navigate up
  const exitLoopEdit = useCallback(
    (saveChanges: boolean = true) => {
      if (loopEditStack.length === 0) return;

      // Hide canvas while restoring parent workflow (will show after fit view is applied)
      setIsViewReady(false);

      const context = loopEditStack[loopEditStack.length - 1];
      const isStepMatch =
        context.stepType === "loop" ? isLoopStep : isWorkflowStep;

      // Restore parent metadata (shared across save/discard branches).
      const restoreParentMeta = () => {
        setWorkflow((prev) => ({
          ...prev,
          name: context.parentWorkflow.name || "",
          description: context.parentWorkflow.description || "",
          inputs: context.parentWorkflow.inputs || {},
          entry: context.parentWorkflow.entry,
        }));
      };

      if (saveChanges) {
        // Build the current workflow (inline body)
        const inlineBodyWorkflow = buildWorkflow();

        // Find the original step in the parent workflow
        const originalStep = (context.parentWorkflow.nodes || []).find(
          (s) => s.id === context.stepId && isStepMatch(s),
        ) as (LoopStep | WorkflowStep) | undefined;

        if (originalStep) {
          // Convert edited workflow back to inline definition
          const updatedInline = workflowToInlineDefinition(inlineBodyWorkflow);

          // Update the parent nodes with the new inline workflow
          // With flattened schema, inline is directly on the step
          const updatedParentNodes = context.parentNodes.map((node) => {
            if (node.id === context.stepId) {
              const step = (node.data as { step: Step }).step;
              if (isStepMatch(step)) {
                const updatedStep =
                  step.type === "loop"
                    ? withLoopArgs(step, { inline: updatedInline } as any)
                    : withWorkflowArgs(step, { inline: updatedInline } as any);
                return {
                  ...node,
                  data: {
                    ...node.data,
                    step: updatedStep,
                  },
                };
              }
            }
            return node;
          });

          // Restore parent state with updated step
          setNodes(updatedParentNodes as Node[]);
          setEdges(context.parentEdges as Edge[]);
          restoreParentMeta();

          toast.success("Changes applied to workflow");
        } else {
          // Couldn't find original step, just restore without changes
          setNodes(context.parentNodes as Node[]);
          setEdges(context.parentEdges as Edge[]);
          restoreParentMeta();
        }
      } else {
        // Discard changes, just restore parent state
        setNodes(context.parentNodes as Node[]);
        setEdges(context.parentEdges as Edge[]);
        restoreParentMeta();
      }

      // Pop the stack
      setLoopEditStack((prev) => prev.slice(0, -1));
      setSelectedNodeId(null);
      setSelectedEdgeId(null);
      clearHistory();
    },
    [
      loopEditStack,
      buildWorkflow,
      setNodes,
      setEdges,
      setLoopEditStack,
      setWorkflow,
      setSelectedNodeId,
      setSelectedEdgeId,
      setIsViewReady,
      clearHistory,
    ],
  );

  return {
    enterInlineEdit,
    enterLoopEdit,
    enterWorkflowEdit,
    exitLoopEdit,
  };
}
