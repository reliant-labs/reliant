/**
 * NodeDetailsPanel - Slide-out panel showing node configuration and execution details
 *
 * Displays:
 * - Node type and ID
 * - Configuration from workflow definition
 * - Execution status, duration, timestamps
 * - Output JSON from step execution
 * - "View Execution" button for workflow nodes with children
 */

import { memo, useState } from "react";
import {
  X,
  ChevronDown,
  ChevronRight,
  Clock,
  CheckCircle,
  XCircle,
  Loader2,
  ExternalLink,
} from "lucide-react";
import type {
  FlowNodeData,
  NodeExecutionStatus,
} from "../../lib/workflow-flow";
import type { Step } from "../../types/workflow";
import { getStepCommand, getStepRef, getStepInline, getStepWhile } from "../../types/workflow";
import type {
  StepExecution,
  WorkflowExecution,
} from "../Chat/ExecutionSidebar/types";
import type { LoopIterationInfo } from "./hooks/useExecutionStatus";
import { getNodeDisplayName } from "../../lib/node-metadata";
import { unwrapProtoInputs } from "../../lib/protoValueUtils";
import { getActionArgsRecord, hasTypedArgs } from "../../lib/actionStepArgs";

interface NodeDetailsPanelProps {
  /** The selected node's data */
  nodeData: FlowNodeData;
  /** Node ID */
  nodeId: string;
  /** Step executions for this node (may be multiple for loops) */
  stepExecutions: StepExecution[];
  /** Child workflow if this is a workflow node */
  childWorkflow?: WorkflowExecution;
  /** All loop iterations if this is a loop node (legacy child workflow approach) */
  loopIterations?: WorkflowExecution[];
  /** Loop iterations from step executions (inline loop approach) */
  loopIterationSteps?: LoopIterationInfo[];
  /** Close the panel */
  onClose: () => void;
  /** Navigate into sub-workflow */
  onViewSubWorkflow?: (childWorkflow: WorkflowExecution) => void;
  /** Viewer mode - inline or side */
  viewerMode?: "inline" | "side";
}

/** Collapsible section component */
function Section({
  title,
  defaultOpen = true,
  children,
}: {
  title: string;
  defaultOpen?: boolean;
  children: React.ReactNode;
}) {
  const [isOpen, setIsOpen] = useState(defaultOpen);

  return (
    <div className="border-b border-border last:border-b-0">
      <button
        onClick={() => setIsOpen(!isOpen)}
        className="flex items-center gap-2 w-full px-4 py-2 text-sm font-medium text-foreground hover:bg-muted/50 transition-colors"
      >
        {isOpen ? (
          <ChevronDown className="w-4 h-4 text-muted-foreground" />
        ) : (
          <ChevronRight className="w-4 h-4 text-muted-foreground" />
        )}
        {title}
      </button>
      {isOpen && <div className="px-4 pb-3">{children}</div>}
    </div>
  );
}

/** JSON viewer with syntax highlighting */
function JsonViewer({
  data,
  maxHeight = 200,
}: {
  data: unknown;
  maxHeight?: number;
}) {
  if (data === null || data === undefined) {
    return <span className="text-muted-foreground italic">null</span>;
  }

  const jsonString = JSON.stringify(data, null, 2);

  return (
    <pre
      className="text-xs font-mono bg-muted/50 rounded p-2 overflow-auto whitespace-pre-wrap break-words"
      style={{ maxHeight }}
    >
      {jsonString}
    </pre>
  );
}

/** Status badge component */
function StatusBadge({ status }: { status?: NodeExecutionStatus | string }) {
  const config = {
    running: {
      icon: Loader2,
      className: "bg-blue-100 text-blue-700",
      iconClass: "animate-spin",
    },
    completed: {
      icon: CheckCircle,
      className: "bg-emerald-100 text-emerald-700",
      iconClass: "",
    },
    failed: {
      icon: XCircle,
      className: "bg-red-100 text-red-700",
      iconClass: "",
    },
    pending: {
      icon: Clock,
      className: "bg-gray-100 text-gray-600",
      iconClass: "",
    },
  };

  const {
    icon: Icon,
    className,
    iconClass,
  } = config[status as keyof typeof config] || config.pending;

  return (
    <span
      className={`inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-xs font-medium ${className}`}
    >
      <Icon className={`w-3 h-3 ${iconClass}`} />
      {status || "pending"}
    </span>
  );
}

