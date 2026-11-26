// Copyright (c) 2025 Reliant Labs

/**
 * The Supabase client's fetch, with a deadline on the token refresh.
 *
 * Every RPC waits behind the auth interceptor, which waits on getSession(),
 * which waits on a due refresh. supabase-js gives that request no timeout, so
 * a refresh POST stranded on a dead socket (an iPhone waking up) held every
 * RPC and the chat stream for 38-51s in prod, and could hold them forever.
 *
 * The deadline is generous: Mobile Safari's refresh p90 is about 20s, so a
 * slow refresh that is going to succeed does so within one attempt. A refresh
 * that runs past it is aborted and sent once more, on a fresh connection;
 * past the second deadline the error surfaces (supabase-js keeps the session
 * on a fetch failure and tries again on its next tick). Both attempts fit
 * supabase-js's own 30s retry window, so it does not add a third.
 *
 * Why resending is safe (checked 2026-11-26). Both projects, prod (custom
 * domain dash.reliantlabs.io) and nonprod, are hosted Supabase running
 * GoTrue v2.199.0 (GET /auth/v1/health). Rotation and the reuse interval are
 * dashboard settings that only the Management API reads (hosted defaults:
 * rotation on, 10s). They do not decide this case:
 *
 *  - The resend is the same request, so it carries the same refresh token.
 *    If the first attempt reached the server, that token is now exactly ONE
 *    rotation behind the session's active one. v2.199.0 accepts that at any
 *    age and answers with the active token, without rotating again: a v1
 *    token that is the active token's parent, or a v2 token whose counter is
 *    one behind ("fail-to-save"; supabase/auth internal/tokens/service.go,
 *    RefreshTokenGrant). The reuse interval, and revoking the token family
 *    (or logging out the session), apply only to a token two or more
 *    rotations behind.
 *  - Only a client that received and stored the newer token could send one
 *    that far behind. That client is not this one: a resend happens only
 *    when no response reached the caller, so the caller still holds the
 *    token it sent.
 *  - A client-side window cannot keep the resend inside 10s anyway. On a
 *    fetch failure supabase-js resends the same token itself, up to 30s
 *    after the first try, and again on its next refresh tick. Dropping this
 *    retry would only move the resend later.
 *
 * This rests on GoTrue's one-behind rule, which v2.100.0 lacks (v2.150.0 has
 * it). On a GoTrue without it, supabase-js's own resends would be just as
 * unsafe.
 *
 * Only the refresh grant is bounded. Other auth requests (sign-in, OTP,
 * sign-out) pass through untouched: they are user-initiated, and resending one
 * is not always harmless (a second OTP email).
 */

export const REFRESH_ATTEMPT_TIMEOUT_MS = 20_000;
export const REFRESH_ATTEMPTS = 2;

export function isTokenRefreshRequest(input: RequestInfo | URL, init?: RequestInit): boolean {
  const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
  const method = (init?.method ?? (typeof input === "object" && "method" in input ? input.method : "GET")).toUpperCase();
  if (method !== "POST") return false;
  try {
    const parsed = new URL(url, "http://localhost");
    return parsed.pathname.endsWith("/token") && parsed.searchParams.get("grant_type") === "refresh_token";
  } catch {
    return false;
  }
}

export class TokenRefreshTimeoutError extends Error {
  constructor(attempts: number, timeoutMs: number) {
    super(`Token refresh did not answer within ${timeoutMs}ms (${attempts} attempts)`);
    this.name = "TokenRefreshTimeoutError";
  }
}

export function withTokenRefreshTimeout(
  base: typeof fetch,
  { timeoutMs = REFRESH_ATTEMPT_TIMEOUT_MS, attempts = REFRESH_ATTEMPTS }: { timeoutMs?: number; attempts?: number } = {},
): typeof fetch {
  return async (input, init) => {
    if (!isTokenRefreshRequest(input, init)) return base(input, init);
    const callerSignal = init?.signal ?? (input instanceof Request ? input.signal : undefined);
    for (let attempt = 1; ; attempt++) {
      try {
        return await boundedFetch(base, input, init, callerSignal, timeoutMs);
      } catch (error) {
        if (!(error instanceof AttemptTimedOut) || callerSignal?.aborted) throw error;
        if (attempt >= attempts) throw new TokenRefreshTimeoutError(attempt, timeoutMs);
      }
    }
  };
}

class AttemptTimedOut extends Error {}

const NULL_BODY_STATUSES = new Set([101, 204, 205, 304]);

/** One attempt, its body read in full under the deadline (a stalled body hangs as well as stalled headers). */
async function boundedFetch(
  base: typeof fetch,
  input: RequestInfo | URL,
  init: RequestInit | undefined,
  callerSignal: AbortSignal | undefined,
  timeoutMs: number,
): Promise<Response> {
  callerSignal?.throwIfAborted();
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(new AttemptTimedOut()), timeoutMs);
  const onCallerAbort = (): void => controller.abort(callerSignal?.reason);
  callerSignal?.addEventListener("abort", onCallerAbort);
  let onAbort = (): void => {};
  const aborted = new Promise<never>((_resolve, reject) => {
    onAbort = () => reject(controller.signal.reason);
    controller.signal.addEventListener("abort", onAbort);
  });
  try {
    // Aborting fetch is best effort. Settle independently even when the
    // transport or its body reader does not respond to the signal.
    return await Promise.race([
      aborted,
      (async () => {
        const response = await base(input, { ...init, signal: controller.signal });
        const body = NULL_BODY_STATUSES.has(response.status) ? null : await response.arrayBuffer();
        return new Response(body, { status: response.status, statusText: response.statusText, headers: response.headers });
      })(),
    ]);
  } finally {
    clearTimeout(timer);
    callerSignal?.removeEventListener("abort", onCallerAbort);
    controller.signal.removeEventListener("abort", onAbort);
  }
}
