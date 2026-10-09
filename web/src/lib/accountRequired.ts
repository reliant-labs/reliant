import { Code, ConnectError } from "@connectrpc/connect";

/**
 * `x-reliant-reason` the control plane sets when it refuses a managed machine
 * or a compute coupon to a session with no real identity behind it. The one
 * thing the owner can do about it is attach an email, so every surface that
 * meets it opens the identity-link modal rather than the plans page.
 */
export const ACCOUNT_REQUIRED_REASON = "account_required";

export const ACCOUNT_REQUIRED_MACHINE_COPY =
  "Add an email to your account to start a cloud machine — we use it to reach you about your machine and billing.";
export const ACCOUNT_REQUIRED_COUPON_COPY =
  "Add an email to your account to redeem this code — we use it to reach you about your machine and billing.";

export function isAccountRequiredError(err: unknown): err is ConnectError {
  return (
    err instanceof ConnectError &&
    err.code === Code.FailedPrecondition &&
    err.metadata.get("x-reliant-reason") === ACCOUNT_REQUIRED_REASON
  );
}
