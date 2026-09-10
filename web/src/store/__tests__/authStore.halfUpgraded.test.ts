/**
 * The half-upgraded account: password set, email never verified, no way out.
 *
 * This is the state the owner is in RIGHT NOW, and it is produced by the
 * happy path itself. `linkEmailIdentity` sent `{ email, password }` in one
 * `updateUser` call. GoTrue's UserUpdate handler processes the password FIRST
 * and the email SECOND, in that order, in one transaction:
 *
 *   1. password  -> user.SetPassword + UpdatePassword   (applied immediately)
 *   2. email     -> sendEmailChange                     (leaves it PENDING)
 *
 * So a first attempt that half-lands writes the password and leaves the
 * address in `email_change`. On a RETRY with the same password, GoTrue reaches
 * step 1, finds `isSamePassword`, and returns
 *
 *   422 same_password  "New password should be different from the old password."
 *
 * BEFORE EVER REACHING STEP 2. No email is sent. The user is told to pick a
 * different password for an account they do not believe they have, and there
 * is no way forward. That is the trap.
 *
 * ── Why the retry cannot simply send the password on its own ──────────────
 *
 * It cannot. GoTrue explicitly refuses a password-only update on an anonymous
 * user:
 *
 *   if user.IsAnonymous && params.Password != "" &&
 *      params.Email == "" && params.Phone == "" -> 422 validation_failed
 *
 * So the FIRST attempt genuinely must send both together. The fix is not to
 * reorder the first call \u2014 it is to make the RETRY send the email ALONE, which
 * skips the password branch entirely and re-enters sendEmailChange.
 *
 * ── Why the retry cannot use supabase.auth.resend() ───────────────────────
 *
 * This is the part the original diagnosis got wrong, and it matters. GoTrue's
 * Resend handler resolves the user with
 *
 *   models.FindUserByEmailAndAudience(db, params.Email, aud)
 *     -> "LOWER(email) = ?"      i.e. the users.EMAIL column
 *
 * An anonymous user mid-upgrade has an EMPTY users.email \u2014 the pending address
 * lives in users.email_change. So looking the user up BY THE PENDING ADDRESS
 * misses, hits IsNotFoundError, and returns `200 {}` having sent nothing.
 *
 * That is true for `type: 'signup'` AND for `type: 'email_change'`. Both
 * resend variants are silently dead for this user; "returns 200 but no email
 * arrives" is the expected output of either. Only `updateUser({ email })`
 * re-enters sendEmailChange, because UserUpdate resolves the user from the
 * session's access token rather than from an email lookup.
 */

import { beforeEach, describe, expect, it, vi } from 'vitest'

const updateUserMock = vi.fn()
const resendMock = vi.fn()
const getSessionMock = vi.fn(async () => ({ data: { session: null }, error: null }))
const onAuthStateChangeMock = vi.fn(() => ({
  data: { subscription: { unsubscribe: vi.fn() } },
}))

vi.mock('@/lib/supabase', () => ({
  supabase: {
    auth: {
      getSession: getSessionMock,
      onAuthStateChange: onAuthStateChangeMock,
      updateUser: updateUserMock,
      resend: resendMock,
      verifyOtp: vi.fn(),
      signUp: vi.fn(),
    },
  },
  setAuthStorageUnreadableHandler: vi.fn(),
}))

vi.mock('@/api/grpc-unauth', () => ({
  devAuthGrpc: {
    load: vi.fn(async () => ({ success: false })),
    save: vi.fn(async () => ({ success: true })),
    clear: vi.fn(async () => ({ success: true })),
  },
}))

vi.mock('@/lib/logger', () => ({
  logger: { info: vi.fn(), error: vi.fn(), warn: vi.fn(), debug: vi.fn() },
}))

vi.mock('@/lib/sentry', () => ({ setSentryUser: vi.fn() }))

vi.mock('@/lib/constants', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/lib/constants')>()),
  getIsDev: () => false,
  getAppURL: () => 'https://app.reliantlabs.io',
}))

const ANON_USER_ID = 'anon-user-0001'
const EMAIL = 'someone@example.com'
const PASSWORD = 'Sup3rSecret!pass'
const ONBOARDING_URL = '/onboarding?plan=eyJzdGVwIjoiY2hlY2tvdXQifQ'

/** GoTrue's 422 when the submitted password equals the stored one. */
const samePasswordError = Object.assign(
  new Error('New password should be different from the old password.'),
  { code: 'same_password', status: 422 },
)

/** The user as GoTrue returns it mid-upgrade: address pending in new_email. */
const pendingUser = {
  id: ANON_USER_ID,
  is_anonymous: true,
  email: undefined,
  new_email: EMAIL,
}

const loadStore = async () => (await import('@/store/authStore')).useAuthStore

beforeEach(() => {
  vi.resetModules()
  vi.clearAllMocks()
  updateUserMock.mockResolvedValue({ data: { user: pendingUser }, error: null })
  resendMock.mockResolvedValue({ error: null })
})

