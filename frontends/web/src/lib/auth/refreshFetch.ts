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
 * Resending is safe: Supabase Auth answers a refresh token that is the parent
 * of the session's active one with that active token, so a retry after the
 * first attempt's response was lost gets the tokens that attempt rotated to.
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
    const callerSignal = init?.signal ?? undefined;
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
  const controller = new AbortController();
  let timedOut = false;
  const timer = setTimeout(() => {
    timedOut = true;
    controller.abort();
  }, timeoutMs);
  const onCallerAbort = (): void => controller.abort(callerSignal?.reason);
  if (callerSignal?.aborted) controller.abort(callerSignal.reason);
  callerSignal?.addEventListener("abort", onCallerAbort);
  try {
    const response = await base(input, { ...init, signal: controller.signal });
    const body = NULL_BODY_STATUSES.has(response.status) ? null : await response.arrayBuffer();
    return new Response(body, { status: response.status, statusText: response.statusText, headers: response.headers });
  } catch (error) {
    if (timedOut) throw new AttemptTimedOut();
    throw error;
  } finally {
    clearTimeout(timer);
    callerSignal?.removeEventListener("abort", onCallerAbort);
  }
}
