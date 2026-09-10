/**
 * The two store calls behind the primary sign-in, pinned at the SUPABASE
 * CLIENT — because that request is the only place their correctness is
 * visible.
 *
 * Two things can be wrong here in ways no component test would catch:
 *
 * 1. `shouldCreateUser`. If it were false, an unknown address would fail
 *    instead of being enrolled, and the failure IS the enumeration oracle the
 *    whole design exists to avoid. It is not a UX toggle.
 * 2. The verify `type`. GoTrue resolves an OTP from a different column per
 *    type — confirmation_token for 'signup', recovery_token for 'magiclink' —
 *    and this flow cannot know which population the user is in. 'email' is the
 *    type paired with signInWithOtp and matches either token. Getting it wrong
 *    reports "expired or invalid" for a code that is perfectly good, which is
 *    indistinguishable from mail trouble and was already lost an evening to
 *    once (as an OTP-length mismatch).
 */

import { beforeEach, describe, expect, it, vi } from 'vitest'

const signInWithOtpMock = vi.fn()
const verifyOtpMock = vi.fn()
const getSessionMock = vi.fn(async () => ({ data: { session: null }, error: null }))
const onAuthStateChangeMock = vi.fn(() => ({
  data: { subscription: { unsubscribe: vi.fn() } },
}))

vi.mock('@/lib/supabase', () => ({
  supabase: {
    auth: {
      getSession: getSessionMock,
      onAuthStateChange: onAuthStateChangeMock,
      signInWithOtp: signInWithOtpMock,
      verifyOtp: verifyOtpMock,
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

vi.mock('@/lib/query-client', () => ({ queryClient: { clear: vi.fn() } }))

const { useAuthStore } = await import('@/store/authStore')

const SESSION = { access_token: 'fresh' }
const USER = { id: 'u1', email: 'someone@example.com', is_anonymous: false }

beforeEach(() => {
  vi.clearAllMocks()
  // The store is a module singleton, so a session adopted by an earlier case
  // is still there for the next one. Reset it, or "no session was set" passes
  // for the wrong reason and fails for the wrong reason.
  useAuthStore.setState({ user: null, session: null })
  signInWithOtpMock.mockResolvedValue({ data: {}, error: null })
  verifyOtpMock.mockResolvedValue({
    data: { user: USER, session: SESSION },
    error: null,
  })
})

describe('sendEmailSignInCode', () => {
  it('enrolls an unknown address rather than failing on it', async () => {
    await useAuthStore.getState().sendEmailSignInCode('someone@example.com')

    const [args] = signInWithOtpMock.mock.calls[0]
    expect(args.email).toBe('someone@example.com')
    // The anti-enumeration property in one flag: a known and an unknown
    // address take the identical path, so neither the response nor the UI can
    // tell them apart.
    expect(args.options.shouldCreateUser).toBe(true)
  })

  it('points the magic link at the app callback, not the project default', async () => {
    await useAuthStore.getState().sendEmailSignInCode('someone@example.com')

    const [args] = signInWithOtpMock.mock.calls[0]
    // The same email carries a clickable link. Without emailRedirectTo GoTrue
    // falls back to the project's site URL, which lands somewhere that cannot
    // complete a sign-in — so the code would work and the link would not.
    expect(args.options.emailRedirectTo).toContain('/auth/callback')
  })

  it('threads returnTo onto the link so the flow resumes where it left off', async () => {
    await useAuthStore
      .getState()
      .sendEmailSignInCode('someone@example.com', { returnTo: '/onboarding?plan=abc' })

    const [args] = signInWithOtpMock.mock.calls[0]
    expect(args.options.emailRedirectTo).toContain(
      `returnTo=${encodeURIComponent('/onboarding?plan=abc')}`,
    )
  })

  it('surfaces a send failure instead of reporting a code that was never mailed', async () => {
    signInWithOtpMock.mockResolvedValue({
      data: {},
      error: Object.assign(new Error('rate limited'), {
        code: 'over_email_send_rate_limit',
      }),
    })

    await expect(
      useAuthStore.getState().sendEmailSignInCode('someone@example.com'),
    ).rejects.toThrow(/rate limited/)
  })
})

describe('verifyEmailSignInCode', () => {
  it("verifies with type 'email', which matches either population's token", async () => {
    await useAuthStore.getState().verifyEmailSignInCode('someone@example.com', '123456')

    expect(verifyOtpMock).toHaveBeenCalledWith({
      email: 'someone@example.com',
      token: '123456',
      // Not 'signup' (new accounts only) and not 'magiclink' (existing only).
      // The screen deliberately cannot know which one the user is.
      type: 'email',
    })
  })

  it('adopts the returned session so the user is actually signed in', async () => {
    await useAuthStore.getState().verifyEmailSignInCode('someone@example.com', '123456')

    expect(useAuthStore.getState().user).toEqual(USER)
    expect(useAuthStore.getState().session).toEqual(SESSION)
  })

  it('throws on a bad code without half-setting a session', async () => {
    verifyOtpMock.mockResolvedValue({
      data: { user: null, session: null },
      error: Object.assign(new Error('Token has expired or is invalid'), {
        code: 'otp_expired',
      }),
    })

    await expect(
      useAuthStore.getState().verifyEmailSignInCode('someone@example.com', '000000'),
    ).rejects.toThrow(/expired/)
    expect(useAuthStore.getState().session).toBeNull()
  })
})