describe('linkEmailIdentity \u2014 a retry must not dead-end on same_password', () => {
  it('sends email and password together on the FIRST attempt', async () => {
    const useAuthStore = await loadStore()

    await useAuthStore.getState().linkEmailIdentity(EMAIL, PASSWORD)

    // Forced by GoTrue: a password-only update on an anonymous user is a 422.
    expect(updateUserMock).toHaveBeenCalledTimes(1)
    expect(updateUserMock.mock.calls[0][0]).toEqual({ email: EMAIL, password: PASSWORD })
  })

  it('treats same_password as "already set" and continues to verification', async () => {
    updateUserMock
      .mockResolvedValueOnce({ data: { user: null }, error: samePasswordError })
      .mockResolvedValueOnce({ data: { user: pendingUser }, error: null })

    const useAuthStore = await loadStore()

    // The trap: this used to reject, and the user was told to choose a
    // different password for an account they did not know they had.
    const { verificationRequired } = await useAuthStore
      .getState()
      .linkEmailIdentity(EMAIL, PASSWORD)

    expect(verificationRequired).toBe(true)
  })

  it('retries with the email ALONE, so GoTrue never re-enters the password branch', async () => {
    updateUserMock
      .mockResolvedValueOnce({ data: { user: null }, error: samePasswordError })
      .mockResolvedValueOnce({ data: { user: pendingUser }, error: null })

    const useAuthStore = await loadStore()
    await useAuthStore.getState().linkEmailIdentity(EMAIL, PASSWORD)

    expect(updateUserMock).toHaveBeenCalledTimes(2)
    // No `password` key at all. Including it — even the same value — returns
    // 422 before sendEmailChange runs, which is why no email ever arrived.
    expect(updateUserMock.mock.calls[1][0]).toEqual({ email: EMAIL })
  })

  it('keeps returnTo on the retry\u2019s emailed link', async () => {
    updateUserMock
      .mockResolvedValueOnce({ data: { user: null }, error: samePasswordError })
      .mockResolvedValueOnce({ data: { user: pendingUser }, error: null })

    const useAuthStore = await loadStore()
    await useAuthStore
      .getState()
      .linkEmailIdentity(EMAIL, PASSWORD, { source: 'link', returnTo: ONBOARDING_URL })

    const redirect = new URL(updateUserMock.mock.calls[1][1].emailRedirectTo)
    // A recovery that drops returnTo lands an onboarding user at step one with
    // every answer gone — the same dead end, one email later.
    expect(redirect.searchParams.get('returnTo')).toBe(ONBOARDING_URL)
    expect(redirect.pathname).toBe('/auth/callback')
  })

  it('still surfaces a taken address rather than retrying into it', async () => {
    updateUserMock.mockResolvedValueOnce({
      data: { user: null },
      error: Object.assign(
        new Error('A user with this email address has already been registered'),
        { code: 'email_exists', status: 422 },
      ),
    })

    const useAuthStore = await loadStore()

    await expect(
      useAuthStore.getState().linkEmailIdentity('taken@example.com', PASSWORD),
    ).rejects.toThrow(/already been registered/)
    // Only same_password earns a second call.
    expect(updateUserMock).toHaveBeenCalledTimes(1)
  })

  it('propagates the rate limit from the recovery attempt', async () => {
    updateUserMock
      .mockResolvedValueOnce({ data: { user: null }, error: samePasswordError })
      .mockResolvedValueOnce({
        data: { user: null },
        error: Object.assign(
          new Error('For security purposes, you can only request this after 54 seconds.'),
          { code: 'over_email_send_rate_limit', status: 429 },
        ),
      })

    const useAuthStore = await loadStore()

    await expect(
      useAuthStore.getState().linkEmailIdentity(EMAIL, PASSWORD),
    ).rejects.toMatchObject({ code: 'over_email_send_rate_limit' })
  })

  it('exposes the pending address so the UI can resume without starting over', async () => {
    const useAuthStore = await loadStore()

    const { user } = await useAuthStore.getState().linkEmailIdentity(EMAIL, PASSWORD)

    // GoTrue serialises users.email_change as `new_email`. This is the only
    // signal that an upgrade is half-finished.
    expect(user?.new_email).toBe(EMAIL)
    expect(useAuthStore.getState().user?.new_email).toBe(EMAIL)
  })
})

describe('sendEmailIdentityVerification \u2014 the only resend that actually sends', () => {
  it('re-sends via updateUser, NOT supabase.auth.resend', async () => {
    const useAuthStore = await loadStore()

    await useAuthStore.getState().sendEmailIdentityVerification(EMAIL)

    // resend() resolves the user by users.email, which is EMPTY mid-upgrade.
    // It answers 200 with an empty body and sends nothing — the owner's
    // "resend button returns 200 but no email arrives", exactly.
    expect(resendMock).not.toHaveBeenCalled()
    expect(updateUserMock).toHaveBeenCalledTimes(1)
    expect(updateUserMock.mock.calls[0][0]).toEqual({ email: EMAIL })
  })

  it('carries returnTo so the resent link lands where the first one would have', async () => {
    const useAuthStore = await loadStore()

    await useAuthStore
      .getState()
      .sendEmailIdentityVerification(EMAIL, { source: 'link', returnTo: ONBOARDING_URL })

    const redirect = new URL(updateUserMock.mock.calls[0][1].emailRedirectTo)
    expect(redirect.searchParams.get('returnTo')).toBe(ONBOARDING_URL)
    expect(redirect.origin).toBe('https://app.reliantlabs.io')
  })

  it('surfaces the rate limit instead of reporting a send that did not happen', async () => {
    updateUserMock.mockResolvedValueOnce({
      data: { user: null },
      error: Object.assign(
        new Error('For security purposes, you can only request this after 54 seconds.'),
        { code: 'over_email_send_rate_limit', status: 429 },
      ),
    })

    const useAuthStore = await loadStore()

    await expect(
      useAuthStore.getState().sendEmailIdentityVerification(EMAIL),
    ).rejects.toMatchObject({ code: 'over_email_send_rate_limit' })
  })
})
