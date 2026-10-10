import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { ErrorUpdate } from '../../../types/streaming'

const mocks = vi.hoisted(() => ({ navigate: vi.fn() }))

vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => mocks.navigate,
}))

import { WorkflowErrorMessage } from '../WorkflowErrorMessage'

function buildError(overrides: Partial<ErrorUpdate> = {}): ErrorUpdate {
  return {
    update_type: 'error',
    id: 'err-1',
    chat_id: 'chat-1',
    activity_type: 'CallLLM',
    activity_id: 'activity-1',
    error_message: 'generic error',
    timestamp: '2026-10-10T14:41:32.000Z',
    sequence_number: 1,
    ...overrides,
  }
}

// The no-fallback card (#671/#685): a model only a disconnected provider serves,
// with nothing connected to stand in. Its text says "Reconnect Codex (ChatGPT)
// in Settings → Providers"; the card must also take the user there.
const NO_FALLBACK_SUMMARY =
  'gpt-5.6-sol runs only on Codex (ChatGPT), and Codex (ChatGPT) is not connected. ' +
  "Reconnect Codex (ChatGPT) in Settings → Providers, or pick a model from a connected provider in the composer's model picker"

describe('WorkflowErrorMessage → Settings → Providers', () => {
  beforeEach(() => {
    mocks.navigate.mockReset()
  })

  it('offers a one-click way to Settings → Providers on the no-fallback error', async () => {
    const user = userEvent.setup()
    render(
      <WorkflowErrorMessage
        error={buildError({
          error_summary: NO_FALLBACK_SUMMARY,
          error_message: `activity error: failed to resolve model: ${NO_FALLBACK_SUMMARY} (type: TerminalError, retryable: false)`,
        })}
      />,
    )

    await user.click(screen.getByRole('button', { name: /open settings → providers/i }))

    expect(mocks.navigate).toHaveBeenCalledWith({
      to: '/settings/$section',
      params: { section: 'general' },
    })
  })

  it('offers it for a provider sign-in that must be redone', () => {
    render(
      <WorkflowErrorMessage
        error={buildError({ error_summary: 'Codex session expired. Please reconnect Codex. Workflow paused — send a message to retry.' })}
      />,
    )
    expect(screen.getByRole('button', { name: /open settings → providers/i })).toBeInTheDocument()
  })

  it('does not offer it for errors Settings cannot fix', () => {
    render(<WorkflowErrorMessage error={buildError({ error_summary: 'Rate limited by the AI provider' })} />)
    expect(screen.queryByRole('button', { name: /open settings → providers/i })).not.toBeInTheDocument()
  })

  it('does not offer it while the step is still retrying', () => {
    render(
      <WorkflowErrorMessage
        error={buildError({ error_summary: NO_FALLBACK_SUMMARY, is_retrying: true, attempt_number: 1, max_attempts: 5 })}
      />,
    )
    expect(screen.queryByRole('button', { name: /open settings → providers/i })).not.toBeInTheDocument()
  })
})
