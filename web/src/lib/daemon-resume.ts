// Copyright (c) 2025 Reliant Labs

/**
 * What to say when waking (resuming) a suspended machine is refused.
 *
 * Shared by ResumeDaemonPill, the Inbox's "Wake <machine>" and
 * MachineStatus's "Wake it" (WORKFLOW_UI.md §9.2): three places offer the same
 * action, so they must not each invent their own copy for the same refusal.
 */

import { Code, ConnectError } from "@connectrpc/connect";

function normalize(error: string): string {
  return error.toLowerCase();
}

/**
 * True when a resume was refused because the account's compute entitlement is
 * spent. The fix is a plan or a coupon, so callers offer Billing.
 *
 * The literal-string arm is kept alongside the code because the server's
 * message is not guaranteed to survive as a typed error through every
 * transport; it matches on the entitlement wording rather than "free tier",
 * which nothing emits any more.
 */
export function isQuotaResumeError(error: string): boolean {
  const normalized = normalize(error);
  return normalized.includes("resource_exhausted") || normalized.includes("compute limit");
}

/**
 * The message to show for a refused resume. There is no free tier, so a
 * quota refusal means the account's compute is spent — the old copy ("Free
 * tier compute limit reached") named a tier that does not exist.
 */
export function formatResumeError(error: string): string {
  if (isQuotaResumeError(error)) {
    return "You've used the compute included with your account. Upgrade or redeem a coupon to resume this environment.";
  }
  return error;
}

/** The message carried by whatever a failed resume threw. */
export function resumeErrorMessage(error: unknown): string {
  return formatResumeError(error instanceof Error ? error.message : "Failed to resume environment");
}

/**
 * Whether a refused resume is a plan/billing problem, so the pill may offer
 * "Upgrade plan". Keyed on the server's `x-reliant-reason` header (set on every
 * deliberate entitlement denial) or the quota wording — never on the status
 * code alone: FailedPrecondition is also what "daemon is not suspended" and
 * other non-billing refusals use.
 */
export function resumeErrorNeedsUpgrade(error: unknown): boolean {
  if (error instanceof ConnectError && error.metadata.get("x-reliant-reason")) return true;
  return isQuotaResumeError(error instanceof Error ? error.message : "");
}

/**
 * A resume refused with "not suspended" means the machine is already up or
 * coming up — the outcome the caller wanted, not a failure.
 */
export function isAlreadyResumedError(error: unknown): boolean {
  return (
    error instanceof ConnectError &&
    error.code === Code.FailedPrecondition &&
    !error.metadata.get("x-reliant-reason") &&
    normalize(error.rawMessage).includes("not suspended")
  );
}
