import { describeAuthError, isPkceVerifierMissing } from "./authErrors";

const ERROR_CODES = new Set([
  "access_denied", "identity_already_exists", "email_exists",
  "server_error", "unexpected_failure", "invalid_client", "invalid_grant",
  "redirect_uri_mismatch", "otp_expired", "otp_disabled",
  "pkce_code_verifier_not_found", "flow_state_expired", "flow_state_not_found",
  "bad_code_verifier", "validation_failed", "over_email_send_rate_limit",
]);

export function safeCallbackErrorCode(value: unknown): string {
  return typeof value === "string" && ERROR_CODES.has(value) ? value : "unknown";
}

export type CallbackFailure = {
  code: string;
  category: "linked_identity" | "account_conflict" | "cancelled" | "authentication_failed";
  message: string;
  alreadyRegistered: boolean;
};

/** Upstream descriptions may contain a live code. Inspect for known cases, never return them. */
export function providerCallbackFailure(error: unknown, description?: string): CallbackFailure {
  const code = safeCallbackErrorCode(error);
  const text = description ?? "";
  if (code === "identity_already_exists" || /already linked|already registered/i.test(text)) {
    return { code, category: "linked_identity", alreadyRegistered: true,
      message: "This account is already registered. You can either sign in and out, or link a different account" };
  }
  if (/Multiple accounts|same email/.test(text)) {
    return { code, category: "account_conflict", alreadyRegistered: false,
      message: "An account with this email already exists. Please sign in with your existing method first, then link your OAuth provider in settings." };
  }
  if (code === "access_denied" || /denied|cancelled/i.test(text)) {
    return { code, category: "cancelled", alreadyRegistered: false,
      message: "Authorization cancelled. Please try again." };
  }
  return { code, category: "authentication_failed", alreadyRegistered: false,
    message: "We couldn't complete sign-in with your provider. Please start sign-in again. If it keeps failing, contact support." };
}

/** SDK error objects/messages can also embed credentials: only fixed codes reach the formatter. */
export function callbackExchangeFailure(error: unknown, fallback: string): { code: string; message: string } {
  const rawCode = typeof error === "object" && error !== null && "code" in error ? error.code : undefined;
  const code = isPkceVerifierMissing(error) ? "pkce_code_verifier_not_found" : safeCallbackErrorCode(rawCode);
  return { code, message: describeAuthError({ code }, fallback) };
}
