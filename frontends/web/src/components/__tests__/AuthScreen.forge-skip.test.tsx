/**
 * "Skip for now" on /auth. It signs in as a guest and then navigates. On the
 * forge host the guest goes straight to their apps (/projects?show=apps) (whose empty state, ForgeFirstRun,
 * is the no-account deploy): there is no onboarding to start there, and `/` was
 * a second hop that, racing the sign-in screen's own bounce, ended on a blank
 * page. On Reliant it still goes to `/`, where onboarding starts.
 */
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const mockNavigate = vi.fn()
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => mockNavigate,
  useSearch: () => ({}),
}))

const signInAnonymously = vi.fn().mockResolvedValue(undefined)
vi.mock('@/store/authStore', () => ({
  useAuthStore: () => ({
    user: null,
    signIn: vi.fn(),
    sendEmailSignInCode: vi.fn(),
    verifyEmailSignInCode: vi.fn(),
    signInAnonymously,
    signInWithGoogle: vi.fn(),
    signInWithGithub: vi.fn(),
    signInWithApple: vi.fn(),
    signOut: vi.fn(),
  }),
}))

const isForgeShell = vi.fn(() => false)
vi.mock('@/lib/shell', () => ({ isForgeShell: () => isForgeShell() }))

import { AuthScreen } from '../AuthScreen'

beforeEach(() => {
  vi.clearAllMocks()
  isForgeShell.mockReturnValue(false)
})

describe('AuthScreen — Skip for now', () => {
  it('on the forge host, lands the guest on their apps (Projects)', async () => {
    isForgeShell.mockReturnValue(true)
    render(<AuthScreen />)
    fireEvent.click(screen.getByRole('button', { name: /skip for now/i }))

    await waitFor(() => expect(mockNavigate).toHaveBeenCalledWith({ to: '/projects', search: { show: 'apps' } }))
    expect(signInAnonymously).toHaveBeenCalledTimes(1)
  })

  it('on the Reliant host, still goes to / where onboarding starts', async () => {
    render(<AuthScreen />)
    fireEvent.click(screen.getByRole('button', { name: /skip for now/i }))

    await waitFor(() => expect(mockNavigate).toHaveBeenCalledWith({ to: '/', search: {} }))
  })
})
