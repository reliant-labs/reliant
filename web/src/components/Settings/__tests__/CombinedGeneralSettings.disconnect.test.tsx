import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

// Settings → Providers must change only the provider the user acted on.
// Prod, 2026-10-09: a user whose only provider was Codex lost it seconds before
// a Claude sign-in. The server log shows the web client sent one
// UpdateProviderAPIKey (this Disconnect) and then CompleteClaudeOAuth; these
// pin both halves: Disconnect says exactly which provider goes, and a Claude
// sign-in never sends UpdateProviderAPIKey at all.

const mocks = vi.hoisted(() => ({
  refetchModels: vi.fn(),
  updateProvider: vi.fn(),
  validateProviderAPIKey: vi.fn(),
  getPreferences: vi.fn(),
  updatePreferences: vi.fn(),
  claudeStart: vi.fn(),
  codexStart: vi.fn(),
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
  useGlobalDataStore: { getState: () => ({ refetchModels: mocks.refetchModels }) },
  useModels: () => ({ models: [], loading: false, error: null }),
}))

vi.mock('@/store/apiKeySetupStore', () => ({
  useApiKeySetupStore: { setState: vi.fn() },
  resetApiKeySetupDismissed: vi.fn(),
}))

vi.mock('@/hooks', () => ({
  useClaudeOAuth: () => ({ start: mocks.claudeStart, cancel: vi.fn() }),
  useCodexOAuth: () => ({ start: mocks.codexStart, cancel: vi.fn() }),
  useAntigravityOAuth: () => ({ start: vi.fn(), cancel: vi.fn() }),
  useCopilotOAuth: () => ({
    phase: 'idle',
    isActive: false,
    userCode: null,
    verificationUri: null,
    message: null,
    lastResult: null,
    start: vi.fn(),
    cancel: vi.fn(),
    reset: vi.fn(),
  }),
  useOAuthAvailability: () => ({ available: true, loading: false, recheck: vi.fn() }),
}))

vi.mock('@/services/controlPlane/onboarding', () => ({
  onboardingService: { provisionManagedKey: vi.fn() },
}))

vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => vi.fn(),
}))

vi.mock('@/lib/events', () => ({
  getEventBus: () => ({ emit: vi.fn() }),
}))

import { CombinedGeneralSettings } from '@/components/Settings/CombinedGeneralSettings'

const codexConnected = {
  provider: 'codex',
  displayName: 'Codex (ChatGPT)',
  hasApiKey: true,
  configured: true,
}

type Providers = React.ComponentProps<typeof CombinedGeneralSettings>['providers']

function renderSettings(providers: Providers = [codexConnected]) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <CombinedGeneralSettings providers={providers} />
    </QueryClientProvider>,
  )
}

describe('Settings → Providers: one provider at a time', () => {
  const confirmSpy = vi.fn()

  beforeEach(() => {
    vi.clearAllMocks()
    vi.stubGlobal('confirm', confirmSpy)
    mocks.getPreferences.mockResolvedValue({ streaming_enabled: false })
    mocks.updatePreferences.mockResolvedValue({})
    mocks.updateProvider.mockResolvedValue({ message: 'ok' })
    mocks.refetchModels.mockResolvedValue(undefined)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('names the provider being disconnected and says the others stay connected', async () => {
    confirmSpy.mockReturnValue(false)
    const user = userEvent.setup()
    renderSettings()

    await user.click(await screen.findByRole('button', { name: 'Disconnect Codex (ChatGPT)' }))

    expect(confirmSpy).toHaveBeenCalledTimes(1)
    const prompt = confirmSpy.mock.calls[0][0] as string
    expect(prompt).toMatch(/^Disconnect Codex \(ChatGPT\)\?/)
    expect(prompt).toContain('Chats using Codex (ChatGPT) models stop working')
    expect(prompt).toContain('Your other providers stay connected')
    // A sign-in provider has no API key; the prompt must not claim one.
    expect(prompt).not.toMatch(/api key/i)
    // Declined: nothing is sent.
    expect(mocks.updateProvider).not.toHaveBeenCalled()
  })

  it('confirmed, it disconnects only the provider whose button was clicked', async () => {
    confirmSpy.mockReturnValue(true)
    const user = userEvent.setup()
    renderSettings([
      codexConnected,
      { provider: 'openai', displayName: 'OpenAI', hasApiKey: true, configured: true, maskedKey: 'sk-...abcd' },
    ])

    await user.click(await screen.findByRole('button', { name: 'Disconnect Codex (ChatGPT)' }))

    await waitFor(() => expect(mocks.updateProvider).toHaveBeenCalledTimes(1))
    expect(mocks.updateProvider).toHaveBeenCalledWith('codex', '')
  })

  it('a Claude sign-in with Codex connected never sends UpdateProviderAPIKey', async () => {
    mocks.claudeStart.mockResolvedValue({ ok: true, message: 'Connected to Claude' })
    const user = userEvent.setup()
    renderSettings()

    await user.selectOptions(await screen.findByRole('combobox'), 'claude')
    await user.click(await screen.findByRole('button', { name: 'Login with Claude Code' }))

    await waitFor(() => expect(mocks.claudeStart).toHaveBeenCalledTimes(1))
    // The success path ran to its end (models refetched after the sign-in).
    await waitFor(() => expect(mocks.refetchModels).toHaveBeenCalledTimes(1))
    expect(mocks.codexStart).not.toHaveBeenCalled()
    expect(confirmSpy).not.toHaveBeenCalled()
    expect(mocks.updateProvider).not.toHaveBeenCalled()
  })
})
