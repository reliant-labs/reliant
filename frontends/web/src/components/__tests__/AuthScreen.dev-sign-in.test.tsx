/**
 * The sign-in screen offers dev sign-in only in a dev server built with
 * VITE_DEV_SIGN_IN=true (lib/devSignIn.ts), and a token that could never work
 * is reported there instead of being stored.
 */
import { render, screen, fireEvent } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => vi.fn(),
  useSearch: () => ({}),
}))

vi.mock('@/store/authStore', () => ({
  useAuthStore: () => ({
    signIn: vi.fn(),
    sendEmailSignInCode: vi.fn(),
    verifyEmailSignInCode: vi.fn(),
    signInAnonymously: vi.fn(),
    signInWithGoogle: vi.fn(),
    signInWithGithub: vi.fn(),
    signInWithApple: vi.fn(),
  }),
}))

vi.mock('@/lib/analytics', () => ({ trackEvent: vi.fn() }))

afterEach(() => {
  vi.unstubAllEnvs()
  vi.resetModules()
})

async function renderAuthScreen() {
  const { AuthScreen } = await import('../AuthScreen')
  render(<AuthScreen />)
}

describe('AuthScreen dev sign-in', () => {
  it('is absent from a build without the flag', async () => {
    await renderAuthScreen()
    expect(screen.queryByTestId('dev-sign-in')).toBeNull()
  })

  it('is offered with the flag, and refuses a token that is not one', async () => {
    vi.stubEnv('VITE_DEV_SIGN_IN', 'true')
    await renderAuthScreen()
    expect(screen.getByTestId('dev-sign-in')).toBeTruthy()
    fireEvent.change(screen.getByLabelText('Dev token'), { target: { value: 'not-a-token' } })
    fireEvent.click(screen.getByRole('button', { name: 'Sign in with dev token' }))
    expect(await screen.findByText(/three dot-separated parts/)).toBeTruthy()
  })
})
