import { render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  refetchModels: vi.fn(),
  updateProvider: vi.fn(),
  validateProviderAPIKey: vi.fn(),
  getPreferences: vi.fn(),
  updatePreferences: vi.fn(),
  useApiKeySetupSetState: vi.fn(),
  resetApiKeySetupDismissed: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  api: {
    settings: {
      updateProvider: mocks.updateProvider,
      validateProviderAPIKey: mocks.validateProviderAPIKey,
      getPreferences: mocks.getPreferences,
      updatePreferences: mocks.updatePreferences,
    },
  },
}))

vi.mock('@/store/globalDataStore', () => ({
  useGlobalDataStore: {
    getState: () => ({
      refetchModels: mocks.refetchModels,
    }),
  },
}))

vi.mock('@/store/apiKeySetupStore', () => ({
  useApiKeySetupStore: {
    setState: mocks.useApiKeySetupSetState,
  },
  resetApiKeySetupDismissed: mocks.resetApiKeySetupDismissed,
}))

vi.mock('@/hooks', () => ({
  useCodexOAuth: () => ({ start: vi.fn(), cancel: vi.fn() }),
  useClaudeOAuth: () => ({ start: vi.fn(), cancel: vi.fn() }),
  useOAuthAvailability: () => ({ available: true, loading: false, recheck: vi.fn() }),
}))

import { CombinedGeneralSettings } from '@/components/Settings/CombinedGeneralSettings'

describe('CombinedGeneralSettings Reliant manual controls', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.getPreferences.mockResolvedValue({ streaming_enabled: false })
    mocks.updatePreferences.mockResolvedValue({})
    mocks.refetchModels.mockResolvedValue(undefined)
  })

  it('hides Reliant from add-provider options while still showing managed status', async () => {
    render(
      <CombinedGeneralSettings
        providers={[
          {
            provider: 'reliant',
            displayName: 'Reliant',
            hasApiKey: true,
            maskedKey: 'sk-a...1234',
            configured: true,
          },
        ]}
      />
    )

    expect(await screen.findByText('Reliant')).toBeInTheDocument()
    expect(screen.getByText(/managed automatically from your Reliant account/i)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Update' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Delete' })).not.toBeInTheDocument()
    expect(screen.queryByRole('option', { name: 'Reliant' })).not.toBeInTheDocument()
  })
})
