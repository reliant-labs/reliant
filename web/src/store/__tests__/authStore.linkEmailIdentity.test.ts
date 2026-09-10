/**
 * Attaching an email to an ANONYMOUS account — the call, and why it is not
 * `signUp`.
 *
 * The bug these pin: the form called `supabase.auth.signUp(email, password)`
 * and a comment claimed it "upgrades the anonymous user in place". It does
 * not. `signUp` POSTs to /signup, which mints a SEPARATE user (or returns one
 * with no session) and leaves the anonymous account — its org, its redeemed
 * coupons, its onboarding progress — untouched and still anonymous. The user
 * confirmed their address, came back, and hit the same "Finish setting up your
 * account" modal. Forever.
 *
 * Supabase upgrades an anonymous session IN PLACE via `updateUser`, which PUTs
 * /user with the current session's access token, so the email lands on that
 * user id and nothing is migrated.
 *
 * The assertions are written against the SUPABASE CLIENT, not against the
 * store's return value, because that is the only place the difference is
 * visible: both calls resolve, both set a user, and only the request tells you
 * whether the identity attached to the account the user is sitting in.
 */

import { beforeEach, describe, expect, it, vi } from 'vitest'

const updateUserMock = vi.fn()
const signUpMock = vi.fn()
const verifyOtpMock = vi.fn()
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
      signUp: signUpMock,
      verifyOtp: verifyOtpMock,
      resend: resendMock,
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

/** The anonymous account the user is sitting in — chats, org, coupons and all. */
const ANON_USER_ID = 'anon-user-0001'

const loadStore = async () => (await import('@/store/authStore')).useAuthStore

beforeEach(() => {
  vi.resetModules()
  vi.clearAllMocks()

  // updateUser echoes the SAME user id back, with the address still pending
  // confirmation (GoTrue holds it in new_email and leaves `email` unset).
  updateUserMock.mockResolvedValue({
    data: { user: { id: ANON_USER_ID, is_anonymous: true, new_email: 'someone@example.com' } },
    error: null,
  })
})

describe('linkEmailIdentity — upgrading an anonymous user in place', () => {
  it('calls updateUser, NOT signUp', async () => {
    const useAuthStore = await loadStore()

    await useAuthStore.getState().linkEmailIdentity('someone@example.com', 'Sup3rSecret!pass')

    expect(updateUserMock).toHaveBeenCalledTimes(1)
    // The whole bug in one assertion. signUp creates a second account and
    // strands the first; the two are indistinguishable from the UI.
    expect(signUpMock).not.toHaveBeenCalled()
  })

  it('sends the email and password in the update, so the identity attaches to the current user', async () => {
    const useAuthStore = await loadStore()

    await useAuthStore.getState().linkEmailIdentity('someone@example.com', 'Sup3rSecret!pass')

    const [attributes] = updateUserMock.mock.calls[0]
    expect(attributes).toEqual({
      email: 'someone@example.com',
      password: 'Sup3rSecret!pass',
    })
  })

  it('keeps the SAME user id — the account is upgraded, not replaced', async () => {
    const useAuthStore = await loadStore()

    const { user } = await useAuthStore
      .getState()
      .linkEmailIdentity('someone@example.com', 'Sup3rSecret!pass')

    // This is the property the org, the coupons and the onboarding progress
    // all hang off. A new id here means every one of them was left behind.
    expect(user?.id).toBe(ANON_USER_ID)
    expect(useAuthStore.getState().user?.id).toBe(ANON_USER_ID)
  })

  it('reports verification pending while the address is unconfirmed', async () => {
    const useAuthStore = await loadStore()

    const { verificationRequired } = await useAuthStore
      .getState()
      .linkEmailIdentity('someone@example.com', 'Sup3rSecret!pass')

    // The caller must NOT read the pre-existing anonymous session as success.
    // updateUser issues no new session, so "we have a session" is true both
    // before and after — which is exactly how the old code concluded it had
    // linked an account it had not touched.
    expect(verificationRequired).toBe(true)
  })

  it('reports linked immediately when the project has confirmations off', async () => {
    updateUserMock.mockResolvedValue({
      data: { user: { id: ANON_USER_ID, is_anonymous: false, email: 'someone@example.com' } },
      error: null,
    })
    const useAuthStore = await loadStore()

    const { verificationRequired } = await useAuthStore
      .getState()
      .linkEmailIdentity('someone@example.com', 'Sup3rSecret!pass')

    expect(verificationRequired).toBe(false)
  })

  it('points the emailed link at /auth/callback and carries returnTo through it', async () => {
    const useAuthStore = await loadStore()

    await useAuthStore
      .getState()
      .linkEmailIdentity('someone@example.com', 'Sup3rSecret!pass', {
        source: 'link',
        returnTo: '/onboarding?plan=eyJzdGVwIjoiY2hlY2tvdXQifQ',
      })

    const [, options] = updateUserMock.mock.calls[0]
    const redirect = new URL(options.emailRedirectTo)

    // `emailRedirectTo: undefined` was the old value, justified by "Electron
    // shouldn't redirect" — but the same code runs in a browser, where the
    // redirect IS the mechanism, and an unset one drops the user on the
    // Supabase Site URL instead of back in the flow.
    expect(redirect.pathname).toBe('/auth/callback')
    expect(redirect.searchParams.get('returnTo')).toBe(
      '/onboarding?plan=eyJzdGVwIjoiY2hlY2tvdXQifQ',
    )
    // Never the renderer's own origin: in packaged Electron that is an
    // ephemeral loopback port the mail client cannot reach.
    expect(redirect.origin).toBe('https://app.reliantlabs.io')
  })

  it('surfaces a taken address rather than signing the user into that account', async () => {
    updateUserMock.mockResolvedValue({
      data: { user: null },
      error: new Error('A user with this email address has already been registered'),
    })
    const useAuthStore = await loadStore()

    await expect(
      useAuthStore.getState().linkEmailIdentity('taken@example.com', 'Sup3rSecret!pass'),
    ).rejects.toThrow(/already been registered/)
  })
})

