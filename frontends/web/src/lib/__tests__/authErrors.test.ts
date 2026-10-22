/**
 * Turning GoTrue's error codes into something a stuck user can act on.
 *
 * Every string pinned here was, at some point, shown to the owner verbatim as
 * a raw Supabase code. `pkce_code_verifier_not_found` and
 * `over_email_send_rate_limit` are not messages — they are the absence of one.
 */

import { describe, expect, it } from 'vitest'

import { describeAuthError, retryAfterSeconds } from '../authErrors'

/** Shape of a real supabase-js AuthError: message + code, sometimes status. */
const authError = (code: string, message: string, status?: number) =>
  Object.assign(new Error(message), { code, status })

describe('retryAfterSeconds', () => {
  it('reads the wait out of GoTrue\u2019s frequency-limit message', () => {
    // generateFrequencyLimitErrorMessage renders exactly this shape.
    expect(
      retryAfterSeconds('For security purposes, you can only request this after 54 seconds.'),
    ).toBe(54)
  })

  it('returns null when the message carries no wait', () => {
    expect(retryAfterSeconds('email rate limit exceeded')).toBeNull()
  })
})

describe('describeAuthError \u2014 the rate limit becomes a wait, not a code', () => {
  it('renders over_email_send_rate_limit with the number of seconds to wait', () => {
    const message = describeAuthError(
      authError(
        'over_email_send_rate_limit',
        'For security purposes, you can only request this after 54 seconds.',
        429,
      ),
      'fallback',
    )

    expect(message).toMatch(/54 seconds/)
    // The raw code must never reach the user.
    expect(message).not.toMatch(/over_email_send_rate_limit/)
  })

  it('still explains the limit when GoTrue gives no number', () => {
    const message = describeAuthError(
      authError('over_email_send_rate_limit', 'email rate limit exceeded', 429),
      'fallback',
    )

    expect(message).toMatch(/too many/i)
    expect(message).not.toMatch(/over_email_send_rate_limit/)
  })
})

describe('describeAuthError \u2014 the PKCE dead end becomes an instruction', () => {
  it('explains that a link opened in another browser cannot complete', () => {
    // The owner's actual log line. A code verifier lives in the browser that
    // STARTED the flow; the link was clicked in a mail client. Structural.
    const message = describeAuthError(
      authError('pkce_code_verifier_not_found', 'code verifier not found'),
      'fallback',
    )

    expect(message).not.toMatch(/pkce_code_verifier_not_found/)
    expect(message).toMatch(/code/i)
  })

  it('treats a validation failure on the verifier the same way', () => {
    const message = describeAuthError(
      authError('validation_failed', 'invalid request: both auth code and code verifier should be non-empty'),
      'fallback',
    )

    expect(message).not.toMatch(/code verifier should be non-empty/)
  })
})

describe('describeAuthError \u2014 the rest', () => {
  it('maps a taken address to the "already attached" copy', () => {
    expect(
      describeAuthError(
        authError('email_exists', 'A user with this email address has already been registered'),
        'fallback',
      ),
    ).toMatch(/already attached to another account/i)
  })

  it('maps an expired one-time token to a request-a-new-one message', () => {
    expect(
      describeAuthError(authError('otp_expired', 'Token has expired or is invalid'), 'fallback'),
    ).toMatch(/expired/i)
  })

  it('falls back to the error message for anything unrecognised', () => {
    expect(describeAuthError(new Error('Something specific went wrong'), 'fallback')).toBe(
      'Something specific went wrong',
    )
  })

  it('falls back to the caller\u2019s copy for a non-error', () => {
    expect(describeAuthError(undefined, 'Could not save your account')).toBe(
      'Could not save your account',
    )
  })
})
