import { Code, ConnectError } from "@connectrpc/connect";

import { stripServerFraming } from "@reliantlabs/forge-web-runtime";

import { errorReason } from "./errorReasons";

/**
 * What to tell someone whose coupon code was refused.
 *
 * RedeemCoupon fails with one of six stable reasons (control-plane
 * internal/coupon/errors.go) and each is a different conversation: a typo, a
 * code that ended, a code that ran out, a code this org already used. The
 * reason picks the sentence and the next step, so rewording the server's copy
 * never changes what the user is told to do. A refusal whose reason this web
 * does not know shows the server's own sentence, because it is per-case and
 * already user-facing; only a failure with nothing to show (the network, a
 * redacted 500) gets generic words.
 */
const BY_REASON: Readonly<Record<string, string>> = {
  coupon_not_found: "That code isn't valid. Check it for typos, or ask whoever gave it to you.",
  coupon_inactive: "That coupon is no longer active, so it can't be redeemed.",
  coupon_expired: "That coupon has expired.",
  coupon_exhausted: "That coupon has been fully redeemed — there are no uses left.",
  coupon_too_many_attempts: "Too many failed attempts. Wait a few minutes, then try again.",
};

const COULD_NOT_CHECK = "We couldn't check that code just now. Try again in a moment.";
const GENERIC = "Could not redeem that code.";

function sentence(text: string): string {
  const trimmed = text.trim();
  if (!trimmed) return trimmed;
  const capped = trimmed[0].toUpperCase() + trimmed.slice(1);
  return /[.!?]$/.test(capped) ? capped : `${capped}.`;
}

export function describeCouponError(err: unknown): string {
  const reason = errorReason(err);
  if (reason && BY_REASON[reason]) return BY_REASON[reason];

  const message =
    err instanceof ConnectError
      ? stripServerFraming(err.rawMessage || err.message)
      : err instanceof Error
        ? stripServerFraming(err.message)
        : "";

  // Per-org repeat: the server says whether it was once or the maximum number
  // of times, so its sentence is the better one.
  if (reason === "coupon_already_redeemed") {
    return message ? sentence(message) : "Your organization has already redeemed that coupon.";
  }

  if (err instanceof ConnectError) {
    // Nothing the user can act on: the server was unreachable, timed out, or
    // redacted an internal failure to a fixed string.
    if (
      err.code === Code.Unavailable ||
      err.code === Code.DeadlineExceeded ||
      err.code === Code.Internal ||
      err.code === Code.Unknown
    ) {
      return COULD_NOT_CHECK;
    }
    if (!reason) {
      // An older server, or a proxy, that sends no reason: the code still
      // narrows it down.
      if (err.code === Code.NotFound) return BY_REASON.coupon_not_found;
      if (err.code === Code.AlreadyExists) return "Your organization has already redeemed that coupon.";
    }
  } else if (err instanceof TypeError) {
    return COULD_NOT_CHECK;
  }

  return message ? sentence(message) : GENERIC;
}
