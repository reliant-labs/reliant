import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// ---------------------------------------------------------------------------
// forge sign-in (AUTH_PROVIDER "forge"): the store restores, signs in and signs
// out through the login broker (lib/auth/native-login), never supabase-js, and
// installs each identity through the same serialized transition. The
// provider-neutral `identity` is what the app reads.
// ---------------------------------------------------------------------------

const broker = vi.hoisted(() => ({
  fetchIdentity: vi.fn(),
  signIn: vi.fn(),
  signOut: vi.fn(),
}))
vi.mock('@/lib/auth/native-login', () => broker)

const supabaseAuth = vi.hoisted(() => ({
  getSession: vi.fn(async () => ({ data: { session: null }, error: null })),
  onAuthStateChange: vi.fn(() => ({ data: { subscription: { unsubscribe: vi.fn() } } })),
  signOut: vi.fn(async () => ({ error: null })),
  signInWithPassword: vi.fn(),
  signInAnonymously: vi.fn(),
}))
vi.mock('@/lib/supabase', () => ({ supabase: { auth: supabaseAuth }, setAuthStorageUnreadableHandler: vi.fn() }))

const resetMock = vi.hoisted(() => vi.fn())
vi.mock('../chatStore', () => ({ useChatStore: { getState: () => ({ reset: resetMock }) } }))
vi.mock('../projectStore', () => ({ useProjectStore: { getState: () => ({ reset: resetMock }) } }))
vi.mock('../worktreeStore', () => ({ useWorktreeStore: { getState: () => ({ reset: resetMock }) } }))
vi.mock('../chatNavigationStore', () => ({ useChatNavigationStore: { getState: () => ({ reset: resetMock }) } }))
vi.mock('../attachmentStore', () => ({ useAttachmentStore: { getState: () => ({ reset: resetMock }) } }))
vi.mock('../../hooks/worktree-queries', () => ({ resetWorktreeAnnouncements() {} }))
vi.mock('@/services/controlPlane/onboarding', () => ({ resetUserCache() {} }))
vi.mock('@/lib/logger', () => ({ logger: { info: vi.fn(), error: vi.fn(), warn: vi.fn(), debug: vi.fn() } }))
vi.mock('@/lib/telemetry', () => ({ setTelemetryUserId: vi.fn() }))

const ALICE = {
  authenticated: true,
  subject: 'a1b2c3d4-0000-4000-8000-000000000001',
  email: 'alice@example.test',
  name: 'Alice',
  account: { id: 'acct-1', created_at: '2026-01-02T03:04:05Z', onboarding_completed: true },
}
const BOB = { authenticated: true, subject: 'b0b00000-0000-4000-8000-000000000002', email: 'bob@example.test' }

async function loadStore() {
  vi.resetModules()
  ;(window as unknown as { __FORGE_CONFIG__?: unknown }).__FORGE_CONFIG__ = { AUTH_PROVIDER: 'forge' }
  const { useAuthStore } = await import('../authStore')
  const { getBearerToken } = await import('@/lib/auth/use-auth')
  return { useAuthStore, getBearerToken }
}

beforeEach(() => {
  broker.fetchIdentity.mockReset()
  broker.signIn.mockReset()
  broker.signOut.mockReset()
  localStorage.clear()
})

afterEach(() => {
  delete (window as unknown as { __FORGE_CONFIG__?: unknown }).__FORGE_CONFIG__
})

