/**
 * The account screen — the one mobile affordance whose absence was a dead end.
 *
 * The load-bearing assertions are that sign-out goes through the SHARED store
 * action (a local reimplementation would leave seven other stores populated)
 * and that it lands on `/auth`, not on a mobile route the now-signed-out user
 * cannot render.
 */

import { describe, expect, it, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

const signOut = vi.fn()
const navigate = vi.fn()
const setSetting = vi.fn()

let user: unknown = {
  email: 'ada@example.com',
  user_metadata: { full_name: 'Ada L' },
  identities: [{ provider: 'google' }],
}

vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => navigate,
}))

vi.mock('../../../store/authStore', () => ({
  useAuthStore: (selector: (s: unknown) => unknown) =>
    selector({ user, signOut }),
}))

vi.mock('../../../services/settingsSync', () => ({
  settingsSync: { setSetting },
  SETTINGS_KEYS: { THEME: 'appearance.theme' },
}))

const { MobileAccountScreen } = await import('../MobileAccountScreen')

beforeEach(() => {
  signOut.mockReset()
  signOut.mockResolvedValue(undefined)
  navigate.mockReset()
  navigate.mockResolvedValue(undefined)
  setSetting.mockReset()
  setSetting.mockResolvedValue(undefined)
  document.documentElement.classList.remove('dark')
  user = {
    email: 'ada@example.com',
    user_metadata: { full_name: 'Ada L' },
    identities: [{ provider: 'google' }],
  }
})

describe('MobileAccountScreen', () => {
  it('shows the display name and email', () => {
    render(<MobileAccountScreen />)
    expect(screen.getByText('Ada L')).toBeInTheDocument()
    expect(screen.getByText('ada@example.com')).toBeInTheDocument()
  })

  it('falls back to the email when there is no display name', () => {
    user = { email: 'nobody@example.com', identities: [{ provider: 'github' }] }
    render(<MobileAccountScreen />)
    expect(screen.getByText('nobody@example.com')).toBeInTheDocument()
    expect(screen.getByText('Connected via github')).toBeInTheDocument()
  })

  it('says so plainly for an anonymous session', () => {
    // The app hands out anonymous sessions pre-upgrade; rendering a blank
    // identity block for one reads as a load failure.
    user = { identities: [{ provider: 'anonymous' }] }
    render(<MobileAccountScreen />)
    expect(screen.getByText('Signed in')).toBeInTheDocument()
    expect(screen.getByText('Anonymous session')).toBeInTheDocument()
  })

  it('does not sign out on the first tap', async () => {
    render(<MobileAccountScreen />)
    await userEvent.click(screen.getByRole('button', { name: /sign out/i }))

    // A mis-tap on a phone is easy, so the first tap only asks.
    expect(signOut).not.toHaveBeenCalled()
    expect(screen.getByText(/sign out of this account\?/i)).toBeInTheDocument()
  })

  it('calls the shared store action once confirmed and lands on /auth', async () => {
    render(<MobileAccountScreen />)
    await userEvent.click(screen.getByRole('button', { name: /sign out/i }))
    await userEvent.click(screen.getByRole('button', { name: /^sign out$/i }))

    await waitFor(() => expect(signOut).toHaveBeenCalledTimes(1))
    await waitFor(() =>
      expect(navigate).toHaveBeenCalledWith({
        to: '/auth',
        search: { redirect: undefined },
      }),
    )
  })

  it('backs out of the confirmation without signing out', async () => {
    render(<MobileAccountScreen />)
    await userEvent.click(screen.getByRole('button', { name: /sign out/i }))
    await userEvent.click(screen.getByRole('button', { name: /cancel/i }))

    expect(signOut).not.toHaveBeenCalled()
    expect(screen.queryByText(/sign out of this account\?/i)).not.toBeInTheDocument()
  })

  it('keeps the user here when sign-out fails', async () => {
    signOut.mockRejectedValue(new Error('network down'))
    render(<MobileAccountScreen />)
    await userEvent.click(screen.getByRole('button', { name: /sign out/i }))
    await userEvent.click(screen.getByRole('button', { name: /^sign out$/i }))

    expect(await screen.findByText('network down')).toBeInTheDocument()
    expect(navigate).not.toHaveBeenCalled()
  })

  it('reflects the theme already on the document', () => {
    document.documentElement.classList.add('dark')
    render(<MobileAccountScreen />)
    // Reading the class rather than the settings key is what makes this right
    // for a user who has never chosen explicitly on an OS-dark phone.
    expect(screen.getByRole('button', { name: /dark/i })).toHaveAttribute(
      'aria-pressed',
      'true',
    )
  })

  it('applies and persists a theme change', async () => {
    render(<MobileAccountScreen />)
    await userEvent.click(screen.getByRole('button', { name: /dark/i }))

    expect(document.documentElement.classList.contains('dark')).toBe(true)
    expect(setSetting).toHaveBeenCalledWith('appearance.theme', 'dark')
  })

  it('offers none of the settings tree', () => {
    // `settings` stays false for this surface. The way that regresses is
    // someone growing this screen one reasonable-sounding section at a time.
    render(<MobileAccountScreen />)
    expect(screen.queryByText(/mcp|connector|shortcut/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/link.*account/i)).not.toBeInTheDocument()
  })

  it('shows no back button when rendered as the standalone /m/account route', () => {
    render(<MobileAccountScreen />)
    expect(
      screen.queryByRole('button', { name: /back to settings/i }),
    ).not.toBeInTheDocument()
  })

  it('shows a back button and calls onBack when embedded in the settings drill-in', async () => {
    const onBack = vi.fn()
    render(<MobileAccountScreen onBack={onBack} />)
    await userEvent.click(
      screen.getByRole('button', { name: /back to settings/i }),
    )
    expect(onBack).toHaveBeenCalled()
  })

  it('still signs out correctly when embedded with onBack', async () => {
    const onBack = vi.fn()
    render(<MobileAccountScreen onBack={onBack} />)
    await userEvent.click(screen.getByRole('button', { name: /sign out/i }))
    await userEvent.click(screen.getByRole('button', { name: /^sign out$/i }))

    await waitFor(() => expect(signOut).toHaveBeenCalledTimes(1))
    await waitFor(() =>
      expect(navigate).toHaveBeenCalledWith({
        to: '/auth',
        search: { redirect: undefined },
      }),
    )
  })
})
