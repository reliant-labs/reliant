/**
 * Remote sign-in has to acknowledge the click.
 *
 * Starting a provider sign-in is slow: the app talks to the backend, which
 * opens a browser, and the redirect can take seconds. With no visible change
 * the screen looks inert and users click a second time. Two things have to be
 * true while that is in flight:
 *
 *  1. The provider the user actually clicked shows a pending state.
 *  2. The OTHER provider does not. A single shared flag put every button into
 *     "Connecting..." at once, which reads as though the app is signing in with
 *     something the user never chose.
 */
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const mockNavigate = vi.fn()

vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => mockNavigate,
  useSearch: () => ({}),
}))

/** Resolves only when the test says so, standing in for a slow redirect. */
let releaseGoogle: () => void
const signInWithGoogle = vi.fn(
  () =>
    new Promise<void>((resolve) => {
      releaseGoogle = resolve
    }),
)
const signInWithGithub = vi.fn().mockResolvedValue(undefined)

vi.mock('@/store/authStore', () => ({
  useAuthStore: () => ({
    signIn: vi.fn(),
    signUp: vi.fn(),
    signInAnonymously: vi.fn(),
    signInWithGoogle,
    signInWithGithub,
    signInWithApple: vi.fn(),
  }),
}))

import { AuthScreen } from '../AuthScreen'

const buttonFor = (name: RegExp) => screen.getByRole('button', { name })

beforeEach(() => {
  vi.clearAllMocks()
})

describe('AuthScreen remote sign-in pending state', () => {
  it('shows a pending state on the provider that was clicked', async () => {
    render(<AuthScreen />)

    fireEvent.click(buttonFor(/continue with google/i))

    await waitFor(() => {
      expect(screen.getByTestId('oauth-button-google')).toHaveAttribute(
        'aria-busy',
        'true',
      )
    })

    releaseGoogle()
  })

  it('leaves the other provider idle rather than showing every button as busy', async () => {
    render(<AuthScreen />)

    fireEvent.click(buttonFor(/continue with google/i))

    await waitFor(() => {
      expect(screen.getByTestId('oauth-button-google')).toHaveAttribute(
        'aria-busy',
        'true',
      )
    })

    // GitHub was not clicked: it must not claim to be connecting.
    expect(screen.getByTestId('oauth-button-github')).toHaveAttribute(
      'aria-busy',
      'false',
    )

    releaseGoogle()
  })

  it('disables both providers while one is in flight so a stray click cannot start a second sign-in', async () => {
    render(<AuthScreen />)

    fireEvent.click(buttonFor(/continue with google/i))

    await waitFor(() => {
      expect(screen.getByTestId('oauth-button-github')).toBeDisabled()
    })

    fireEvent.click(screen.getByTestId('oauth-button-github'))
    expect(signInWithGithub).not.toHaveBeenCalled()

    releaseGoogle()
  })
})
