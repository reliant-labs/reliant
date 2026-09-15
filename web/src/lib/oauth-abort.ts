/**
 * Recognising a CANCELLED OAuth flow, so it is never reported as a failure.
 *
 * # Why this needs a helper rather than an `instanceof` at each call site
 *
 * An aborted flow surfaces as several unrelated error shapes, depending on how
 * far it got before the signal fired:
 *
 *   - `DOMException` named `AbortError` — a fetch severed by AbortController,
 *     the common case. Its message is the browser's, and Chrome's wording is
 *     "signal is aborted without reason", which reads to a user like a crash.
 *   - `TypeError` — a fetch torn down mid-flight can reject this way instead.
 *   - Anything at all — the flow also runs over the daemon gRPC path, where an
 *     abort becomes a transport error whose type and message name nothing about
 *     aborting.
 *
 * So the error alone cannot be trusted to identify the case. The SIGNAL is the
 * reliable witness: if it is aborted, the flow was cancelled, whatever the
 * rejection happened to look like.
 *
 * # Why this matters in practice
 *
 * Clicking Connect twice is a normal thing to do — the first tab was closed, or
 * the user changed their mind. `useCodexOAuth.start` aborts the previous run
 * before starting a new one, which is correct. But the abandoned run's
 * rejection still propagates, and without this check it was classified as
 * `daemon_error` and shown as a failure banner *while the second flow was
 * succeeding*. The user saw an error for the action that worked.
 */
export function isAbort(error: unknown, signal?: AbortSignal): boolean {
  if (signal?.aborted) return true
  if (error instanceof DOMException && error.name === 'AbortError') return true
  // Some runtimes reject with a plain Error carrying the name instead.
  return (
    typeof error === 'object' &&
    error !== null &&
    'name' in error &&
    (error as { name?: unknown }).name === 'AbortError'
  )
}
