import { render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

const mocks = vi.hoisted(() => ({
  refetchModels: vi.fn(),
  updateProvider: vi.fn(),
  validateProviderAPIKey: vi.fn(),
  getPreferences: vi.fn(),
  updatePreferences: vi.fn(),
  useApiKeySetupSetState: vi.fn(),
  resetApiKeySetupDismissed: vi.fn(),
  provisionManagedKey: vi.fn(),
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

vi.mock('@/hooks/useOnboardingQueries', () => ({
  useCloudEligibility: () => ({ eligible: false, reason: null, isLoading: false }),
}))

vi.mock('@/services/controlPlane/onboarding', () => ({
  onboardingService: {
    provisionManagedKey: mocks.provisionManagedKey,
  },
}))

// CombinedGeneralSettings now uses TanStack Router's useNavigate for the
// in-app Reliant AI / billing links. The test renders the component outside a
// RouterProvider, so stub the hook to a no-op navigate.
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => vi.fn(),
}))

import { CombinedGeneralSettings } from '@/components/Settings/CombinedGeneralSettings'

function renderWithClient(ui: React.ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>)
}

describe('CombinedGeneralSettings Reliant provider', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.getPreferences.mockResolvedValue({ streaming_enabled: false })
    mocks.updatePreferences.mockResolvedValue({})
    mocks.refetchModels.mockResolvedValue(undefined)
  })

  it('shows Reliant in the add-provider dropdown so users can open the admin portal', async () => {
    renderWithClient(<CombinedGeneralSettings providers={[]} />)

    const select = await screen.findByRole('combobox')
    const options = Array.from(select.querySelectorAll('option'))
    const reliantOption = options.find((o) => o.textContent === 'Reliant')
    expect(reliantOption).toBeDefined()
  })

  it('renders Disconnect (not Delete) and hides the masked key for a configured Reliant row', async () => {
    renderWithClient(
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

    // Disconnect button is rendered (and the legacy "Managed by Reliant" badge is gone).
    expect(await screen.findByRole('button', { name: /disconnect/i })).toBeInTheDocument()
    expect(screen.queryByText('Managed by Reliant')).not.toBeInTheDocument()

    // Key stays opaque — no masked key shown.
    expect(screen.queryByText('sk-...abcd')).not.toBeInTheDocument()

    // No Update button for Reliant — the key is provisioned, not manually editable.
    expect(screen.queryByRole('button', { name: /update/i })).not.toBeInTheDocument()

    // No "Delete" label — Reliant uses Disconnect terminology.
    expect(screen.queryByRole('button', { name: /^delete$/i })).not.toBeInTheDocument()

    // No mutation should fire on render.
    expect(mocks.updateProvider).not.toHaveBeenCalled()
  })
})
