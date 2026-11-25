/**
 * The forge-mode pages the login broker's emails and the Google/GitHub round
 * trip open: /auth/reset, /auth/verify, /auth/magic, /auth/social/callback,
 * and the forgot-password screen that mails the reset link.
 */
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const landAt = vi.fn()
vi.mock('../forgeLanding', () => ({ landAt: (path: string) => landAt(path) }))
vi.mock('@/lib/shell', () => ({ isForgeShell: () => false }))

const store = { installForgeIdentity: vi.fn() }
vi.mock('@/store/authStore', () => ({ useAuthStore: { getState: () => store } }))

vi.mock('@/lib/auth/native-login', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/auth/native-login')>()
  return {
    ...actual,
    fetchPasswordPolicy: vi.fn(),
    forgotPassword: vi.fn(),
    resetPassword: vi.fn(),
    verifyEmail: vi.fn(),
    consumeMagicLink: vi.fn(),
    socialComplete: vi.fn(),
  }
})

import * as native from '@/lib/auth/native-login'
import { ForgeForgotPassword } from '../ForgeForgotPassword'
import { ForgeResetPasswordForm } from '../ForgeResetPassword'
import { ForgeVerifyEmail } from '../ForgeVerifyEmail'
import { ForgeMagicLink } from '../ForgeMagicLink'
import { ForgeSocialCallback } from '../ForgeSocialCallback'
import { rememberReturnTo } from '../forgeAuthLib'

const mocked = vi.mocked(native)
const IDENTITY = { authenticated: true, email: 'a@example.com', subject: 'u1' }

beforeEach(() => {
  vi.clearAllMocks()
  localStorage.clear()
  store.installForgeIdentity.mockImplementation(async (identity: { authenticated: boolean }) => identity.authenticated)
  mocked.fetchPasswordPolicy.mockResolvedValue(null)
})

function fill(label: RegExp, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } })
}

describe('ForgeForgotPassword', () => {
  it('mails the link and shows the broker’s answer', async () => {
    mocked.forgotPassword.mockResolvedValue('If that address has an account, a reset link is on its way.')
    render(<ForgeForgotPassword onBackToSignIn={vi.fn()} />)
    fill(/email address/i, 'a@example.com')
    fireEvent.click(screen.getByRole('button', { name: /send reset link/i }))

    expect(await screen.findByText(/a reset link is on its way/i)).toBeTruthy()
    expect(mocked.forgotPassword).toHaveBeenCalledWith('a@example.com')
  })
})

describe('ForgeResetPasswordForm (/auth/reset)', () => {
  it('sets the password, installs the session and lands', async () => {
    mocked.resetPassword.mockResolvedValue(IDENTITY)
    render(<ForgeResetPasswordForm userID="u1" code="c1" />)
    fill(/^new password$/i, 'Brand-new-1')
    fill(/confirm new password/i, 'Brand-new-1')
    fireEvent.click(screen.getByRole('button', { name: /set password/i }))

    await waitFor(() => expect(landAt).toHaveBeenCalledWith('/'))
    expect(mocked.resetPassword).toHaveBeenCalledWith({ userID: 'u1', code: 'c1', password: 'Brand-new-1' })
    expect(store.installForgeIdentity).toHaveBeenCalledWith(IDENTITY)
  })

  it('sends a set-but-signed-out reset to sign in', async () => {
    mocked.resetPassword.mockResolvedValue({ authenticated: false })
    render(<ForgeResetPasswordForm userID="u1" code="c1" />)
    fill(/^new password$/i, 'Brand-new-1')
    fill(/confirm new password/i, 'Brand-new-1')
    fireEvent.click(screen.getByRole('button', { name: /set password/i }))

    await waitFor(() => expect(landAt).toHaveBeenCalledWith('/auth'))
  })

  it('shows the broker’s specific refusal', async () => {
    mocked.resetPassword.mockRejectedValue(new native.AuthError('That reset link is not valid.', 400))
    render(<ForgeResetPasswordForm userID="u1" code="c1" />)
    fill(/^new password$/i, 'Brand-new-1')
    fill(/confirm new password/i, 'Brand-new-1')
    fireEvent.click(screen.getByRole('button', { name: /set password/i }))

    expect(await screen.findByText('That reset link is not valid.')).toBeTruthy()
    expect(landAt).not.toHaveBeenCalled()
  })

  it('refuses two different passwords without a request', async () => {
    render(<ForgeResetPasswordForm userID="u1" code="c1" />)
    fill(/^new password$/i, 'Brand-new-1')
    fill(/confirm new password/i, 'Brand-new-2')
    fireEvent.click(screen.getByRole('button', { name: /set password/i }))

    expect((await screen.findAllByText(/don’t match/i)).length).toBeGreaterThan(0)
    expect(mocked.resetPassword).not.toHaveBeenCalled()
  })

  it('says so when the link is incomplete', () => {
    render(<ForgeResetPasswordForm userID={null} code="c1" />)
    expect(screen.getByText(/this reset link is incomplete/i)).toBeTruthy()
  })
})

