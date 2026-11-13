import { trackEvent } from "@/lib/analytics";

/**
 * Onboarding failures as an opaque CODE plus the sentence the user reads.
 *
 * Session replay masks every text node, so the banner a user saw cannot be read
 * by whoever reviews the replay — an owner watching a sign-up fail could see
 * that a red box appeared and not what it said. The code is the readable half:
 * it rides the banner as `data-error-code` and the session timeline as an
 * `onboarding_error` product event (`reliant forge obs session <id>` lists it).
 *
 * A code comes from this fixed list and never carries the provider's own text,
 * which can echo account or key details. The message stays on screen only.
 */
export type OnboardingErrorCode =
  /** The provider refused the key itself (401, "invalid"). */
  | "key_invalid"
  /** The key authenticated, but its account has no credit or quota left (402, insufficient_quota). */
  | "key_no_credits"
  /** The provider rate-limited the validation call (429). */
  | "key_rate_limited"
  /** OpenRouter: the account's data policy leaves no model to call. */
  | "key_data_policy"
  /** Validation failed for a reason not classified above; the message is the provider's. */
  | "key_validation_failed"
  /** A subscription sign-in failed; the suffix is one of OAUTH_ERROR_CODES (oauthErrorCode). */
  | `oauth_${OAuthErrorSuffix}`
  /** A subscription sign-in failed with a code the hooks do not declare. */
  | "oauth_unknown"
  /** A subscription sign-in threw instead of returning a result. */
  | "oauth_exception"
  /** Finish was reached with no compute chosen. */
  | "compute_missing";

/**
 * Every errorCode the subscription sign-in hooks declare (lib/*-oauth.ts),
 * except `cancelled`, which is never reported. The list is closed: a hook value
 * outside it, whatever text it carries, is reported as `oauth_unknown`.
 */
const OAUTH_ERROR_CODES = [
  "pkce_generation_failed",
  "timeout",
  "state_mismatch",
  "token_exchange_failed",
  "daemon_error",
  "start_failed",
  "denied",
  "poll_error",
] as const;

type OAuthErrorSuffix = (typeof OAUTH_ERROR_CODES)[number];

/** The opaque code for a failed subscription sign-in, from the hook's errorCode. */
export function oauthErrorCode(hookCode: unknown): OnboardingErrorCode {
  return (OAUTH_ERROR_CODES as readonly unknown[]).includes(hookCode)
    ? `oauth_${hookCode as OAuthErrorSuffix}`
    : "oauth_unknown";
}

export interface OnboardingFailure {
  code: OnboardingErrorCode;
  message: string;
}

const DEFAULT_KEY_MESSAGE = "Validation failed. Please check your API key.";

/**
 * Classify a failed API-key validation.
 *
 * Order matters. "No credit" is checked before "invalid" because providers
 * report an unfunded account in bodies that also say `invalid_request_error`
 * (Anthropic: "credit balance is too low"), and before "rate limit" because
 * OpenAI reports exhausted quota as a 429 (`insufficient_quota`). Both used to
 * tell the user their key was wrong or to wait, when the fix is to add credit.
 */
export function describeKeyError(
  errorText: string,
  provider: string,
): OnboardingFailure {
  const lowerError = (errorText || "").toLowerCase();

  if (
    provider === "openrouter" &&
    lowerError.includes("no endpoints found matching your data policy")
  ) {
    return {
      code: "key_data_policy",
      message: "No models available with your current data policy.",
    };
  }
  if (
    /\b402\b/.test(lowerError) ||
    lowerError.includes("payment required") ||
    lowerError.includes("insufficient credits") ||
    lowerError.includes("insufficient_quota") ||
    lowerError.includes("credit balance is too low") ||
    lowerError.includes("quota") ||
    lowerError.includes("billing")
  ) {
    return {
      code: "key_no_credits",
      message:
        "This key works, but its account has no credit left. Add credit with the provider, or choose another option above.",
    };
  }
  if (
    lowerError.includes("unauthorized") ||
    /\b401\b/.test(lowerError) ||
    lowerError.includes("invalid")
  ) {
    return {
      code: "key_invalid",
      message: "Invalid API key. Please check your credentials.",
    };
  }
  if (lowerError.includes("rate limit") || /\b429\b/.test(lowerError)) {
    return {
      code: "key_rate_limited",
      message: "Rate limit exceeded. Please wait and try again.",
    };
  }
  return {
    code: "key_validation_failed",
    message: errorText || DEFAULT_KEY_MESSAGE,
  };
}

/**
 * Record a failure the user was shown. Analytics consent still applies
 * (trackEvent is a no-op without it), and only opaque values go out.
 */
export function reportOnboardingError(
  step: string,
  code: OnboardingErrorCode,
  provider?: string,
): void {
  trackEvent("onboarding_error", {
    step,
    code,
    ...(provider ? { provider } : {}),
  });
}
