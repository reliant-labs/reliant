/**
 * Bottom sheet showing read-only detail for one workflow node.
 *
 * Deliberately not `NodeDetailsPanel` — that component is coupled to
 * ReactFlow's `FlowNodeData` shape (positions, loop-expansion callbacks,
 * child-workflow drilling) and renders as a fixed 320px side panel, wider
 * than a 390px phone viewport. This reads the same underlying `Step` /
 * `StepExecution` data directly and renders only what a read-only mobile
 * view needs: what the node is configured to do, and what happened the last
 * time it ran.
 */

import { CheckCircle2, Loader2, X, XCircle } from "lucide-react";
import type { Step } from "../../types/workflow";
import {
  getStepCommand,
  getStepInline,
  getStepRef,
  getStepWhile,
} from "../../types/workflow";
import { getActionArgsRecord, hasTypedArgs } from "../../lib/actionStepArgs";
import { getNodeDisplayName } from "../../lib/node-metadata";
import type { StepExecution } from "../Chat/ExecutionSidebar/types";
import { formatValueForDisplay } from "../../lib/paramUtils";

function displayWorkflowRef(ref: string): string {
  return ref.replace(/^(builtin|project|workflow):\/\//, "");
}

/** The single most useful summary line for a node's static configuration. */
function stepSummary(step: Step): string | null {
  if (step.type === "run") {
    const cmd = getStepCommand(step);
    return cmd || null;
  }
  if (step.type === "workflow" || step.type === "loop") {
    const ref = getStepRef(step);
    if (ref) return displayWorkflowRef(ref);
    if (getStepInline(step)) return "Inline workflow";
    if (step.type === "loop") {
      const whileExpr = getStepWhile(step);
      if (whileExpr) return `while: ${whileExpr}`;
    }
    return null;
  }
  return null;
}

function formatDuration(ms?: number): string {
  if (ms === undefined || ms === null) return "—";
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`;
  return `${(ms / 60000).toFixed(1)}m`;
}

function ExecutionStatusRow({ execution }: { execution: StepExecution }) {
  const config = {
    running: { icon: Loader2, className: "text-primary", iconClass: "animate-spin" },
    completed: { icon: CheckCircle2, className: "text-success", iconClass: "" },
    failed: { icon: XCircle, className: "text-destructive", iconClass: "" },
  } as const;
  const { icon: Icon, className, iconClass } =
    config[execution.status as keyof typeof config] ?? config.completed;

  return (
    <div className="flex items-center gap-2 px-4 py-2">
      <Icon className={`h-4 w-4 shrink-0 ${className} ${iconClass}`} />
      <span className={`text-sm capitalize ${className}`}>{execution.status}</span>
      <span className="ml-auto text-xs text-muted-foreground">
        {formatDuration(execution.durationMs)}
      </span>
    </div>
  );
}

interface MobileWorkflowNodeSheetProps {
  nodeId: string;
  step?: Step;
  /** Most recent step executions for this node, newest first. */
  stepExecutions: StepExecution[];
  onClose: () => void;
}

export function MobileWorkflowNodeSheet({
  nodeId,
  step,
  stepExecutions,
  onClose,
}: MobileWorkflowNodeSheetProps) {
  const typeLabel = step?.type ? getNodeDisplayName(step.type) : "Event";
  const summary = step ? stepSummary(step) : null;
  const argsRecord = step && hasTypedArgs(step) ? getActionArgsRecord(step) : {};
  const argEntries = Object.entries(argsRecord).filter(
    ([, value]) => value !== undefined && value !== "" && value !== null,
  );
  const latest = stepExecutions[0];

  return (
    <div className="fixed inset-0 z-50 flex flex-col justify-end">
      {/* Backdrop closes the sheet — same tap-outside-to-dismiss convention
          as every other overlay in the app. */}
      <button
        type="button"
        aria-label="Dismiss"
        onClick={onClose}
        className="absolute inset-0 bg-black/40"
      />

      <div
        className="relative flex max-h-[75vh] flex-col rounded-t-2xl border-t border-border bg-background shadow-lg"
        style={{ paddingBottom: "env(safe-area-inset-bottom)" }}
      >
        <div className="flex items-center justify-between border-b border-border px-4 py-3">
          <div className="min-w-0 flex-1">
            <div className="truncate text-sm font-semibold text-foreground">{nodeId}</div>
            <div className="text-xs text-muted-foreground">{typeLabel}</div>
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label="Close"
            className="flex min-h-[44px] min-w-[44px] items-center justify-center rounded-md text-muted-foreground active:bg-muted"
          >
            <X className="h-5 w-5" />
          </button>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto">
          {summary && (
            <div className="border-b border-border px-4 py-3">
              <div className="mb-1 text-xs font-medium uppercase tracking-wide text-muted-foreground">
                Configuration
              </div>
              <div className="break-words font-mono text-sm text-foreground">{summary}</div>
            </div>
          )}

          {argEntries.length > 0 && (
            <div className="border-b border-border px-4 py-3">
              <div className="mb-1 text-xs font-medium uppercase tracking-wide text-muted-foreground">
                Parameters
              </div>
              <div className="space-y-1.5">
                {argEntries.map(([key, value]) => (
                  <div key={key} className="flex items-start justify-between gap-3 text-sm">
                    <span className="shrink-0 text-muted-foreground">{key}</span>
                    <span className="min-w-0 break-words text-right text-foreground">
                      {formatValueForDisplay(value)}
                    </span>
                  </div>
                ))}
              </div>
            </div>
          )}

          <div className="px-4 py-3">
            <div className="mb-1 text-xs font-medium uppercase tracking-wide text-muted-foreground">
              Execution
            </div>
            {latest ? (
              <div className="-mx-4">
                <ExecutionStatusRow execution={latest} />
              </div>
            ) : (
              <p className="text-sm text-muted-foreground">Not run yet.</p>
            )}
          </div>

          {/* This view never offers to change the node — editing lives on
              desktop only, where the builder's canvas and CEL editor fit. */}
          <p className="border-t border-border px-4 py-3 text-xs text-muted-foreground">
            Edit this workflow on desktop.
          </p>
        </div>
      </div>
    </div>
  );
}