describe('ForgeVerifyEmail (/auth/verify)', () => {
  it('does nothing until pressed, then confirms with the link’s values', async () => {
    mocked.verifyEmail.mockResolvedValue({ verified: true, enroll: false })
    render(<ForgeVerifyEmail userID="u1" code="c1" />)
    expect(mocked.verifyEmail).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: /confirm my email/i }))

    expect(await screen.findByText(/your email is confirmed/i)).toBeTruthy()
    expect(mocked.verifyEmail).toHaveBeenCalledWith('u1', 'c1')
    expect(store.installForgeIdentity).not.toHaveBeenCalled()
  })

  it('offers the reset form when the sign-up password was not saved', async () => {
    mocked.verifyEmail.mockResolvedValue({
      verified: true,
      enroll: false,
      password: 'not_set',
      reset: { userID: 'u1', code: 'r1' },
    })
    render(<ForgeVerifyEmail userID="u1" code="c1" />)
    fireEvent.click(screen.getByRole('button', { name: /confirm my email/i }))

    expect(await screen.findByRole('heading', { name: /choose a new password/i })).toBeTruthy()
  })
})

describe('ForgeMagicLink (/auth/magic)', () => {
  it('signs in only when pressed, and lands where the sign-in screen was headed', async () => {
    rememberReturnTo('/oauth/cli?x=1')
    mocked.consumeMagicLink.mockResolvedValue(IDENTITY)
    render(<ForgeMagicLink code="m1" />)
    expect(mocked.consumeMagicLink).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: /^sign in$/i }))

    await waitFor(() => expect(landAt).toHaveBeenCalledWith('/oauth/cli?x=1'))
    expect(mocked.consumeMagicLink).toHaveBeenCalledWith('m1')
    expect(store.installForgeIdentity).toHaveBeenCalledWith(IDENTITY)
  })

  it('explains a link opened in another browser', async () => {
    mocked.consumeMagicLink.mockRejectedValue(new native.AuthError('other browser', 400, [], 'magic_other_browser'))
    render(<ForgeMagicLink code="m1" />)
    fireEvent.click(screen.getByRole('button', { name: /^sign in$/i }))

    expect(await screen.findByText(/only works in the browser where you asked for it/i)).toBeTruthy()
  })
})

describe('ForgeSocialCallback (/auth/social/callback)', () => {
  it('completes the sign-in, installs it and lands on its returnTo', async () => {
    mocked.socialComplete.mockResolvedValue({ identity: IDENTITY, returnTo: '/inbox' })
    render(<ForgeSocialCallback id="i1" token="t1" />)

    await waitFor(() => expect(landAt).toHaveBeenCalledWith('/inbox'))
    expect(mocked.socialComplete).toHaveBeenCalledTimes(1)
    expect(mocked.socialComplete).toHaveBeenCalledWith('i1', 't1')
    expect(store.installForgeIdentity).toHaveBeenCalledWith(IDENTITY)
  })

  it('never lands off-site', async () => {
    mocked.socialComplete.mockResolvedValue({ identity: IDENTITY, returnTo: '//evil.com' })
    render(<ForgeSocialCallback id="i1" token="t1" />)
    await waitFor(() => expect(landAt).toHaveBeenCalledWith('/'))
  })

  it('goes back to settings after a link, without installing an identity', async () => {
    mocked.socialComplete.mockResolvedValue({
      linked: true,
      provider: 'github',
      returnTo: '/settings/account',
      identity: { authenticated: true, subject: 'u1' },
    } as never)
    render(<ForgeSocialCallback id="i1" token="t1" />)

    await waitFor(() => expect(landAt).toHaveBeenCalledWith('/settings/account'))
    expect(store.installForgeIdentity).not.toHaveBeenCalled()
  })

  it('explains an account already connected elsewhere', async () => {
    mocked.socialComplete.mockRejectedValue(new native.AuthError('taken', 409, [], 'social_taken'))
    render(<ForgeSocialCallback id="i1" token="t1" />)
    expect(await screen.findByText(/already connected to a different reliant account/i)).toBeTruthy()
  })

  it('shows "check your email" when the address must be confirmed first', async () => {
    mocked.socialComplete.mockResolvedValue({ verify: 'sent', message: 'Confirm your address first.' })
    render(<ForgeSocialCallback id="i1" token="t1" />)
    expect(await screen.findByText('Confirm your address first.')).toBeTruthy()
  })

  it('posts nothing when the round trip came back incomplete', () => {
    render(<ForgeSocialCallback id={null} token="t1" />)
    expect(screen.getByText(/that sign-in didn’t complete/i)).toBeTruthy()
    expect(mocked.socialComplete).not.toHaveBeenCalled()
  })
})
