/**
 * Read-only, ordered list of a workflow's nodes with live execution status.
 *
 * The desktop `WorkflowViewer` renders the same information as a pannable
 * ReactFlow canvas. That layout exists to show branching/parallel structure
 * spatially, which needs room a 390px viewport doesn't have — the auto-layout
 * algorithm spaces nodes for a wide canvas, and answering "what is my agent
 * doing right now" by pinch-zooming a phone screen is the wrong shape for the
 * question. A flat, ordered list matches vertical scroll and needs no pan/zoom
 * chrome. See `workflowStepOrder.ts` for how the order is derived.
 */

import { CheckCircle2, Circle, Loader2, XCircle } from "lucide-react";
import type { Workflow } from "../../types/workflow";
import type { NodeExecutionStatus } from "../../lib/workflow-flow";
import { getNodeDisplayName, getNodeIcon } from "../../lib/node-metadata";
import { cn } from "../../lib/utils";
import { orderedWorkflowNodeIds } from "./workflowStepOrder";

function StatusIcon({ status }: { status?: NodeExecutionStatus }) {
  switch (status) {
    case "running":
      return <Loader2 className="h-4 w-4 shrink-0 animate-spin text-primary" />;
    case "completed":
      return <CheckCircle2 className="h-4 w-4 shrink-0 text-success" />;
    case "failed":
      return <XCircle className="h-4 w-4 shrink-0 text-destructive" />;
    default:
      return <Circle className="h-4 w-4 shrink-0 text-muted-foreground/40" />;
  }
}

function statusLabel(status?: NodeExecutionStatus): string {
  switch (status) {
    case "running":
      return "Running";
    case "completed":
      return "Done";
    case "failed":
      return "Failed";
    default:
      return "Pending";
  }
}

interface MobileWorkflowStepListProps {
  workflow: Workflow;
  /** Node ID → execution status, e.g. from `useExtendedExecutionStatus`. */
  statusMap: Record<string, NodeExecutionStatus>;
  /** Called when a row is tapped — the caller owns sheet/detail state. */
  onSelectNode: (nodeId: string) => void;
  /** Currently open node, for a subtle selected-row highlight. */
  selectedNodeId?: string | null;
}

export function MobileWorkflowStepList({
  workflow,
  statusMap,
  onSelectNode,
  selectedNodeId,
}: MobileWorkflowStepListProps) {
  const nodesById = new Map((workflow.nodes ?? []).map((n) => [n.id, n]));
  const orderedIds = orderedWorkflowNodeIds(workflow);

  if (orderedIds.length === 0) {
    return (
      <div className="flex flex-1 items-center justify-center px-6 py-10 text-center text-sm text-muted-foreground">
        This workflow has no steps.
      </div>
    );
  }

  return (
    <ol className="flex flex-1 flex-col overflow-y-auto py-1">
      {orderedIds.map((nodeId) => {
        const step = nodesById.get(nodeId);
        const typeKey = step?.type || "action";
        const Icon = getNodeIcon(typeKey);
        const status = statusMap[nodeId];
        const selected = selectedNodeId === nodeId;

        return (
          <li key={nodeId}>
            <button
              type="button"
              onClick={() => onSelectNode(nodeId)}
              // 56px — comfortably above the 44px floor, matching the chat
              // list row height so the two surfaces feel consistent.
              className={cn(
                "flex min-h-14 w-full items-center gap-3 border-b border-border px-4 py-3 text-left",
                "active:bg-muted/50",
                selected && "bg-muted/40",
              )}
            >
              <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-muted">
                <Icon className="h-4 w-4 text-muted-foreground" />
              </span>
              <div className="min-w-0 flex-1">
                <div className="truncate text-sm text-foreground">
                  {getNodeDisplayName(typeKey)}
                </div>
                <div className="truncate text-xs text-muted-foreground">{nodeId}</div>
              </div>
              <div className="flex shrink-0 items-center gap-1.5 text-xs text-muted-foreground">
                <StatusIcon status={status} />
                <span>{statusLabel(status)}</span>
              </div>
            </button>
          </li>
        );
      })}
    </ol>
  );
}
