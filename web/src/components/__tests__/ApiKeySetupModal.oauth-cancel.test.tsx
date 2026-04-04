import { act, render } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const codexCancelMock = vi.fn()
const claudeCancelMock = vi.fn()

vi.mock('@/hooks', () => ({
  useCodexOAuth: () => ({ isRunning: false, lastResult: null, start: vi.fn(), cancel: codexCancelMock, reset: vi.fn() }),
  useClaudeOAuth: () => ({ isRunning: false, lastResult: null, start: vi.fn(), cancel: claudeCancelMock, reset: vi.fn() }),
  useOAuthAvailability: () => ({ available: true, loading: false, recheck: vi.fn() }),
}))

vi.mock('@/api/client', () => ({
  api: {
    settings: {
      validateProviderAPIKey: vi.fn(),
      updateProvider: vi.fn(),
    },
  },
}))

vi.mock('@/lib/logger', () => ({
  logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

import { ApiKeySetupModal } from '@/components/ApiKeySetupModal'
import { useApiKeySetupStore } from '@/store/apiKeySetupStore'

describe('ApiKeySetupModal OAuth cancellation', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    useApiKeySetupStore.setState({ showModal: true })
  })

  it('cancels in-flight OAuth flows when the modal hides', () => {
    const { rerender } = render(<ApiKeySetupModal />)

    expect(codexCancelMock).not.toHaveBeenCalled()
    expect(claudeCancelMock).not.toHaveBeenCalled()

    act(() => {
      useApiKeySetupStore.setState({ showModal: false })
    })
    rerender(<ApiKeySetupModal />)

    expect(codexCancelMock).toHaveBeenCalled()
    expect(claudeCancelMock).toHaveBeenCalled()
  })
})