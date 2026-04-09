import { act, render } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  initialize: vi.fn(),
  syncReliantProvider: vi.fn(),
  prefetch: vi.fn(),
  checkApiKeys: vi.fn(),
  reset: vi.fn(),
  getSession: vi.fn(),
}))

vi.mock('@/store/authStore', () => ({
  useAuthStore: Object.assign(
    (selector?: (state: {
      initialize: typeof mocks.initialize
      user: { id: string; email: string } | null
      loading: boolean
      initialized: boolean
      session: { access_token: string } | null
    }) => unknown) => {
      const state = {
        initialize: mocks.initialize,
        user: { id: 'user-1', email: 'user@example.com' },
        loading: false,
        initialized: true,
        session: { access_token: 'token-123' },
      }
      return selector ? selector(state) : state
    },
    { getState: vi.fn() }
  ),
}))

vi.mock('@/store/globalDataStore', () => ({
  useGlobalDataStore: (selector: (state: { prefetch: typeof mocks.prefetch }) => unknown) =>
    selector({ prefetch: mocks.prefetch }),
}))

vi.mock('@/store/apiKeySetupStore', () => ({
  useApiKeySetupStore: (selector: (state: { checkApiKeys: typeof mocks.checkApiKeys; reset: typeof mocks.reset }) => unknown) =>
    selector({ checkApiKeys: mocks.checkApiKeys, reset: mocks.reset }),
}))

vi.mock('@/store/onboardingChecklistStore', () => ({
  useOnboardingChecklistStore: {
    getState: () => ({ isInitialized: true }),
  },
}))

vi.mock('@/api/client', () => ({
  api: {
    settings: {
      syncReliantProvider: mocks.syncReliantProvider,
    },
  },
}))

vi.mock('@/lib/logger', () => ({
  logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

vi.mock('@/lib/supabase', () => ({
  supabase: {
    auth: {
      getSession: mocks.getSession,
    },
  },
}))

describe('AuthInitializer Reliant sync', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.useFakeTimers()
    mocks.syncReliantProvider.mockResolvedValue({
      synced: true,
      created_org: false,
      created_key: false,
      rotated_key: false,
    })
    mocks.prefetch.mockResolvedValue(undefined)
    mocks.checkApiKeys.mockResolvedValue(undefined)
    mocks.getSession.mockResolvedValue({
      data: { session: { access_token: 'token-123' } },
    })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('syncs Reliant before prefetch and API key checks', async () => {
    const { AuthInitializer } = await import('@/components/AuthInitializer')

    render(
      <AuthInitializer>
        <div>child</div>
      </AuthInitializer>
    )

    await act(async () => {
      await vi.runAllTimersAsync()
      await Promise.resolve()
      await Promise.resolve()
    })

    expect(mocks.syncReliantProvider).toHaveBeenCalledTimes(1)
    expect(mocks.prefetch).toHaveBeenCalledTimes(1)
    expect(mocks.checkApiKeys).toHaveBeenCalledTimes(1)

    expect(mocks.syncReliantProvider.mock.invocationCallOrder[0]).toBeLessThan(mocks.prefetch.mock.invocationCallOrder[0])
    expect(mocks.prefetch.mock.invocationCallOrder[0]).toBeLessThan(mocks.checkApiKeys.mock.invocationCallOrder[0])
  })
})