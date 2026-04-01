/**
 * Transform API workflow execution data to sidebar format
 */

import { ChatWorkflowStatus } from "../../../gen/reliant/v1/chat_pb";
import type {
  WorkflowExecutionData,
  StepExecutionData,
} from "../../../types/chat";
import type {
  WorkflowExecution,
  StepExecution,
  WorkflowStatus,
  StepStatus,
} from "./types";

/**
 * Convert API workflow execution to sidebar format
 */
export function transformWorkflowExecution(
  data: WorkflowExecutionData
): WorkflowExecution {
  return {
    id: data.id,
    workflowName: data.workflowName,
    thread: data.thread,
    status: mapWorkflowStatus(data.status),
    parentId: data.parentId,
    threadTitle: data.threadTitle,
    spawnedByNodeId: data.spawnedByNodeId,
    forkedFromThread: data.forkedFromThread,
    parentThread: data.parentThread,
    createdAt: new Date(data.createdAt).getTime(),
    completedAt: data.completedAt
      ? new Date(data.completedAt).getTime()
      : undefined,
    messageCount: data.messageCount,
    children: (data.children || []).map(transformWorkflowExecution),
    steps: (data.steps || []).map(transformStepExecution),
    iteration: data.iteration,
  };
}

/**
 * Convert API step execution to sidebar format
 */
function transformStepExecution(
  step: StepExecutionData
): StepExecution {
  // Parse output_json if present
  let outputJson: Record<string, unknown> | undefined;
  if (step.outputJson) {
    try {
      outputJson = JSON.parse(step.outputJson);
    } catch {
      // Invalid JSON, leave as undefined
    }
  }

  // Determine status from success/exit_code
  let status: StepStatus = "completed";
  if (
    step.success === false ||
    (step.exitCode !== undefined && step.exitCode !== 0)
  ) {
    status = "failed";
  }

  return {
    id: step.id,
    stepId: step.stepId,
    activityName: step.activityName,
    status,
    durationMs: step.durationMs != null ? Number(step.durationMs) : undefined,
    exitCode: step.exitCode,
    success: step.success,
    createdAt: new Date(step.createdAt).getTime(),
    outputJson,
    loopNodeId: step.loopNodeId,
    loopIteration: step.loopIteration,
  };
}

/**
 * Map API status string to WorkflowStatus
 */
function mapWorkflowStatus(status: ChatWorkflowStatus): WorkflowStatus {
  switch (status) {
    case ChatWorkflowStatus.COMPLETED:
      return "completed";
    case ChatWorkflowStatus.FAILED:
      return "failed";
    case ChatWorkflowStatus.CANCELLED:
      return "cancelled";
    case ChatWorkflowStatus.RUNNING:
    case ChatWorkflowStatus.PENDING:
    case ChatWorkflowStatus.PAUSED:
    case ChatWorkflowStatus.EXPIRED:
    case ChatWorkflowStatus.UNSPECIFIED:
    default:
      return "running";
  }
}
