/**
 * WorkflowNodeCallbacksContext
 * ----------------------------
 * Typed callback bridge from shared graph nodes (LoopNode, WorkflowNode)
 * back to whatever container is hosting them. Replaces the legacy
 * `document.dispatchEvent(new CustomEvent('loop-expand' | 'workflow-expand'))`
 * pattern so node→parent communication is type-checked instead of stringly-typed.
 *
 * Why a sibling context rather than extending `WorkflowMutationContext`:
 * - The viewer renders the same node components but does not provide
 *   `WorkflowMutationProvider` (it doesn't mutate the graph). Extending the
 *   mutation context would force the viewer to wrap with a no-op mutation
 *   provider just to register an expand handler.
 * - The surface here is intentionally narrow: just node-emit callbacks.
 *
 * Two consumers today:
 *   1. WorkflowBuilder — `onExpandLoop` / `onExpandWorkflow` drill into the
 *      inline body via `useInlineEditStack`.
 *   2. WorkflowViewer — `onExpandLoop` toggles runtime loop expansion via
 *      `useExpandedLoops`.
 *
 * Both callbacks are optional so each container supplies only what it needs.
 * Nodes that call a missing callback get a clear console warning.
 */
import { createContext, useContext, useMemo, type ReactNode } from "react";
import type { LoopStep, WorkflowStep } from "../../types/workflow";

export interface WorkflowNodeCallbacks {
  /** Loop node clicked its expand button. */
  onExpandLoop?: (loopNodeId: string, step: LoopStep) => void;
  /** Workflow (agent) node clicked its expand button. */
  onExpandWorkflow?: (workflowNodeId: string, step: WorkflowStep) => void;
}

const WorkflowNodeCallbacksContext =
  createContext<WorkflowNodeCallbacks | null>(null);

/**
 * Hook used by `LoopNode` / `WorkflowNode` to obtain the expand callbacks.
 * Returns an empty object outside any provider so unprovided callers are
 * inert rather than throwing — nodes can render in storybook etc. without
 * a provider.
 */
export function useWorkflowNodeCallbacks(): WorkflowNodeCallbacks {
  return useContext(WorkflowNodeCallbacksContext) ?? {};
}

export interface WorkflowNodeCallbacksProviderProps
  extends WorkflowNodeCallbacks {
  children: ReactNode;
}

export function WorkflowNodeCallbacksProvider({
  onExpandLoop,
  onExpandWorkflow,
  children,
}: WorkflowNodeCallbacksProviderProps) {
  const value = useMemo<WorkflowNodeCallbacks>(
    () => ({ onExpandLoop, onExpandWorkflow }),
    [onExpandLoop, onExpandWorkflow],
  );
  return (
    <WorkflowNodeCallbacksContext.Provider value={value}>
      {children}
    </WorkflowNodeCallbacksContext.Provider>
  );
}
