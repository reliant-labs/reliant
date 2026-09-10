import { beforeEach, describe, expect, it, vi } from 'vitest'

/**
 * Starting a provider round-trip must not blank the page.
 *
 * AuthGuard renders a full-screen LoadingSpinner INSTEAD of its children
 * whenever the store's `loading` is true. Both OAuth entry points used to set
 * it: `runOAuthSignIn` (sign-in) and `linkOAuthIdentity` (attaching a provider
 * to an existing account). Between the click and the browser actually leaving
 * for the provider there are a few hundred ms — a Supabase round-trip, and on
 * the link path an analytics await too — and for that whole window the app
 * unmounted the screen the user was looking at and replaced it with the boot
 * spinner. On return the tree remounted. The user sees a flash that is
 * indistinguishable from the page refreshing on its own, which reads as a
 * fault rather than as progress.
 *
 * The click is already acknowledged without unmounting anything: each caller
 * tracks which provider is pending and OAuthButton renders that one as busy
 * (see AuthScreen.oauth-pending.test.tsx). That is the correct layer for it —
 * it is local to the screen, so it cannot take the rest of the app down.
 *
 * These tests pin the store's side of that contract: neither path may touch
 * the global flag, on success or on failure.
 */

const signInWithOAuthMock = vi.fn()
const linkIdentityMock = vi.fn()
const getSessionMock = vi.fn(async () => ({ data: { session: null }, error: null }))
const onAuthStateChangeMock = vi.fn(() => ({
  data: { subscription: { unsubscribe: vi.fn() } },
}))

vi.mock('@/lib/supabase', () => ({
  supabase: {
    auth: {
      getSession: getSessionMock,
      onAuthStateChange: onAuthStateChangeMock,
      signInWithOAuth: signInWithOAuthMock,
      linkIdentity: linkIdentityMock,
      setSession: vi.fn(),
    },
  },
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

const PROVIDER_URL = 'https://accounts.google.com/o/oauth2/auth?client_id=x'

beforeEach(() => {
  vi.resetModules()
  vi.clearAllMocks()
  delete (window as Window & { electronAPI?: unknown }).electronAPI
  signInWithOAuthMock.mockResolvedValue({ data: { url: PROVIDER_URL }, error: null })
  linkIdentityMock.mockResolvedValue({ data: { url: PROVIDER_URL }, error: null })
})

/**
 * Load the store fresh and settle it into the state a signed-in user is in:
 * `loading` false, which is what AuthGuard needs to render the app at all.
 */
const freshStore = async () => {
  const { useAuthStore } = await import('../authStore')
  useAuthStore.setState({ loading: false, initialized: true })
  return useAuthStore
}

describe('OAuth start does not trip the global loading flag', () => {
  it('leaves `loading` false while sign-in is starting', async () => {
    const useAuthStore = await freshStore()

    // Observe every value the flag takes, not just the value it settles on: a
    // true that is immediately set back to false is exactly the flicker this
    // guards against, and a before/after assertion cannot see it.
    const seen: boolean[] = []
    const unsubscribe = useAuthStore.subscribe((s) => seen.push(s.loading))

    await useAuthStore.getState().signInWithGoogle()
    unsubscribe()

    expect(seen).not.toContain(true)
    expect(useAuthStore.getState().loading).toBe(false)
  })

  it('leaves `loading` false while a link flow is starting', async () => {
    const useAuthStore = await freshStore()

    const seen: boolean[] = []
    const unsubscribe = useAuthStore.subscribe((s) => seen.push(s.loading))

    await useAuthStore.getState().linkOAuthIdentity('github')
    unsubscribe()

    expect(seen).not.toContain(true)
    expect(useAuthStore.getState().loading).toBe(false)
  })

  it('leaves `loading` false when the provider call fails', async () => {
    const useAuthStore = await freshStore()
    signInWithOAuthMock.mockResolvedValue({
      data: null,
      error: Object.assign(new Error('Manual linking is disabled'), {
        code: 'manual_linking_disabled',
      }),
    })

    const seen: boolean[] = []
    const unsubscribe = useAuthStore.subscribe((s) => seen.push(s.loading))

    // The failure must surface to the caller so the screen can show it — the
    // point is that it does so without having blanked the app on the way.
    await expect(useAuthStore.getState().signInWithGoogle()).rejects.toThrow()
    unsubscribe()

    expect(seen).not.toContain(true)
    expect(useAuthStore.getState().loading).toBe(false)
  })
})
