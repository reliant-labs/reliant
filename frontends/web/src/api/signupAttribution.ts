// Copyright (c) 2025 Reliant Labs

import { Code, ConnectError, type Interceptor } from "@connectrpc/connect";
import { dropSignupAttribution, readSignupAttribution } from "@/lib/landingAttribution";

/** The header control-plane reads first-touch signup attribution from (internal/signupattr). */
export const SIGNUP_ATTRIBUTION_HEADER = "X-Reliant-Attribution";

// Set when a request carrying the header failed before reaching the server: a
// control plane that does not allow the header yet refuses the CORS preflight.
// For the rest of the page load the header is not sent; the hold stays for the
// next one.
let refusedThisPageLoad = false;

/**
 * Sends the landing site's held handoff (utm_*, referrer host, entry page, rid)
 * to the control plane on signed-in requests, until one succeeds. The account is
 * created by whichever signed-in request reaches the control plane first, and
 * that request's header becomes the account's first-touch attribution and the
 * props of its user_signed_up event; a request for an account that already
 * exists changes nothing. So once one signed-in request has succeeded the copy
 * has done its work (or had none to do) and is dropped
 * (lib/landingAttribution.ts, SIGNUP ATTRIBUTION).
 *
 * It can never cost a request: one that fails as a network failure (the
 * browser's refusal of a preflight looks like one) is sent again without the
 * header, and the header is not sent again this page load.
 *
 * Unary requests only, like orgInterceptor: the web streams nothing from the
 * control plane.
 */
export const signupAttributionInterceptor: Interceptor = (next) => async (req) => {
  const held = req.stream || refusedThisPageLoad ? undefined : readSignupAttribution();
  if (!held) {
    // Header-free means absent: an earlier attempt with this same request
    // object (a retry above this interceptor) may have left the header on it.
    req.header.delete(SIGNUP_ATTRIBUTION_HEADER);
    return await next(req);
  }
  req.header.set(SIGNUP_ATTRIBUTION_HEADER, held);
  try {
    const res = await next(req);
    dropSignupAttribution();
    return res;
  } catch (err) {
    if (!isNetworkFailure(err)) throw err;
    refusedThisPageLoad = true;
    req.header.delete(SIGNUP_ATTRIBUTION_HEADER);
    try {
      return await next(req);
    } finally {
      // Whatever the header-free retry did, the request leaves without it.
      req.header.delete(SIGNUP_ATTRIBUTION_HEADER);
    }
  }
};

// A failure that never reached the server: fetch rejected (connect-web reports
// it as Unknown or Unavailable, wrapping the TypeError).
function isNetworkFailure(err: unknown): boolean {
  if (err instanceof TypeError) return true;
  const ce = ConnectError.from(err);
  return (ce.code === Code.Unknown || ce.code === Code.Unavailable) && ce.cause instanceof TypeError;
}

/** Test seam. */
export function resetSignupAttributionState(): void {
  refusedThisPageLoad = false;
}
