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
  useModels: () => ({ models: [], loading: false, error: null }),
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

describe('CombinedGeneralSettings Reliant provider', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.getPreferences.mockResolvedValue({ streaming_enabled: false })
    mocks.updatePreferences.mockResolvedValue({})
    mocks.refetchModels.mockResolvedValue(undefined)
  })

  it('shows Reliant in the add-provider dropdown so users can open the admin portal', async () => {
    render(
      <CombinedGeneralSettings
        providers={[]}
      />
    )

    const select = await screen.findByRole('combobox')
    const options = Array.from(select.querySelectorAll('option'))
    const reliantOption = options.find((o) => o.textContent === 'Reliant')
    expect(reliantOption).toBeDefined()
  })

  it('renders a managed badge and hides edit/delete affordances when Reliant is configured', async () => {
    render(
      <CombinedGeneralSettings
        providers={[
          {
            provider: 'reliant',
            displayName: 'Reliant',
            hasApiKey: true,
            maskedKey: 'sk-...abcd',
            configured: true,
          },
        ]}
      />
    )

    // Managed badge is visible.
    expect(await screen.findByText('Managed by Reliant')).toBeInTheDocument()

    // No masked key is shown for the managed provider.
    expect(screen.queryByText('sk-...abcd')).not.toBeInTheDocument()

    // No Update / Delete buttons for the managed row.
    expect(screen.queryByRole('button', { name: /update/i })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^delete$/i })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /disconnect/i })).not.toBeInTheDocument()

    // No mutation should fire on render.
    expect(mocks.updateProvider).not.toHaveBeenCalled()
  })
})
