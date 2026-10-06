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
 * The words the UI uses for the two states. The wire and the backend say
 * "complete"; everywhere a person reads it, a runnable workflow is
 * "Published" and the act of making it runnable is "Publish".
 */
export const DRAFT_STATUS_LABEL: Record<DraftStatus, string> = {
  draft: "Draft",
  complete: "Published",
};

/** Why a draft cannot run, in the words every surface uses. */
export const DRAFT_NOT_RUNNABLE = "Drafts can't run until they are published.";

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
