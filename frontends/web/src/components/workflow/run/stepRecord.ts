// Copyright (c) 2025 Reliant Labs

/**
 * The full record of a run's steps (ChatService.ListStepExecutions): per
 * attempt, what the step was given (its resolved inputs), what it produced and
 * why it failed. The builder's Run tab inspects a step from these; the lean
 * execution tree (GetWorkflowExecutions) carries none of it. Read through the
 * generated useListStepExecutions (BuilderRunContext); this is its view model.
 */

import type { ListStepExecutionsResponse, StepExecution } from "../../../gen/reliant/v1/chat_pb";

export interface StepRecord {
  id: string;
  workflowId: string;
  /** The activity's step id ("summarize", "summarize-save"). */
  stepId: string;
  activityName: string;
  /** Dotted graph position ("agent.agent_loop.call_llm"); empty on older rows. */
  nodePath: string;
  /** Temporal's 1-based attempt; 0 when not recorded. */
  attempt: number;
  loopNodeId?: string;
  loopIteration?: number;
  createdAtMs: number;
  durationMs?: number;
  exitCode?: number;
  success?: boolean;
  /** The resolved inputs, parsed; undefined when none were recorded. */
  input?: unknown;
  /** The output, parsed; undefined when the attempt produced none. */
  output?: unknown;
  error?: string;
}

function parseJson(text: string): unknown {
  if (!text) return undefined;
  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
}

export function toStepRecord(execution: StepExecution): StepRecord {
  return {
    id: execution.id,
    workflowId: execution.workflowId,
    stepId: execution.stepId,
    activityName: execution.activityName,
    nodePath: execution.nodePath,
    attempt: execution.attempt,
    loopNodeId: execution.loopNodeId,
    loopIteration: execution.loopIteration,
    createdAtMs: Date.parse(execution.createdAt) || 0,
    durationMs: execution.durationMs === undefined ? undefined : Number(execution.durationMs),
    exitCode: execution.exitCode,
    success: execution.success,
    input: parseJson(execution.inputJson),
    output: parseJson(execution.outputJson),
    error: execution.errorMessage || undefined,
  };
}

/** A run's step records, newest first, as ListStepExecutions answered them. */
export function toStepRecords(response: ListStepExecutionsResponse): { records: StepRecord[]; truncated: boolean } {
  return { records: response.stepExecutions.map(toStepRecord), truncated: response.truncated };
}
