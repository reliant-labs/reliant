/**
 * The builder's assistant panel starts closed. It opens by itself only for a
 * new, empty draft, where there is nothing on the canvas for it to cover and
 * "describe what you want" is the natural first move. Everything else (an
 * existing workflow, a built-in, any workflows-tour step) leaves it closed;
 * the floating button still opens it.
 */

import { useEffect, useRef } from "react";

import type { Workflow } from "../../../types/workflow";
import { currentTourParam, isWorkflowsTour } from "../../Onboarding/journeys/workflowsTour";

export function shouldOpenAssistantPanel({
  workflow,
  isBuiltin,
  tourParam,
}: {
  workflow: Workflow | undefined;
  isBuiltin: boolean;
  tourParam: string | null;
}): boolean {
  if (!workflow || isBuiltin) return false;
  if (isWorkflowsTour(tourParam)) return false;
  return (workflow.nodes?.length ?? 0) === 0;
}

/** Opens the panel once per workflow, when it loads as a new, empty draft. */
export function useAssistantPanelDefault({
  workflow,
  workflowKey,
  isBuiltin,
  openPanel,
}: {
  workflow: Workflow | undefined;
  /** Stable across a rename (the draft id, else the name). */
  workflowKey: string | undefined;
  isBuiltin: boolean;
  openPanel: () => void;
}): void {
  const decidedFor = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (!workflow || !workflowKey || decidedFor.current === workflowKey) return;
    decidedFor.current = workflowKey;
    if (shouldOpenAssistantPanel({ workflow, isBuiltin, tourParam: currentTourParam() })) openPanel();
    // openPanel is a state setter; the decision is per workflow, not per render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workflow, workflowKey, isBuiltin]);
}
