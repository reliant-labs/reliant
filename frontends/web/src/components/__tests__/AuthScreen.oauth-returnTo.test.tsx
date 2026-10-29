/**
 * A provider sign-in keeps the flow the person was sent to sign in for.
 *
 * The device-code approval page (/oauth/device?user_code=…, opened from a link
 * a terminal printed, often on a phone) and the CLI-login consent page send a
 * signed-out person to /auth?redirect=<that page>. A Google, GitHub or Apple
 * sign-in leaves the app for the provider and comes back through
 * /auth/callback, so the redirect has to ride along as `returnTo` — or the
 * person lands on the home page and the approval they came to make is gone.
 */
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

let search: { redirect?: string } = {}
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => vi.fn(),
  useSearch: () => search,
}))

const signInWithGoogle = vi.fn().mockResolvedValue(undefined)
const signInWithGithub = vi.fn().mockResolvedValue(undefined)
const signInWithApple = vi.fn().mockResolvedValue(undefined)
vi.mock('@/store/authStore', () => ({
  useAuthStore: () => ({
    signIn: vi.fn(),
    sendEmailSignInCode: vi.fn(),
    verifyEmailSignInCode: vi.fn(),
    signInAnonymously: vi.fn(),
    signInWithGoogle,
    signInWithGithub,
    signInWithApple,
  }),
}))

import { AuthScreen } from '../AuthScreen'

beforeEach(() => {
  vi.clearAllMocks()
  search = {}
})

describe('AuthScreen provider sign-in', () => {
  it('carries the device approval page through the provider as returnTo', async () => {
    search = { redirect: '/oauth/device?user_code=BDFG-HJKL' }
    render(<AuthScreen />)
    fireEvent.click(screen.getByRole('button', { name: /continue with google/i }))
    await waitFor(() =>
      expect(signInWithGoogle).toHaveBeenCalledWith({ returnTo: '/oauth/device?user_code=BDFG-HJKL' }),
    )
  })

  it('carries it for GitHub too', async () => {
    search = { redirect: '/oauth/device?user_code=BDFG-HJKL' }
    render(<AuthScreen />)
    fireEvent.click(screen.getByRole('button', { name: /continue with github/i }))
    await waitFor(() =>
      expect(signInWithGithub).toHaveBeenCalledWith({ returnTo: '/oauth/device?user_code=BDFG-HJKL' }),
    )
  })

  it('never forwards an off-origin redirect', async () => {
    search = { redirect: '//evil.example/x' }
    render(<AuthScreen />)
    fireEvent.click(screen.getByRole('button', { name: /continue with google/i }))
    await waitFor(() => expect(signInWithGoogle).toHaveBeenCalledWith(undefined))
  })
})
