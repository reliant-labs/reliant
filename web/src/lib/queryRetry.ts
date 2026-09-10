import { Code, ConnectError } from "@connectrpc/connect";

/**
 * The retry predicate shared by every React Query query in the app.
 *
 * It lives in its own module rather than inline in the QueryClient config so
 * it can be tested directly. That matters more than it sounds: the previous
 * version was inline, untested, and wrong for the entire transport this app
 * actually speaks.
 *
 * ── What was wrong ───────────────────────────────────────────────────────
 *
 * It asked whether the error MESSAGE contained "401" or "403". Almost nothing
 * here throws such a message. A failed Connect RPC surfaces as a ConnectError
 * whose message is the code's name plus the server's text:
 *
 *     [unauthenticated] missing authorization token
 *
 * No "401", no "403" — so the guard never fired and every unauthenticated
 * call was retried twice. On the sign-in screen, where by definition there is
 * no token, that turned each mount into a burst of three failing requests,
 * which is the background "polling with 401" users reported against
 * GetProviderStatuses while they were still entering their email code.
 *
 * The fix is to branch on the ConnectError CODE, which is the actual signal;
 * the rendered prose is a presentation detail that was never load-bearing.
 */

/**
 * Codes where a retry cannot change the answer, so retrying only adds latency.
 *
 * Unauthenticated / PermissionDenied are the auth pair — the credential will
 * not become valid between two immediate attempts. NotFound is here for the
 * same reason and is pure latency besides: an entity that does not exist will
 * not exist 200ms later, and re-asking twice delays every "no such thing"
 * empty state by the length of the backoff ladder.
 */
const NON_RETRYABLE_CONNECT_CODES: ReadonlySet<Code> = new Set([
  Code.Unauthenticated,
  Code.PermissionDenied,
  Code.NotFound,
]);

/** HTTP statuses that mean the same thing for any non-Connect error path. */
const NON_RETRYABLE_HTTP_STATUSES: ReadonlySet<number> = new Set([401, 403, 404]);

const MAX_RETRIES = 2;

/**
 * Pull an HTTP status off a plain (non-Connect) error.
 *
 * Kept because not everything reaching this predicate is a ConnectError —
 * `fetch` wrappers and third-party SDKs throw errors carrying `status` or
 * `statusCode`. It is a fallback, not the primary signal.
 */
function httpStatusOf(error: unknown): number | undefined {
  if (typeof error !== "object" || error === null) return undefined;
  const { status, statusCode } = error as {
    status?: unknown;
    statusCode?: unknown;
  };
  if (typeof status === "number") return status;
  if (typeof statusCode === "number") return statusCode;
  return undefined;
}

/**
 * Last-resort read of a status out of the message text.
 *
 * Bounded by word boundaries, unlike the substring test this replaces:
 * `"timed out after 4013ms"` contains "401" and was previously treated as an
 * auth failure, silently suppressing the retry of a genuinely transient
 * timeout.
 */
const HTTP_STATUS_IN_MESSAGE = /\b(401|403|404)\b/;

export function shouldRetryQuery(failureCount: number, error: unknown): boolean {
  if (error instanceof ConnectError) {
    if (NON_RETRYABLE_CONNECT_CODES.has(error.code)) return false;
    return failureCount < MAX_RETRIES;
  }

  const status = httpStatusOf(error);
  if (status !== undefined && NON_RETRYABLE_HTTP_STATUSES.has(status)) {
    return false;
  }

  if (
    error instanceof Error &&
    status === undefined &&
    HTTP_STATUS_IN_MESSAGE.test(error.message)
  ) {
    return false;
  }

  return failureCount < MAX_RETRIES;
}
