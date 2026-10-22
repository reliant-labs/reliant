/**
 * GoTrue error codes → something a stuck user can act on.
 *
 * Every mapping here replaces a raw code that was, at some point, rendered to
 * the owner verbatim. `pkce_code_verifier_not_found` and
 * `over_email_send_rate_limit` are not messages; they are the absence of one,
 * and both appeared on screen during the session that produced this file.
 *
 * Kept separate from the store so the copy is testable without mounting a
 * component, and shared so two surfaces cannot phrase the same failure
 * differently — which is how a user starts wondering which one is true.
 */

/** The shape supabase-js actually throws: an Error carrying `code`/`status`. */
type MaybeAuthError = { code?: string; message?: string; status?: number } | null | undefined

const codeOf = (error: unknown): string | undefined =>
  typeof error === 'object' && error !== null
    ? (error as MaybeAuthError)?.code
    : undefined

const messageOf = (error: unknown): string =>
  error instanceof Error
    ? error.message
    : typeof error === 'object' && error !== null
      ? ((error as MaybeAuthError)?.message ?? '')
      : ''

/**
 * The wait GoTrue quotes in a frequency-limit message.
 *
 * Its generateFrequencyLimitErrorMessage renders "For security purposes, you
 * can only request this after N seconds." The number is the only actionable
 * thing in the whole response, so it is worth digging out rather than showing
 * the sentence — or, worse, the code — as-is.
 */
export const retryAfterSeconds = (message: string): number | null => {
  const match = message.match(/after\s+(\d+)\s+seconds?/i)
  if (!match) return null
  const seconds = Number(match[1])
  return Number.isFinite(seconds) ? seconds : null
}

/**
 * True when the failure is "wait and try again" rather than "you did something
 * wrong". Callers use it to start a cooldown instead of showing a red error.
 */
export const isRateLimitError = (error: unknown): boolean => {
  if (codeOf(error) === 'over_email_send_rate_limit') return true
  const lower = messageOf(error).toLowerCase()
  return (
    lower.includes('rate limit') ||
    lower.includes('security purposes') ||
    /after\s+\d+\s+seconds?/.test(lower)
  )
}

/**
 * True when a `code` in the callback URL cannot possibly be exchanged here.
 *
 * PKCE stores the code verifier in the browser that STARTED the flow. An email
 * link is clicked in a mail client, which opens a different browser context
 * with no verifier, so exchangeCodeForSession can never succeed — it is
 * structural, not a misconfiguration and not a transient failure. The user's
 * own log shows exactly this, and the app rendered the bare code at them.
 */
export const isPkceVerifierMissing = (error: unknown): boolean => {
  const code = codeOf(error)
  if (code === 'pkce_code_verifier_not_found') return true
  const lower = messageOf(error).toLowerCase()
  return (
    lower.includes('code verifier') ||
    lower.includes('code_verifier') ||
    lower.includes('pkce')
  )
}

const RATE_LIMITED_GENERIC =
  'We’ve sent too many emails to this address for the moment. Please wait a minute and try again — you don’t need to start over.'

const PKCE_DEAD_END =
  'This confirmation link can’t be completed in this browser, because it was opened somewhere other than where you started. Enter the 6-digit code from the same email instead — it works anywhere.'

/**
 * The single place a GoTrue failure becomes user-facing copy.
 *
 * `fallback` is what to say when nothing matches and the error carries no
 * message of its own. An error WITH a message falls through to it verbatim:
 * GoTrue's own wording is usually clearer than a generic apology, and
 * swallowing it makes bug reports unreadable.
 */
export const describeAuthError = (error: unknown, fallback: string): string => {
  const code = codeOf(error)
  const message = messageOf(error)

  if (isRateLimitError(error)) {
    const seconds = retryAfterSeconds(message)
    return seconds === null
      ? RATE_LIMITED_GENERIC
      : `We’ve sent too many emails to this address for the moment. Please wait ${seconds} seconds and try again — you don’t need to start over.`
  }

  if (isPkceVerifierMissing(error)) {
    return PKCE_DEAD_END
  }

  if (
    code === 'email_exists' ||
    message.includes('already been registered') ||
    message.includes('already registered') ||
    message.includes('already in use')
  ) {
    return 'That email is already attached to another account. Sign in with it instead, or use a different email.'
  }

  if (code === 'otp_expired' || message.includes('expired')) {
    return 'That confirmation code or link has expired. Send yourself a new one below.'
  }

  if (code === 'otp_disabled' || message.toLowerCase().includes('invalid')) {
    return 'That confirmation code didn’t match. Check the code in your email and try again.'
  }

  return message.length > 0 ? message : fallback
}
