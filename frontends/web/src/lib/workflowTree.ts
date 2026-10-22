import type { WorkflowExecutionData } from "../types/chat";

/** Find a workflow by id anywhere in the execution tree (depth-first). */
export function findWorkflowById(
  roots: WorkflowExecutionData[],
  id: string,
): WorkflowExecutionData | undefined {
  for (const wf of roots) {
    if (wf.id === id) return wf;
    const found = findWorkflowById(wf.children, id);
    if (found) return found;
  }
  return undefined;
}
