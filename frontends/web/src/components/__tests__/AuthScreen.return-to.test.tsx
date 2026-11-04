/**
 * Provider sign-in must carry the page's return path across the redirect. The
 * claim page sends an existing user to /auth?redirect=/claim; an email sign-in
 * already returned there, but a provider sign-in dropped it, so a person who
 * signed in with Google landed on "/" and the claim they came to finish was
 * left waiting in storage.
 */
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

let search: Record<string, string> = {}
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

describe('AuthScreen provider sign-in return path', () => {
  it.each([
    [/continue with google/i, signInWithGoogle],
    [/continue with github/i, signInWithGithub],
  ] as const)('%s returns to a same-origin redirect', async (name, fn) => {
    search = { redirect: '/claim' }
    render(<AuthScreen />)
    fireEvent.click(screen.getByRole('button', { name }))
    await waitFor(() => expect(fn).toHaveBeenCalledWith({ returnTo: '/claim' }))
  })

  it('passes nothing when there is nowhere to return to, or the target is not same-origin', async () => {
    render(<AuthScreen />)
    fireEvent.click(screen.getByRole('button', { name: /continue with google/i }))
    await waitFor(() => expect(signInWithGoogle).toHaveBeenCalledWith(undefined))

    search = { redirect: '//evil.example/x' }
    render(<AuthScreen />)
    fireEvent.click(screen.getAllByRole('button', { name: /continue with github/i })[1])
    await waitFor(() => expect(signInWithGithub).toHaveBeenCalledWith(undefined))
  })
})
