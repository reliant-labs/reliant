/**
 * Workflow draft lifecycle (specs/workflow-draft-lifecycle.md), as the UI sees it.
 *
 *   draft    — work in progress: saved as-is, may be invalid, never runnable.
 *   complete — passed validation when it was marked complete; runnable.
 *
 * Validity is never stored. The backend computes findings on read and returns
 * them alongside the status; these helpers turn that into the builder's
 * decisions (can it be marked complete? is it runnable?) so the rules live in
 * one tested place instead of inline in JSX.
 */
import { WorkflowDraftStatus } from "../../gen/reliant/v1/workflow_pb";

export type DraftStatus = "draft" | "complete";

/** Maps the wire enum to the UI status. UNSPECIFIED only appears on responses
 * where nothing was stored; treat it as draft (never runnable). */
export function draftStatusFromProto(status: WorkflowDraftStatus | undefined): DraftStatus {
  return status === WorkflowDraftStatus.COMPLETE ? "complete" : "draft";
}

export function draftStatusToProto(status: DraftStatus): WorkflowDraftStatus {
  return status === "complete" ? WorkflowDraftStatus.COMPLETE : WorkflowDraftStatus.DRAFT;
}

/** A finding from the backend. Warnings are typed "warning:<category>". */
export interface Finding {
  type: string;
  message: string;
}

export function isWarning(finding: Finding): boolean {
  return finding.type.startsWith("warning:");
}

/** Splits backend findings into blocking errors and non-blocking warnings. */
export function splitFindings<T extends Finding>(findings: readonly T[]): { errors: T[]; warnings: T[] } {
  const errors: T[] = [];
  const warnings: T[] = [];
  for (const f of findings) (isWarning(f) ? warnings : errors).push(f);
  return { errors, warnings };
}

/**
 * Why "Mark complete" is disabled, in the order a user should address them.
 * Empty ⇒ enabled. Unsaved edits block because marking complete validates the
 * STORED definition — completing while the canvas differs would mark
 * something other than what the user is looking at.
 */
export function markCompleteBlockers(opts: {
  errors: readonly Finding[];
  hasUnsavedChanges: boolean;
  isSaving?: boolean;
}): string[] {
  const reasons: string[] = [];
  if (opts.isSaving) reasons.push("Wait for the save to finish.");
  if (opts.hasUnsavedChanges) reasons.push("Save your changes first.");
  const errorCount = opts.errors.filter((e) => !isWarning(e)).length;
  if (errorCount > 0) {
    reasons.push(`Fix ${errorCount} validation error${errorCount === 1 ? "" : "s"}.`);
  }
  return reasons;
}

/**
 * Whether a listed workflow may be offered where a runnable workflow is
 * required (chat agent selector, spawn/ref pickers). Builtin and project
 * workflows are always complete.
 */
export function isRunnable(workflow: { status?: DraftStatus; is_hidden?: boolean }): boolean {
  return workflow.status !== "draft" && !workflow.is_hidden;
}

/**
 * The message a rejected save of a COMPLETE workflow shows. The backend
 * refuses to make a complete workflow invalid; the user's way forward is to
 * save it as a draft (which takes it out of service) or fix the errors.
 */
export function isCompleteSaveRejection(response: { success: boolean; validationErrors: readonly Finding[] }): boolean {
  return !response.success && splitFindings(response.validationErrors).errors.length > 0;
}
