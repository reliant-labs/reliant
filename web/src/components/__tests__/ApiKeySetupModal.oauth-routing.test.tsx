/**
 * The regression this file exists for.
 *
 * `ApiKeySetupModal` chose its OAuth hook with
 * `oauthType === "claude" ? claudeOAuth : codexOAuth`. Selecting Antigravity
 * therefore ran the CODEX flow — no type error, no runtime error, and the
 * user ends up with a ChatGPT account linked under a Google button. The only
 * way to catch that is to click each provider's Connect and assert which hook
 * actually started.
 *
 * Every redirect provider is exercised from REDIRECT_OAUTH_PROVIDERS rather
 * than a hand-written list, so a fourth provider is covered the moment it is
 * registered.
 */
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { REDIRECT_OAUTH_PROVIDERS } from '@/lib/oauth-providers'

// vi.mock is hoisted above every const in this file, so the start spies are
// created inside the factory and read back out through vi.mocked afterwards.
vi.mock('@/hooks', () => {
  const makeHook = () => {
    const start = vi.fn()
    const hook = () => ({
      isRunning: false,
      lastResult: null,
      start,
      cancel: vi.fn(),
      reset: vi.fn(),
    })
    hook.start = start
    return hook
  }
  return {
    useClaudeOAuth: makeHook(),
    useCodexOAuth: makeHook(),
    useAntigravityOAuth: makeHook(),
    useCopilotOAuth: makeHook(),
    useOAuthAvailability: () => ({ available: true, loading: false, recheck: vi.fn() }),
  }
})

vi.mock('../../api/client', () => ({
  api: { settings: { validateProviderAPIKey: vi.fn(), updateProvider: vi.fn() } },
}))

vi.mock('../../store/globalDataStore', () => ({
  useGlobalDataStore: { getState: () => ({ refetchModels: vi.fn() }) },
}))

import { ApiKeySetupModal } from '../ApiKeySetupModal'
import { useClaudeOAuth, useCodexOAuth, useAntigravityOAuth } from '@/hooks'

// Display names as they appear on the provider buttons in the modal.
const PROVIDER_BUTTON_LABEL: Record<string, string> = {
  claude: 'Claude Code',
  codex: 'Codex (ChatGPT)',
  antigravity: 'Antigravity',
}

const starts: Record<string, ReturnType<typeof vi.fn>> = {
  claude: (useClaudeOAuth as unknown as { start: ReturnType<typeof vi.fn> }).start,
  codex: (useCodexOAuth as unknown as { start: ReturnType<typeof vi.fn> }).start,
  antigravity: (useAntigravityOAuth as unknown as { start: ReturnType<typeof vi.fn> })
    .start,
}

describe('ApiKeySetupModal redirect-OAuth routing', () => {
  beforeEach(() => {
    Object.values(starts).forEach((fn) => {
      fn.mockReset()
      fn.mockResolvedValue({ ok: false, errorCode: 'cancelled', message: '' })
    })
  })

  it.each([...REDIRECT_OAUTH_PROVIDERS])(
    'starts the %s flow and no other when %s is selected',
    async (provider) => {
      const user = userEvent.setup()
      render(<ApiKeySetupModal isOpen onClose={vi.fn()} />)

      await user.click(screen.getByText(PROVIDER_BUTTON_LABEL[provider]))
      const connect = await screen.findByRole('button', { name: /^login with/i })
      await user.click(connect)

      await waitFor(() => expect(starts[provider]).toHaveBeenCalledTimes(1))

      // The assertion the ternary could not satisfy: every OTHER provider's
      // flow stayed untouched.
      for (const other of REDIRECT_OAUTH_PROVIDERS) {
        if (other !== provider) {
          expect(starts[other]).not.toHaveBeenCalled()
        }
      }
    },
  )
})
