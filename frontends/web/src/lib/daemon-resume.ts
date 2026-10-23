// Copyright (c) 2025 Reliant Labs

/**
 * What to say when waking (resuming) a suspended machine is refused.
 *
 * Shared by ResumeDaemonPill, the Inbox's "Wake <machine>" and
 * MachineStatus's "Wake it" (WORKFLOW_UI.md §9.2): three places offer the same
 * action, so they must not each invent their own copy for the same refusal.
 */

import { ENTITLEMENT_REASONS, hasErrorReason } from "./errorReasons";

/**
 * True when a resume was refused because the account's compute is spent
 * (`compute_budget_exhausted`). The fix is a plan or a coupon, so callers
 * offer Billing.
 *
 * Takes the error itself, not its message: the reason rides the metadata,
 * which a message string has lost. control-plane has always sent this
 * reason (svcdaemon daemonDenied), so the old "resource_exhausted" /
 * "compute limit" wording match is gone rather than kept as a fallback.
 */
export function isQuotaResumeError(error: unknown): boolean {
  return hasErrorReason(error, "compute_budget_exhausted");
}

/**
 * The message to show for a refused resume. There is no free tier, so a
 * quota refusal means the account's compute is spent — the old copy ("Free
 * tier compute limit reached") named a tier that does not exist.
 */
export function formatResumeError(error: unknown): string {
  if (isQuotaResumeError(error)) {
    return "You've used the compute included with your account. Upgrade or redeem a coupon to resume this environment.";
  }
  if (error instanceof Error) return error.message;
  return typeof error === "string" ? error : "Failed to resume environment";
}

/** The message to show for whatever a failed resume threw. */
export function resumeErrorMessage(error: unknown): string {
  return formatResumeError(error instanceof Error ? error : "Failed to resume environment");
}

/**
 * Whether a refused resume is a plan/billing problem, so the pill may offer
 * "Upgrade plan": an entitlement reason (ENTITLEMENT_REASONS), never the
 * status code alone — FailedPrecondition is also what "daemon is not
 * suspended" and other non-billing refusals use.
 */
export function resumeErrorNeedsUpgrade(error: unknown): boolean {
  return hasErrorReason(error, ENTITLEMENT_REASONS) || isQuotaResumeError(error);
}

/**
 * A resume refused with `daemon_not_suspended` means the machine is already
 * up or coming up — the outcome the caller wanted, not a failure.
 */
export function isAlreadyResumedError(error: unknown): boolean {
  return hasErrorReason(error, "daemon_not_suspended");
}
