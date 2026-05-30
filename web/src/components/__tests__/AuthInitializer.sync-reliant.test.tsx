import { render, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Session, User } from '@supabase/supabase-js'

const mocks = vi.hoisted(() => ({
  syncReliantProvider: vi.fn(),
  triggerRefetch: vi.fn(),
  detectCompletedItems: vi.fn(),
  initializePrivacy: vi.fn(),
  settingsInitialize: vi.fn(),
  applyAppearanceSettingsToDOM: vi.fn(),
  loadProjects: vi.fn(),
  initSentry: vi.fn(),
  identifyUser: vi.fn(),
  resetUser: vi.fn(),
  prefetch: vi.fn(),
  checkApiKeys: vi.fn(),
  resetApiKeyCheck: vi.fn(),
  loggerWarn: vi.fn(),
  loggerInfo: vi.fn(),
  authInitialize: vi.fn(),
}))

const mockUser = { id: 'user-1', email: 'user@example.com' } as User
const mockSession = { access_token: 'tok-abc' } as Session

let mockAuthState: {
  user: User | null
  session: Session | null
  loading: boolean
  initialized: boolean
} = {
  user: mockUser,
  session: mockSession,
  loading: false,
  initialized: true,
}

vi.mock('@/store/authStore', () => ({
  useAuthStore: Object.assign(
    (selector?: (state: typeof mockAuthState & { initialize: typeof mocks.authInitialize }) => unknown) => {
      const state = { ...mockAuthState, initialize: mocks.authInitialize }
      return selector ? selector(state) : state
    },
    {
      getState: () => ({ ...mockAuthState, initialize: mocks.authInitialize }),
    },
  ),
}))

vi.mock('@/store/globalDataStore', () => ({
  useGlobalDataStore: (selector: (state: { prefetch: typeof mocks.prefetch }) => unknown) =>
    selector({ prefetch: mocks.prefetch }),
}))

vi.mock('@/store/apiKeySetupStore', () => ({
  useApiKeySetupStore: (
    selector: (state: { checkApiKeys: typeof mocks.checkApiKeys; reset: typeof mocks.resetApiKeyCheck }) => unknown,
  ) => selector({ checkApiKeys: mocks.checkApiKeys, reset: mocks.resetApiKeyCheck }),
}))

vi.mock('@/store/privacyStore', () => ({
  usePrivacyStore: (selector: (state: { initialize: typeof mocks.initializePrivacy }) => unknown) =>
    selector({ initialize: mocks.initializePrivacy }),
}))

vi.mock('@/store/projectStore', () => ({
  useProjectStore: {
    getState: () => ({ loadProjects: mocks.loadProjects }),
  },
}))

vi.mock('@/services/settingsSync', () => ({
  settingsSync: {
    initialize: mocks.settingsInitialize,
    applyAppearanceSettingsToDOM: mocks.applyAppearanceSettingsToDOM,
  },
}))

vi.mock('@/lib/sentry', () => ({
  initSentry: mocks.initSentry,
}))

vi.mock('@/lib/analytics', () => ({
  identifyUser: mocks.identifyUser,
  resetUser: mocks.resetUser,
}))

vi.mock('@/lib/supabase', () => ({
  supabase: {
    auth: {
      getSession: vi.fn().mockResolvedValue({ data: { session: mockSession } }),
    },
  },
}))

vi.mock('@/lib/logger', () => ({
  logger: { info: mocks.loggerInfo, warn: mocks.loggerWarn, error: vi.fn(), debug: vi.fn() },
}))

vi.mock('@/api/client', () => ({
  api: {
    settings: {
      syncReliantProvider: mocks.syncReliantProvider,
    },
  },
}))

vi.mock('@/store/refetchStore', () => ({
  triggerRefetch: mocks.triggerRefetch,
}))

vi.mock('../../store/onboardingChecklistStore', () => ({
  useOnboardingChecklistStore: {
    getState: () => ({
      detectCompletedItems: mocks.detectCompletedItems,
      loadState: vi.fn(),
      isInitialized: true,
    }),
  },
}))

vi.mock('../../store/tourStore', () => ({
  useTourStore: {
    getState: () => ({ loadState: vi.fn() }),
  },
}))

describe('AuthInitializer SyncReliantProvider on login', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockAuthState = {
      user: mockUser,
      session: mockSession,
      loading: false,
      initialized: true,
    }
    mocks.settingsInitialize.mockResolvedValue(undefined)
    mocks.initializePrivacy.mockResolvedValue(undefined)
    mocks.loadProjects.mockResolvedValue(undefined)
    mocks.initSentry.mockResolvedValue(undefined)
    mocks.prefetch.mockResolvedValue(undefined)
    mocks.checkApiKeys.mockResolvedValue(undefined)
  })

  it('calls syncReliantProvider after login and refreshes provider caches on success', async () => {
    mocks.syncReliantProvider.mockResolvedValue({
      success: true,
      message: 'synced',
      synced: true,
      createdOrg: false,
      createdKey: true,
      rotatedKey: false,
    })

    const { AuthInitializer } = await import('@/components/AuthInitializer')

    render(
      <AuthInitializer>
        <div>child</div>
      </AuthInitializer>,
    )

    await waitFor(() => {
      expect(mocks.syncReliantProvider).toHaveBeenCalledTimes(1)
    })

    await waitFor(() => {
      expect(mocks.triggerRefetch).toHaveBeenCalledWith('config_health')
      expect(mocks.detectCompletedItems).toHaveBeenCalled()
    })
  })

  it('logs and does not throw when syncReliantProvider rejects', async () => {
    mocks.syncReliantProvider.mockRejectedValue(new Error('control-plane unreachable'))

    const { AuthInitializer } = await import('@/components/AuthInitializer')

    render(
      <AuthInitializer>
        <div>child</div>
      </AuthInitializer>,
    )

    await waitFor(() => {
      expect(mocks.syncReliantProvider).toHaveBeenCalled()
    })

    expect(mocks.triggerRefetch).not.toHaveBeenCalled()
    expect(mocks.detectCompletedItems).not.toHaveBeenCalled()
    // Failure is non-blocking — children still render.
  })
})
