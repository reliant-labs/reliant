import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { setRuntimeConfig } from '@/test/runtimeConfig'
import { resetShellForTests } from '@/lib/shell'

/**
 * A claim started on the forge host must finish on the forge host.
 *
 * A claim link (`https://forge.reliantlabs.io/claim#rcl_…`) parks its token in
 * localStorage, which is per ORIGIN. Upgrading the anonymous account through a
 * provider leaves the page and returns to `<redirect origin>/auth/callback`,
 * which then follows `returnTo=/claim`. If that origin were app.reliantlabs.io
 * (the shared bundle's configured APP_URL) the claim token would be on the
 * wrong origin and the claim would be lost. The forge shell therefore returns
 * to its own origin; the desktop app keeps the configured public origin.
 */

const signInWithOAuthMock = vi.fn()
const linkIdentityMock = vi.fn()
const updateUserMock = vi.fn()

vi.mock('@/lib/supabase', () => ({
  supabase: {
    auth: {
      getSession: vi.fn(async () => ({ data: { session: null }, error: null })),
      onAuthStateChange: vi.fn(() => ({ data: { subscription: { unsubscribe: vi.fn() } } })),
      signInWithOAuth: signInWithOAuthMock,
      linkIdentity: linkIdentityMock,
      updateUser: updateUserMock,
      setSession: vi.fn(),
    },
  },
}))

vi.mock('@/api/grpc-unauth', () => ({
  devAuthGrpc: {
    startOAuthSignIn: vi.fn(),
    load: vi.fn(async () => ({ success: false })),
    save: vi.fn(async () => ({ success: true })),
    clear: vi.fn(async () => ({ success: true })),
  },
}))

vi.mock('@/lib/logger', () => ({
  logger: { info: vi.fn(), error: vi.fn(), warn: vi.fn(), debug: vi.fn() },
}))

vi.mock('@/lib/constants', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/lib/constants')>()),
  getIsDev: () => false,
}))

vi.mock('@/lib/telemetry', () => ({ setTelemetryUserId: vi.fn() }))

type TestWindow = Omit<Window, 'electronAPI'> & { electronAPI?: unknown }

const FORGE_ORIGIN = 'https://forge.reliantlabs.io'
const RELIANT_ORIGIN = 'https://app.reliantlabs.io'
const TOKEN = 'rcl_9f3KQeAbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_'

/** Load a page of `origin` at `path` (with a fragment, when given). */
const visit = (origin: string, path = '/', hash = '') => {
  const url = new URL(origin)
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: {
      ...window.location,
      origin: url.origin,
      hostname: url.hostname,
      protocol: url.protocol,
      pathname: path,
      search: '',
      hash,
      href: `${url.origin}${path}${hash}`,
    },
  })
}

const redirectTargetFrom = (mock: ReturnType<typeof vi.fn>): URL => {
  const call = mock.mock.calls.at(-1)
  if (!call) throw new Error('provider redirect was never requested')
  const [first, second] = call as [
    { options?: { redirectTo?: string } },
    { emailRedirectTo?: string } | undefined,
  ]
  return new URL((first.options?.redirectTo ?? second?.emailRedirectTo) as string)
}

describe('claim upgrade returns to the origin the claim started on', () => {
  beforeAll(async () => {
    await import('@/store/authStore')
  }, 60_000)

  beforeEach(() => {
    vi.resetModules()
    vi.clearAllMocks()
    window.localStorage.clear()
    // The mocked constants module survives resetModules, and the shell is memoised per page load.
    resetShellForTests()
    delete (window as TestWindow).electronAPI
    // The shared site's runtime config: APP_URL names Reliant on BOTH hosts.
    setRuntimeConfig({ APP_URL: RELIANT_ORIGIN })
    linkIdentityMock.mockResolvedValue({
      data: { url: 'https://accounts.google.com/o/oauth2/auth?example=1' },
      error: null,
    })
    updateUserMock.mockResolvedValue({ data: { user: {} }, error: null })
  })

  it('claim on the forge origin -> provider upgrade -> /auth/callback -> /claim on the SAME origin', async () => {
    visit(FORGE_ORIGIN, '/claim', `#${TOKEN}`)
    const claimLink = await import('@/lib/claimLink')
    claimLink.captureClaimLinkFromUrl()
    expect(claimLink.readPendingClaim()?.token).toBe(TOKEN)
    const claimOrigin = window.location.origin

    // UpgradeAccount's provider button, as the claim flow reaches it.
    const { useAuthStore } = await import('@/store/authStore')
    await useAuthStore.getState().linkOAuthIdentity('google', {
      source: 'link',
      returnTo: claimLink.CLAIM_RETURN_TO,
    })

    const redirect = redirectTargetFrom(linkIdentityMock)
    expect(redirect.origin).toBe(FORGE_ORIGIN)
    expect(redirect.origin).toBe(claimOrigin)
    expect(redirect.pathname).toBe('/auth/callback')
    expect(redirect.searchParams.get('returnTo')).toBe(claimLink.CLAIM_RETURN_TO)

    // The provider sends the browser back to `redirect`: same origin, so the
    // claim record it left is still there, and the callback's returnTo is the
    // claim page, which completes the claim from that record.
    visit(redirect.origin, redirect.pathname)
    const { isSafeReturnTo } = await import('@/lib/returnTo')
    const returnTo = redirect.searchParams.get('returnTo')
    expect(isSafeReturnTo(returnTo)).toBe(true)
    expect(claimLink.isClaimPath(returnTo as string)).toBe(true)
    expect(claimLink.readPendingClaim()?.token).toBe(TOKEN)
  })

  it('the emailed confirmation link of an email upgrade also returns to the forge origin', async () => {
    visit(FORGE_ORIGIN, '/upgrade')
    const { useAuthStore } = await import('@/store/authStore')

    await useAuthStore.getState().linkEmailIdentity('claimant@example.test', 'Sup3r-secret-pw!', {
      returnTo: '/claim',
    })

    const redirect = redirectTargetFrom(updateUserMock)
    expect(redirect.origin).toBe(FORGE_ORIGIN)
    expect(redirect.searchParams.get('returnTo')).toBe('/claim')
  })

  it('Reliant on app.* is unchanged: it returns to its own origin', async () => {
    visit(RELIANT_ORIGIN, '/upgrade')
    const { useAuthStore } = await import('@/store/authStore')

    await useAuthStore.getState().linkOAuthIdentity('google', { source: 'link', returnTo: '/claim' })

    expect(redirectTargetFrom(linkIdentityMock).origin).toBe(RELIANT_ORIGIN)
  })

  it('the desktop app keeps the configured public origin, even on a forge hostname', async () => {
    ;(window as TestWindow).electronAPI = { openExternal: vi.fn() }
    visit(FORGE_ORIGIN, '/upgrade')
    const { useAuthStore } = await import('@/store/authStore')

    await useAuthStore.getState().linkOAuthIdentity('google', { source: 'link' })

    expect(redirectTargetFrom(linkIdentityMock).origin).toBe(RELIANT_ORIGIN)
  })
})
