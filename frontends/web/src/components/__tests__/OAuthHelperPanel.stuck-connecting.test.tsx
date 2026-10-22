/**
 * The panel must never become a dead end while a sign-in is in flight.
 *
 * Reported symptom: "on 'login with antigravity', it grays the button out, and
 * if the browser closes, or I need to restart, I have to exit and come back."
 *
 * The cause is in this SHARED panel, not in the antigravity flow, so Claude and
 * Codex have the same dead end — antigravity is just where it was noticed.
 *
 * `connecting` disabled the only button on screen, and nothing else could clear
 * it. The flow resolves when the provider redirects to the loopback receiver,
 * so a user who closed the tab, dismissed the consent screen, or hit an error
 * on Google's side produced no redirect and therefore no resolution. The
 * desktop receiver holds the flow for a full ten minutes (FLOW_TIMEOUT_MS)
 * before it gives up, and the web helper never times out at all. For that
 * entire window the panel showed a spinner on a disabled button with no way
 * back, which is why closing the whole window was the only way out.
 *
 * The fix is a visible cancel affordance while connecting. It aborts the
 * AbortController the hook owns, which releases the loopback port immediately
 * rather than at the timeout — that release matters beyond the UI, because
 * Codex's port 1455 is fixed by OpenAI and a leaked listener blocks every later
 * attempt.
 */
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

import { OAuthHelperPanel } from '../OAuthHelperPanel'

const baseProps = {
  providerName: 'Antigravity',
  available: true,
  loading: false,
  onRetry: vi.fn(),
  onConnect: vi.fn(),
}

describe('OAuthHelperPanel while connecting', () => {
  it('offers a cancel control so the user is never stranded', async () => {
    const onCancel = vi.fn()
    const user = userEvent.setup()

    render(<OAuthHelperPanel {...baseProps} connecting onCancel={onCancel} />)

    const cancel = screen.getByRole('button', { name: /cancel/i })
    expect(cancel).toBeEnabled()

    await user.click(cancel)
    expect(onCancel).toHaveBeenCalledTimes(1)
  })

  it('explains that the browser tab is what it is waiting for', () => {
    render(<OAuthHelperPanel {...baseProps} connecting onCancel={vi.fn()} />)

    // Without this the spinner is indistinguishable from a hung app, which is
    // what prompted "I have to exit and come back".
    expect(screen.getByText(/browser/i)).toBeInTheDocument()
  })

  it('still shows no cancel control when idle', () => {
    render(<OAuthHelperPanel {...baseProps} connecting={false} onCancel={vi.fn()} />)

    expect(screen.queryByRole('button', { name: /cancel/i })).not.toBeInTheDocument()
  })

  it('keeps working for callers that pass no onCancel', () => {
    render(<OAuthHelperPanel {...baseProps} connecting />)

    // The login button remains disabled — a caller that cannot cancel must not
    // get a button that silently does nothing.
    expect(
      screen.getByRole('button', { name: /login with antigravity/i }),
    ).toBeDisabled()
  })
})