describe('authStore with forge sign-in', () => {
  it('restores the broker session without touching supabase', async () => {
    broker.fetchIdentity.mockResolvedValue(ALICE)
    const { useAuthStore, getBearerToken } = await loadStore()
    await useAuthStore.getState().initialize()
    const { identity, initialized, loading } = useAuthStore.getState()
    expect(initialized).toBe(true)
    expect(loading).toBe(false)
    expect(identity).toMatchObject({
      id: ALICE.subject,
      email: ALICE.email,
      name: 'Alice',
      provider: 'forge',
      emailVerified: true,
      isAnonymous: false,
      isApiKey: false,
      createdAtMs: Date.parse('2026-01-02T03:04:05Z'),
    })
    expect(getBearerToken()).toBeNull()
    expect(supabaseAuth.getSession).not.toHaveBeenCalled()
  })

  it('restores signed out when the broker says so', async () => {
    broker.fetchIdentity.mockResolvedValue({ authenticated: false })
    const { useAuthStore } = await loadStore()
    await useAuthStore.getState().initialize()
    expect(useAuthStore.getState().identity).toBeNull()
    expect(useAuthStore.getState().initialized).toBe(true)
  })

  it('signs in with a password through the broker', async () => {
    broker.fetchIdentity.mockResolvedValue({ authenticated: false })
    broker.signIn.mockResolvedValue(ALICE)
    const { useAuthStore } = await loadStore()
    await useAuthStore.getState().initialize()
    await useAuthStore.getState().signIn('alice@example.test', 'pw')
    expect(broker.signIn).toHaveBeenCalledWith({ email: 'alice@example.test', password: 'pw' })
    expect(supabaseAuth.signInWithPassword).not.toHaveBeenCalled()
    expect(useAuthStore.getState().identity?.id).toBe(ALICE.subject)
  })

  it('clears the previous account when the cookie names another subject', async () => {
    broker.fetchIdentity.mockResolvedValueOnce(ALICE).mockResolvedValueOnce(BOB)
    const { useAuthStore } = await loadStore()
    await useAuthStore.getState().initialize()
    resetMock.mockClear()
    await useAuthStore.getState().refreshSession()
    expect(useAuthStore.getState().identity?.id).toBe(BOB.subject)
    expect(resetMock).toHaveBeenCalled()
  })

  it('signs out through the broker and holds nobody', async () => {
    broker.fetchIdentity.mockResolvedValue(ALICE)
    broker.signOut.mockResolvedValue({ authenticated: false })
    const { useAuthStore } = await loadStore()
    await useAuthStore.getState().initialize()
    await useAuthStore.getState().signOut()
    expect(broker.signOut).toHaveBeenCalled()
    expect(supabaseAuth.signOut).not.toHaveBeenCalled()
    expect(useAuthStore.getState().identity).toBeNull()
    expect(useAuthStore.getState().loading).toBe(false)
  })

  it('installs an identity the broker answered elsewhere (sign-up, social, magic link)', async () => {
    broker.fetchIdentity.mockResolvedValue({ authenticated: false })
    const { useAuthStore } = await loadStore()
    await useAuthStore.getState().initialize()
    expect(await useAuthStore.getState().installForgeIdentity(BOB)).toBe(true)
    expect(useAuthStore.getState().identity?.id).toBe(BOB.subject)
    expect(await useAuthStore.getState().installForgeIdentity({ authenticated: false })).toBe(false)
  })

  it('refuses the Supabase-only flows', async () => {
    broker.fetchIdentity.mockResolvedValue({ authenticated: false })
    const { useAuthStore } = await loadStore()
    await expect(useAuthStore.getState().signInAnonymously()).rejects.toThrow(/not available with forge sign-in/)
    await expect(useAuthStore.getState().signInWithGithub()).rejects.toThrow(/not available with forge sign-in/)
    expect(supabaseAuth.signInAnonymously).not.toHaveBeenCalled()
  })
})

describe('authStore forge rechecks', () => {
  const deferred = <T,>() => {
    let resolve!: (value: T) => void
    const promise = new Promise<T>((r) => { resolve = r })
    return { promise, resolve }
  }

  it('keeps the session when the broker cannot answer', async () => {
    broker.fetchIdentity.mockResolvedValueOnce(ALICE).mockRejectedValueOnce(new Error('503 store unavailable'))
    const { useAuthStore } = await loadStore()
    await useAuthStore.getState().initialize()
    resetMock.mockClear()
    await useAuthStore.getState().refreshSession()
    expect(useAuthStore.getState().identity?.id).toBe(ALICE.subject)
    expect(resetMock).not.toHaveBeenCalled()
  })

  it('restores nobody, without throwing, when the broker cannot answer at load', async () => {
    broker.fetchIdentity.mockRejectedValueOnce(new Error('unreachable'))
    const { useAuthStore } = await loadStore()
    await useAuthStore.getState().initialize()
    expect(useAuthStore.getState().identity).toBeNull()
    expect(useAuthStore.getState().initialized).toBe(true)
  })

  it('does not reinstall an identity a recheck read before sign-out', async () => {
    const late = deferred<typeof ALICE>()
    broker.fetchIdentity.mockResolvedValueOnce(ALICE).mockReturnValueOnce(late.promise)
    broker.signOut.mockResolvedValue({ authenticated: false })
    const { useAuthStore } = await loadStore()
    await useAuthStore.getState().initialize()
    const recheck = useAuthStore.getState().refreshSession()
    await useAuthStore.getState().signOut()
    late.resolve(ALICE)
    await recheck
    expect(useAuthStore.getState().identity).toBeNull()
  })

  it('does not reinstall an identity a recheck read before the cookie changed hands', async () => {
    const older = deferred<typeof ALICE>()
    broker.fetchIdentity.mockResolvedValueOnce(ALICE).mockReturnValueOnce(older.promise).mockResolvedValueOnce(BOB)
    const { useAuthStore } = await loadStore()
    await useAuthStore.getState().initialize()
    const first = useAuthStore.getState().refreshSession()
    await useAuthStore.getState().refreshSession()
    expect(useAuthStore.getState().identity?.id).toBe(BOB.subject)
    older.resolve(ALICE)
    await first
    expect(useAuthStore.getState().identity?.id).toBe(BOB.subject)
  })
})