/** Get human-readable node type from proto Step */
function getNodeType(step?: Step): string {
  if (!step || !step.type) return "Event";
  return getNodeDisplayName(step.type);
}

function getConditionExpression(condition: Step["condition"]): string {
  return condition?.expr ?? "";
}

/** Get the main value (workflow ref, command, etc.) from proto Step */
function getStepValue(step?: Step): string | null {
  if (!step) return null;
  // Run step: show command
  if (step.type === "run") {
    const cmd = getStepCommand(step);
    if (cmd) return cmd;
  }
  // Workflow step: show ref or inline indicator
  if (step.type === "workflow") {
    const ref = getStepRef(step);
    if (ref) return ref;
    if (getStepInline(step)) return "inline workflow";
  }
  // Loop step: show while condition or ref
  if (step.type === "loop") {
    const ref = getStepRef(step);
    if (ref) return ref;
    if (getStepInline(step)) return "inline workflow";
    const whileExpr = getStepWhile(step);
    if (whileExpr) return `while: ${whileExpr}`;
    return "loop";
  }
  // Join step: show condition
  if (step.type === "join") {
    const joinCondition = getConditionExpression(step.condition);
    if (joinCondition) return joinCondition;
  }
  return null;
}

/** Format duration in human-readable form */
function formatDuration(ms?: number): string {
  if (ms === undefined || ms === null) return "-";
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`;
  return `${(ms / 60000).toFixed(1)}m`;
}

/** Format timestamp */
function formatTime(timestamp?: number): string {
  if (!timestamp) return "-";
  return new Date(timestamp).toLocaleTimeString();
}

export const NodeDetailsPanel = memo(function NodeDetailsPanel({
  nodeData,
  nodeId,
  stepExecutions,
  childWorkflow,
  loopIterations,
  loopIterationSteps,
  onClose,
  onViewSubWorkflow,
  viewerMode = "side",
}: NodeDetailsPanelProps) {
  const { step, executionStatus } = nodeData;
  const nodeType = getNodeType(step);
  const stepValue = getStepValue(step);

  // For loop nodes, track selected iteration
  const isLoopNode = nodeType === "Loop";
  const [selectedIteration, setSelectedIteration] = useState<number>(0);

  // Determine if we have loop data from either source
  const hasLoopChildWorkflows = loopIterations && loopIterations.length > 0;
  const hasLoopStepIterations =
    loopIterationSteps && loopIterationSteps.length > 0;
  const hasLoopData = hasLoopChildWorkflows || hasLoopStepIterations;

  // Get the selected iteration's workflow (for loops with child workflows)
  const selectedIterationWorkflow =
    isLoopNode && hasLoopChildWorkflows
      ? loopIterations![selectedIteration]
      : undefined;

  // Get the selected iteration's step data (for inline loops)
  const selectedIterationStepData =
    isLoopNode && hasLoopStepIterations && !hasLoopChildWorkflows
      ? loopIterationSteps![selectedIteration]
      : undefined;

  // Get the latest step execution for summary
  const latestExecution =
    stepExecutions.length > 0
      ? stepExecutions.reduce((latest, curr) =>
          curr.createdAt > latest.createdAt ? curr : latest,
        )
      : null;

  // Calculate total duration across all executions
  const totalDuration = stepExecutions.reduce(
    (sum, exec) => sum + (exec.durationMs || 0),
    0,
  );

  return (
    <div
      className={`w-80 bg-background border-l border-border flex flex-col h-full overflow-hidden shadow-lg ${viewerMode === "inline" ? "border-t border-border" : ""}`}
    >
      {/* Header */}
      <div className="flex items-center justify-between px-4 py-3 border-b border-border bg-muted/30">
        <div className="flex-1 min-w-0">
          <h3 className="font-semibold text-foreground truncate">{nodeId}</h3>
          <div className="flex items-center gap-2 mt-1">
            <span className="text-xs text-muted-foreground">{nodeType}</span>
            <StatusBadge status={executionStatus} />
          </div>
        </div>
        <button
          onClick={onClose}
          className="p-1 hover:bg-muted rounded transition-colors"
        >
          <X className="w-4 h-4 text-muted-foreground" />
        </button>
      </div>

      {/* Scrollable content */}
      <div className="flex-1 overflow-y-auto">
        {/* Configuration Section */}
        <Section title="Configuration" defaultOpen={true}>
          {stepValue && (
            <div className="mb-2">
              <span className="text-xs text-muted-foreground">Reference:</span>
              <div className="font-mono text-sm text-foreground break-all">
                {stepValue}
              </div>
            </div>
          )}

          {step && hasTypedArgs(step) && Object.keys(getActionArgsRecord(step)).length > 0 && (
            <div>
              <span className="text-xs text-muted-foreground">Inputs:</span>
              <JsonViewer data={unwrapProtoInputs(getActionArgsRecord(step))} maxHeight={150} />
            </div>
          )}

          {step?.type === "loop" && getStepWhile(step) && (
            <div className="mt-2 space-y-1">
              <div className="text-xs">
                <span className="text-muted-foreground">While:</span>{" "}
                <code className="text-foreground bg-muted px-1 rounded">
                  {getStepWhile(step)}
                </code>
              </div>
              {getStepRef(step) && (
                <div className="text-xs">
                  <span className="text-muted-foreground">Workflow:</span>{" "}
                  <span className="text-foreground">{getStepRef(step)}</span>
                </div>
              )}
            </div>
          )}

          {!stepValue && !(step && Object.keys(getActionArgsRecord(step) || {}).length > 0) && (
            <p className="text-xs text-muted-foreground italic">
              No configuration
            </p>
          )}
        </Section>

        {/* Execution Section */}
        <Section title="Execution" defaultOpen={true}>
          {/* Loop iteration selector - handles both child workflow and inline step approaches */}
          {isLoopNode && hasLoopData ? (
            <div className="space-y-3">
              {/* Iteration selector */}
              <div>
                <label className="text-xs text-muted-foreground block mb-1">
                  Iteration
                </label>
                <select
                  value={selectedIteration}
                  onChange={(e) => setSelectedIteration(Number(e.target.value))}
                  className="w-full px-2 py-1.5 text-sm border border-input rounded-md bg-background text-foreground focus:ring-2 focus:ring-ring"
                >
                  {/* Child workflow iterations */}
                  {hasLoopChildWorkflows &&
                    loopIterations!.map((iter, idx) => (
                      <option key={iter.id} value={idx}>
                        Iteration {idx + 1} - {iter.status}
                      </option>
                    ))}
                  {/* Step-based iterations (inline loops) */}
                  {hasLoopStepIterations &&
                    !hasLoopChildWorkflows &&
                    loopIterationSteps!.map((iter, idx) => (
                      <option key={`iter-${iter.iteration}`} value={idx}>
                        Iteration {iter.iteration + 1} - {iter.status}
                      </option>
                    ))}
                </select>
              </div>

              {/* Selected iteration details - child workflow approach */}
              {selectedIterationWorkflow && (
                <div className="grid grid-cols-2 gap-2 text-xs">
                  <div>
                    <span className="text-muted-foreground">Status:</span>
                    <div className="mt-0.5">
                      <StatusBadge status={selectedIterationWorkflow.status} />
                    </div>
                  </div>
                  <div>
                    <span className="text-muted-foreground">Duration:</span>
                    <div className="text-foreground font-medium mt-0.5">
                      {selectedIterationWorkflow.completedAt
                        ? formatDuration(
                            selectedIterationWorkflow.completedAt -
                              selectedIterationWorkflow.createdAt,
                          )
                        : "Running..."}
                    </div>
                  </div>
                  <div>
                    <span className="text-muted-foreground">Started:</span>
                    <div className="text-foreground mt-0.5">
                      {formatTime(selectedIterationWorkflow.createdAt)}
                    </div>
                  </div>
                  <div>
                    <span className="text-muted-foreground">Steps:</span>
                    <div className="text-foreground mt-0.5">
                      {selectedIterationWorkflow.steps.length}
                    </div>
                  </div>
                </div>
              )}

              {/* Selected iteration details - inline step approach */}
              {selectedIterationStepData && (
                <div className="grid grid-cols-2 gap-2 text-xs">
                  <div>
                    <span className="text-muted-foreground">Status:</span>
                    <div className="mt-0.5">
                      <StatusBadge status={selectedIterationStepData.status} />
                    </div>
                  </div>
                  <div>
                    <span className="text-muted-foreground">Duration:</span>
                    <div className="text-foreground font-medium mt-0.5">
                      {formatDuration(
                        selectedIterationStepData.latestCreatedAt -
                          selectedIterationStepData.earliestCreatedAt,
                      )}
                    </div>
                  </div>
                  <div>
                    <span className="text-muted-foreground">Started:</span>
                    <div className="text-foreground mt-0.5">
                      {formatTime(selectedIterationStepData.earliestCreatedAt)}
                    </div>
                  </div>
                  <div>
                    <span className="text-muted-foreground">Steps:</span>
                    <div className="text-foreground mt-0.5">
                      {selectedIterationStepData.steps.length}
                    </div>
                  </div>
                </div>
              )}
            </div>
          ) : latestExecution ? (
            <div className="space-y-2">
              <div className="grid grid-cols-2 gap-2 text-xs">
                <div>
                  <span className="text-muted-foreground">Status:</span>
                  <div className="mt-0.5">
                    <StatusBadge status={latestExecution.status} />
                  </div>
                </div>
                <div>
                  <span className="text-muted-foreground">Duration:</span>
                  <div className="text-foreground font-medium mt-0.5">
                    {formatDuration(totalDuration)}
                  </div>
                </div>
                <div>
                  <span className="text-muted-foreground">Started:</span>
                  <div className="text-foreground mt-0.5">
                    {formatTime(latestExecution.createdAt)}
                  </div>
                </div>
                <div>
                  <span className="text-muted-foreground">Activity:</span>
                  <div
                    className="text-foreground mt-0.5 truncate"
                    title={latestExecution.activityName}
                  >
                    {latestExecution.activityName.replace("V2_", "")}
                  </div>
                </div>
              </div>

              {stepExecutions.length > 1 && (
                <div className="text-xs text-muted-foreground mt-2 pt-2 border-t border-border">
                  {stepExecutions.length} executions (loop iterations or
                  retries)
                </div>
              )}
            </div>
          ) : childWorkflow ? (
            <div className="space-y-2">
              <div className="grid grid-cols-2 gap-2 text-xs">
                <div>
                  <span className="text-muted-foreground">Status:</span>
                  <div className="mt-0.5">
                    <StatusBadge status={childWorkflow.status} />
                  </div>
                </div>
                <div>
                  <span className="text-muted-foreground">Duration:</span>
                  <div className="text-foreground font-medium mt-0.5">
                    {childWorkflow.completedAt
                      ? formatDuration(
                          childWorkflow.completedAt - childWorkflow.createdAt,
                        )
                      : "Running..."}
                  </div>
                </div>
                <div>
                  <span className="text-muted-foreground">Started:</span>
                  <div className="text-foreground mt-0.5">
                    {formatTime(childWorkflow.createdAt)}
                  </div>
                </div>
                <div>
                  <span className="text-muted-foreground">Steps:</span>
                  <div className="text-foreground mt-0.5">
                    {childWorkflow.steps.length}
                  </div>
                </div>
              </div>
            </div>
          ) : (
            <p className="text-xs text-muted-foreground italic">
              Not yet executed
            </p>
          )}
        </Section>

        {/* Output Section - from step execution */}
        {latestExecution?.outputJson && (
          <Section title="Output" defaultOpen={false}>
            <JsonViewer data={latestExecution.outputJson} maxHeight={300} />
          </Section>
        )}

        {/* Output Section - from loop iteration (child workflow - selected iteration's last step output) */}
        {!latestExecution?.outputJson &&
          selectedIterationWorkflow &&
          selectedIterationWorkflow.steps.length > 0 &&
          (() => {
            const lastStepWithOutput = selectedIterationWorkflow.steps
              .filter((s) => s.outputJson)
              .sort((a, b) => b.createdAt - a.createdAt)[0];

            if (!lastStepWithOutput) return null;

            return (
              <Section title="Output" defaultOpen={false}>
                <JsonViewer
                  data={lastStepWithOutput.outputJson}
                  maxHeight={300}
                />
              </Section>
            );
          })()}

        {/* Output Section - from inline loop iteration (selected iteration's last step output) */}
        {!latestExecution?.outputJson &&
          !selectedIterationWorkflow &&
          selectedIterationStepData &&
          selectedIterationStepData.steps.length > 0 &&
          (() => {
            const lastStepWithOutput = selectedIterationStepData.steps
              .filter((s) => s.outputJson)
              .sort((a, b) => b.createdAt - a.createdAt)[0];

            if (!lastStepWithOutput) return null;

            return (
              <Section title="Output" defaultOpen={false}>
                <JsonViewer
                  data={lastStepWithOutput.outputJson}
                  maxHeight={300}
                />
              </Section>
            );
          })()}

        {/* Output Section - from child workflow (last step output) */}
        {!latestExecution?.outputJson &&
          !selectedIterationWorkflow &&
          childWorkflow &&
          childWorkflow.steps.length > 0 &&
          (() => {
            // Get the last step with output (final workflow output)
            const lastStepWithOutput = childWorkflow.steps
              .filter((s) => s.outputJson)
              .sort((a, b) => b.createdAt - a.createdAt)[0];

            if (!lastStepWithOutput) return null;

            return (
              <Section title="Output" defaultOpen={false}>
                <JsonViewer
                  data={lastStepWithOutput.outputJson}
                  maxHeight={300}
                />
              </Section>
            );
          })()}

        {/* All Executions Section (if multiple) */}
        {stepExecutions.length > 1 && (
          <Section
            title={`All Executions (${stepExecutions.length})`}
            defaultOpen={false}
          >
            <div className="space-y-2">
              {stepExecutions.map((exec, idx) => (
                <div
                  key={exec.id}
                  className="text-xs p-2 bg-muted/50 rounded border border-border"
                >
                  <div className="flex items-center justify-between mb-1">
                    <span className="font-medium">
                      #{stepExecutions.length - idx}
                    </span>
                    <StatusBadge status={exec.status} />
                  </div>
                  <div className="text-muted-foreground">
                    {formatTime(exec.createdAt)} •{" "}
                    {formatDuration(exec.durationMs)}
                  </div>
                  {exec.loopIteration !== undefined && (
                    <div className="text-muted-foreground">
                      Loop iteration: {exec.loopIteration + 1}
                    </div>
                  )}
                </div>
              ))}
            </div>
          </Section>
        )}
      </div>

      {/* Footer with actions */}
      {(childWorkflow || selectedIterationWorkflow) && onViewSubWorkflow && (
        <div className="px-4 py-3 border-t border-border bg-muted/30">
          <button
            onClick={() =>
              onViewSubWorkflow(selectedIterationWorkflow || childWorkflow!)
            }
            className="w-full flex items-center justify-center gap-2 px-3 py-2 bg-primary text-primary-foreground rounded-md text-sm font-medium hover:bg-primary/90 transition-colors"
          >
            <ExternalLink className="w-4 h-4" />
            {selectedIterationWorkflow
              ? `View Iteration ${selectedIteration + 1}`
              : "View Sub-workflow Execution"}
          </button>
        </div>
      )}
    </div>
  );
});