describe('verifyEmailIdentityOTP — the typed 6-digit code', () => {
  it("verifies with type 'email_change', which is where GoTrue put the token", async () => {
    verifyOtpMock.mockResolvedValue({
      data: {
        user: { id: ANON_USER_ID, is_anonymous: false, email: 'someone@example.com' },
        session: { access_token: 'fresh' },
      },
      error: null,
    })
    const useAuthStore = await loadStore()

    await useAuthStore.getState().verifyEmailIdentityOTP('123456', 'someone@example.com')

    // An anonymous user upgraded via updateUser has a pending EMAIL CHANGE, so
    // its one-time token lives in email_change_token_new. `type: 'signup'`
    // reads confirmation_token instead and reports "Token has expired or is
    // invalid" for a code that is perfectly valid.
    expect(verifyOtpMock).toHaveBeenCalledWith({
      email: 'someone@example.com',
      token: '123456',
      type: 'email_change',
    })
  })

  it('adopts the verified session, so the user is no longer anonymous', async () => {
    verifyOtpMock.mockResolvedValue({
      data: {
        user: { id: ANON_USER_ID, is_anonymous: false, email: 'someone@example.com' },
        session: { access_token: 'fresh' },
      },
      error: null,
    })
    const useAuthStore = await loadStore()

    await useAuthStore.getState().verifyEmailIdentityOTP('123456', 'someone@example.com')

    const state = useAuthStore.getState()
    // Same account, now permanent and carrying the email. This is the state
    // the checkout guard reads to stop demanding a real account.
    expect(state.user?.id).toBe(ANON_USER_ID)
    expect(state.user?.is_anonymous).toBe(false)
    expect(state.user?.email).toBe('someone@example.com')
    expect(state.session?.access_token).toBe('fresh')
  })
})

describe('sendEmailIdentityVerification', () => {
  it('re-sends through updateUser and keeps returnTo on the new link', async () => {
    const useAuthStore = await loadStore()

    await useAuthStore
      .getState()
      .sendEmailIdentityVerification('someone@example.com', {
        source: 'link',
        returnTo: '/onboarding?plan=abc',
      })

    // NOT supabase.auth.resend, with either type. GoTrue's Resend handler
    // finds the account via FindUserByEmailAndAudience — `WHERE LOWER(email)`
    // — and a user mid-upgrade has an EMPTY users.email, their address being
    // parked in users.email_change. So the lookup misses and GoTrue answers
    // 200 with an empty body, having sent nothing. That is the "resend
    // returns 200 but no email arrives" symptom, and swapping 'signup' for
    // 'email_change' does not change it: the lookup fails before the type is
    // consulted. updateUser resolves the user from the session token instead.
    expect(resendMock).not.toHaveBeenCalled()

    const [attributes, options] = updateUserMock.mock.calls[0]
    expect(attributes).toEqual({ email: 'someone@example.com' })
    // No password: re-sending it would hit GoTrue's password branch and fail
    // with 422 same_password before any mail was sent.
    expect(attributes).not.toHaveProperty('password')
    // A resent link that dropped returnTo would land an onboarding user at
    // step one with every answer gone — the same dead end, one email later.
    expect(
      new URL(options.emailRedirectTo).searchParams.get('returnTo'),
    ).toBe('/onboarding?plan=abc')
  })
})
