/**
 * Activity Indicator Utilities
 * 
 * Identifies workflow steps that execute but don't save messages,
 * so they can be displayed as activity indicators in the timeline.
 * 
 * Logic:
 * - Steps ending in "-save" are internal save operations (skip)
 * - For regular steps, check if a corresponding "-save" step exists
 * - If the "-save" step has message_id in output, a message was saved (skip)
 * - Otherwise, show an activity indicator
 */

import type { StepExecution, WorkflowExecution } from "../ExecutionSidebar/types";

/**
 * Represents a step that should show an activity indicator
 */
export interface ActivityStep {
  step: StepExecution;
  thread: string;
  workflowName: string;
}

/**
 * Check if a step saved a message by looking for its -save counterpart
 */
function stepSavedMessage(step: StepExecution, allSteps: StepExecution[]): boolean {
  // Find the corresponding save step
  const saveStepId = `${step.stepId}-save`;
  const saveStep = allSteps.find(s => 
    s.stepId === saveStepId && 
    // Match loop context if present
    s.loopIteration === step.loopIteration &&
    s.loopNodeId === step.loopNodeId
  );
  
  if (!saveStep) {
    // No save step exists - no message was saved
    return false;
  }
  
  // Check if save step produced a message_id
  const messageId = saveStep.outputJson?.message_id;
  return !!messageId;
}

/**
 * Get all steps that should show activity indicators for a workflow
 */
/**
 * Internal workflow activities that shouldn't be shown to users.
 * These are workflow plumbing - status updates, fork management, etc.
 * Everything else (including user-defined activities) will be shown.
 */
const INTERNAL_ACTIVITIES = new Set([
  "V2_WorkflowStatus",   // Internal workflow state management
  "V2_WorkflowError",    // Internal error handling
  "V2_Cleanup",          // Internal cleanup
  "V2_FetchThreadResult", // Internal thread fetching
  "V2_FailStep",         // Internal failure handling
  "V2_SaveMessage",      // Produces messages (filtered separately)
  "V2_CallLLM",          // Produces messages (filtered separately)
  "V2_Approval",         // Approvals rendered inline by ToolExecution
]);

/**
 * Check if a step should be shown as an activity indicator.
 * Hides known internal activities; shows everything else including custom.
 */
function isUserFacingActivity(step: StepExecution): boolean {
  return !INTERNAL_ACTIVITIES.has(step.activityName);
}

export function getActivitySteps(workflow: WorkflowExecution): ActivityStep[] {
  const result: ActivityStep[] = [];
  
  function processWorkflow(wf: WorkflowExecution) {
    for (const step of wf.steps) {
      // Skip save steps themselves
      if (step.stepId.endsWith("-save")) continue;
      
      // Skip if this step saved a message
      if (stepSavedMessage(step, wf.steps)) continue;
      
      // Only show user-facing activities (skip internal workflow plumbing)
      if (!isUserFacingActivity(step)) continue;
      
      // This step needs an activity indicator
      result.push({
        step,
        thread: wf.thread,
        workflowName: wf.workflowName,
      });
    }
    
    // Process child workflows
    for (const child of wf.children) {
      processWorkflow(child);
    }
  }
  
  processWorkflow(workflow);
  return result;
}

/**
 * Get activity steps for a specific thread
 */
export function getActivityStepsForThread(
  workflow: WorkflowExecution,
  thread: string
): ActivityStep[] {
  return getActivitySteps(workflow).filter(a => a.thread === thread);
}

/**
 * Get a display name for a step
 */
export function getStepDisplayName(stepId: string): string {
  let name = stepId;
  
  // Strip UUIDs (8-4-4-4-12 format, case insensitive)
  name = name.replace(/[a-f0-9]{8}[- ]?[a-f0-9]{4}[- ]?[a-f0-9]{4}[- ]?[a-f0-9]{4}[- ]?[a-f0-9]{12}/gi, "");
  
  // Remove common prefixes
  name = name.replace(/^(v2_|V2_)/, "");
  
  // Clean up separators and extra spaces
  name = name
    .replace(/[_-]+/g, " ")  // Convert underscores/hyphens to spaces
    .replace(/\s+/g, " ")    // Collapse multiple spaces
    .trim();                  // Remove leading/trailing spaces
  
  // If empty after cleanup, return a generic name
  if (!name) return "Step";
  
  // Title case: capitalize first letter of each word
  name = name
    .split(" ")
    .map(word => word.charAt(0).toUpperCase() + word.slice(1).toLowerCase())
    .join(" ");
  
  return name;
}

/**
 * Get status color for a step
 */
export function getStepStatusColor(step: StepExecution): string {
  if (step.status === "running") {
    return "hsl(var(--primary))";
  }
  if (step.status === "completed" && step.success !== false && step.exitCode !== undefined && step.exitCode !== 0) {
    return "hsl(var(--destructive))";
  }
  if (step.status === "completed") {
    return "hsl(var(--muted-foreground))";
  }
  if (step.status === "failed") {
    return "hsl(var(--destructive))";
  }
  return "hsl(var(--muted-foreground))";
}

/**
 * Format duration in a human-readable way
 */
export function formatStepDuration(durationMs?: number): string {
  if (durationMs === undefined) return "";
  
  if (durationMs < 1000) {
    return `${durationMs}ms`;
  }
  if (durationMs < 60000) {
    return `${(durationMs / 1000).toFixed(1)}s`;
  }
  const minutes = Math.floor(durationMs / 60000);
  const seconds = Math.floor((durationMs % 60000) / 1000);
  return `${minutes}m ${seconds}s`;
}

// Re-export from shared utils
export { formatNodeId } from "./threadUtils";