/**
 * UpgradeAccount — the screen that gets a free-tier user OFF an anonymous
 * session without losing the account they already have.
 *
 * Two things are load-bearing here and easy to regress:
 *
 *  1. Provider coverage. /auth offers GitHub, Google and email+password; a user
 *     who signed up expecting GitHub and lands on a Google-only upgrade page is
 *     simply stuck. There is no "Skip for now" on this screen either — that
 *     button MAKES an anonymous session, which is the exact state this page
 *     exists to escape.
 *
 *  2. The mechanics are linkIdentity, not sign-in. The user's chats and
 *     workspaces live on the anonymous account; signing them into a different
 *     one strands that work. So the OAuth buttons must call linkOAuthIdentity,
 *     and they must carry `returnTo` through the provider round-trip so the
 *     user lands back on billing rather than the app root.
 */
import { render, screen, fireEvent } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const mockNavigate = vi.fn()
let mockSearch: { returnTo?: string } = {}

vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => mockNavigate,
  useSearch: () => mockSearch,
}))

vi.mock('@/lib/logger', () => ({
  logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

const linkOAuthIdentity = vi.fn().mockResolvedValue(undefined)
const signUp = vi.fn()
const initialize = vi.fn()

type MockUser = { is_anonymous?: boolean; email?: string } | null
let mockUser: MockUser = { is_anonymous: true }

vi.mock('@/store/authStore', () => ({
  useAuthStore: () => ({
    user: mockUser,
    initialized: true,
    loading: false,
    initialize,
    linkOAuthIdentity,
    signUp,
    sendEmailVerificationOTP: vi.fn(),
    verifyEmailOTP: vi.fn(),
  }),
}))

import { UpgradeAccount } from '../UpgradeAccount'

beforeEach(() => {
  vi.clearAllMocks()
  mockUser = { is_anonymous: true }
  mockSearch = {}
})

describe('UpgradeAccount', () => {
  it('offers every provider /auth supports', () => {
    render(<UpgradeAccount />)

    expect(screen.getByRole('button', { name: /Continue with GitHub/i })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Continue with Google/i })).toBeInTheDocument()
    expect(screen.getByLabelText(/Email address/i)).toBeInTheDocument()
    expect(screen.getByLabelText('Password')).toBeInTheDocument()
  })

  it('does not offer "Skip for now" — that would create the very session being escaped', () => {
    render(<UpgradeAccount />)

    expect(screen.queryByText(/Skip for now/i)).not.toBeInTheDocument()
  })

  it('frames the page as saving the existing account, not signing in or up', () => {
    render(<UpgradeAccount />)

    expect(screen.queryByRole('heading', { name: /^Sign in$/i })).not.toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: /^Sign up$/i })).not.toBeInTheDocument()
    expect(screen.getByText(/chats and workspaces stay exactly as they are/i)).toBeInTheDocument()
  })

  it('LINKS the provider onto the current account, carrying returnTo through', () => {
    mockSearch = { returnTo: '/settings/billing' }
    render(<UpgradeAccount />)

    fireEvent.click(screen.getByRole('button', { name: /Continue with GitHub/i }))

    expect(linkOAuthIdentity).toHaveBeenCalledWith('github', {
      source: 'link',
      returnTo: '/settings/billing',
    })
  })

  it('drops an off-origin returnTo rather than threading it into the OAuth state', () => {
    mockSearch = { returnTo: '//evil.example.com/steal' }
    render(<UpgradeAccount />)

    fireEvent.click(screen.getByRole('button', { name: /Continue with Google/i }))

    expect(linkOAuthIdentity).toHaveBeenCalledWith('google', { source: 'link' })
  })

  it('sends an already-upgraded user to returnTo instead of rendering the form', () => {
    const assign = vi.fn()
    Object.defineProperty(window, 'location', {
      value: { ...window.location, assign },
      writable: true,
      configurable: true,
    })
    mockUser = { is_anonymous: false, email: 'someone@example.com' }
    mockSearch = { returnTo: '/settings/billing' }

    render(<UpgradeAccount />)

    expect(assign).toHaveBeenCalledWith('/settings/billing')
  })
})
