// Copyright (c) 2025 Reliant Labs

/**
 * What a ChangesService failure MEANS to the reader, from its Connect code
 * and the reason the proto header names (lib/errorReasons), never its prose.
 *
 *   not-available      Unimplemented: a control plane without ChangesService yet.
 *   approval-required  PermissionDenied "approval_required": a protected
 *                      environment wants a person's approval (a console
 *                      session, or a step-up code) for a launch, ramp up or
 *                      roll-out. The page offers the code flow.
 *   approval-unavailable FailedPrecondition "approval_unavailable": the
 *                      credential acts for no person to send a code to.
 *   conflict           FailedPrecondition "change_revision_conflict": the
 *                      change moved since it was read. Refetch, show what
 *                      changed, and let the reader decide again.
 *   not-declared       FailedPrecondition "change_not_declared" /
 *                      "variant_not_declared": the environment's current
 *                      release does not declare the key or the variant.
 *   exists             AlreadyExists "change_exists": that key already has a
 *                      change in that environment.
 *   denied             any other PermissionDenied / Unauthenticated.
 *   invalid            InvalidArgument: the server's words, beside the field.
 *   not-found          NotFound: no such change (or not this org's).
 *   unavailable        everything else.
 */

import { Code, ConnectError } from "@connectrpc/connect";

import { hasErrorReason } from "@/lib/errorReasons";

export type ChangeErrorKind =
  | "not-available"
  | "approval-required"
  | "approval-unavailable"
  | "conflict"
  | "not-declared"
  | "exists"
  | "denied"
  | "invalid"
  | "not-found"
  | "unavailable";

export function changeErrorKind(error: unknown): ChangeErrorKind {
  if (!(error instanceof ConnectError)) return "unavailable";
  if (hasErrorReason(error, "approval_required")) return "approval-required";
  if (hasErrorReason(error, "approval_unavailable")) return "approval-unavailable";
  if (hasErrorReason(error, "change_revision_conflict")) return "conflict";
  if (hasErrorReason(error, "change_not_declared") || hasErrorReason(error, "variant_not_declared")) return "not-declared";
  if (hasErrorReason(error, "change_exists")) return "exists";
  switch (error.code) {
    case Code.Unimplemented:
      return "not-available";
    case Code.PermissionDenied:
    case Code.Unauthenticated:
      return "denied";
    case Code.InvalidArgument:
      return "invalid";
    case Code.NotFound:
      return "not-found";
    case Code.AlreadyExists:
      return "exists";
    default:
      return "unavailable";
  }
}

/** The server's own words, without Connect's `[code]` prefix. */
export function changeErrorMessage(error: unknown): string {
  if (error instanceof ConnectError) return error.rawMessage || error.message.replace(/^\[[^\]]+\]\s*/, "");
  if (error instanceof Error) return error.message;
  return "";
}

/** One sentence for a failed write, in the page's words. */
export function describeChangeError(error: unknown): string {
  switch (changeErrorKind(error)) {
    case "not-available":
      return "This control plane doesn't run Experiments yet.";
    case "approval-required":
      return "This environment needs a person's approval for that.";
    case "approval-unavailable":
      return "There's no one to send an approval code to for this sign-in. Approve it from a browser session of the console.";
    case "conflict":
      return "Someone changed this while you were looking. Here is the latest version; nothing was saved.";
    case "not-declared":
      return "The release running in this environment doesn't declare that. Add it to changes.yaml and deploy a release first.";
    case "exists":
      return "That key already has a change in this environment.";
    case "denied":
      return `You can't do that here. ${changeErrorMessage(error)}`.trim();
    case "not-found":
      return "This change no longer exists, or isn't in your organization.";
    case "invalid":
      return changeErrorMessage(error) || "The server didn't accept that.";
    default:
      return changeErrorMessage(error) || "The control plane didn't answer. Try again.";
  }
}

/**
 * Why the results panel has no readout to draw (GetChangeReadout):
 *
 *   not-deployed      Unimplemented / 404: the control plane has no readout
 *                     service yet.
 *   no-experiment     "no_experiment_run": the change never ran an experiment.
 *   not-experiment    "run_not_experiment": the run was a rollout.
 *   no-metric         "metric_missing": launched without a success metric.
 *   plan-invalid      "readout_plan_invalid": no control and treatment.
 *   no-observability  Unavailable: the org's observability is not set up (or
 *                     did not answer).
 *   denied            PermissionDenied ("scope_missing": no obs:read) or
 *                     Unauthenticated.
 *   other             anything else: the server's words.
 */
export type ReadoutErrorKind = "not-deployed" | "no-experiment" | "not-experiment" | "no-metric" | "plan-invalid" | "no-observability" | "denied" | "other";

export function readoutErrorKind(error: unknown): ReadoutErrorKind {
  if (!(error instanceof ConnectError)) return "other";
  if (hasErrorReason(error, "no_experiment_run")) return "no-experiment";
  if (hasErrorReason(error, "run_not_experiment")) return "not-experiment";
  if (hasErrorReason(error, "metric_missing")) return "no-metric";
  if (hasErrorReason(error, "readout_plan_invalid")) return "plan-invalid";
  switch (error.code) {
    case Code.Unimplemented:
      return "not-deployed";
    case Code.Unavailable:
      return "no-observability";
    case Code.PermissionDenied:
    case Code.Unauthenticated:
      return "denied";
    default:
      return "other";
  }
}
