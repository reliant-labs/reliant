/**
 * The forge-mode sign-in screen (AUTH_PROVIDER "forge"): password sign-in
 * through the store, generic sign-in errors, sign-up with the broker's
 * password policy, methods only where GET /auth/options offers them, and the
 * desktop app handing sign-in to the system browser.
 */
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AuthOptions } from '@/lib/auth/native-login'

const landAt = vi.fn()
vi.mock('../forgeLanding', () => ({ landAt: (path: string) => landAt(path) }))
vi.mock('@/lib/analytics', () => ({ trackEvent: vi.fn() }))
vi.mock('@/lib/shell', () => ({ isForgeShell: () => false }))

const store = {
  signIn: vi.fn(),
  installForgeIdentity: vi.fn(),
  refreshSession: vi.fn(),
  identity: null as unknown,
}
vi.mock('@/store/authStore', () => ({ useAuthStore: { getState: () => store } }))

vi.mock('@/lib/auth/native-login', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/auth/native-login')>()
  return {
    ...actual,
    fetchAuthOptions: vi.fn(),
    fetchPasswordPolicy: vi.fn(),
    signUp: vi.fn(),
    socialStart: vi.fn(),
    requestMagicLink: vi.fn(),
    consumeMagicLink: vi.fn(),
    passkeySignIn: vi.fn(),
    passkeysSupported: () => true,
  }
})

import * as native from '@/lib/auth/native-login'
import { ForgeSignIn } from '../ForgeSignIn'

const mocked = vi.mocked(native)

const DEV_OPTIONS: AuthOptions = { selfSignup: true, signup: 'open', methods: ['password'], social: [], mail: false }
const PROD_OPTIONS: AuthOptions = {
  selfSignup: true,
  signup: 'open',
  methods: ['password', 'magic_link'],
  social: [
    { type: 'github', label: 'GitHub' },
    { type: 'google', label: 'Google' },
  ],
  mail: true,
}
const IDENTITY = { authenticated: true, email: 'a@example.com', subject: 'u1' }

beforeEach(() => {
  vi.clearAllMocks()
  store.identity = null
  store.installForgeIdentity.mockResolvedValue(true)
  mocked.fetchAuthOptions.mockResolvedValue(DEV_OPTIONS)
  mocked.fetchPasswordPolicy.mockResolvedValue(null)
  window.history.replaceState({}, '', '/auth?redirect=%2Finbox')
})

afterEach(() => {
  delete (window as { electronAPI?: unknown }).electronAPI
  window.history.replaceState({}, '', '/')
})

function fill(label: RegExp, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } })
}

describe('ForgeSignIn — password sign-in', () => {
  it('signs in through the store and lands on the redirect', async () => {
    store.signIn.mockResolvedValue(undefined)
    render(<ForgeSignIn />)
    fill(/email address/i, 'a@example.com')
    fill(/^password$/i, 'hunter2hunter2')
    fireEvent.click(screen.getByRole('button', { name: /^sign in$/i }))

    await waitFor(() => expect(landAt).toHaveBeenCalledWith('/inbox'))
    expect(store.signIn).toHaveBeenCalledWith('a@example.com', 'hunter2hunter2')
  })

  it('ignores an off-site redirect and lands home', async () => {
    window.history.replaceState({}, '', '/auth?redirect=%2F%5Cevil.com')
    store.signIn.mockResolvedValue(undefined)
    render(<ForgeSignIn />)
    fill(/email address/i, 'a@example.com')
    fill(/^password$/i, 'pw')
    fireEvent.click(screen.getByRole('button', { name: /^sign in$/i }))

    await waitFor(() => expect(landAt).toHaveBeenCalledWith('/'))
  })

  it('reports a refused sign-in generically, never the broker’s reason', async () => {
    store.signIn.mockRejectedValue(new native.AuthError('No user with that email', 401))
    render(<ForgeSignIn />)
    fill(/email address/i, 'a@example.com')
    fill(/^password$/i, 'wrong')
    fireEvent.click(screen.getByRole('button', { name: /^sign in$/i }))

    expect(await screen.findByText(/didn’t match an account/i)).toBeTruthy()
    expect(screen.queryByText(/no user with that email/i)).toBeNull()
    expect(landAt).not.toHaveBeenCalled()
  })

  it('says so when the sign-in service is unreachable', async () => {
    store.signIn.mockRejectedValue(new native.AuthError('Could not reach the sign-in service.', 0))
    render(<ForgeSignIn />)
    fill(/email address/i, 'a@example.com')
    fill(/^password$/i, 'pw')
    fireEvent.click(screen.getByRole('button', { name: /^sign in$/i }))

    expect(await screen.findByText(/could not reach the sign-in service/i)).toBeTruthy()
  })
})

describe('ForgeSignIn — methods follow GET /auth/options', () => {
  it('password only, no mail: no social buttons, no emailed link, no forgot-password', async () => {
    render(<ForgeSignIn />)
    await waitFor(() => expect(mocked.fetchAuthOptions).toHaveBeenCalled())
    expect(screen.queryByTestId('oauth-button-github')).toBeNull()
    expect(screen.queryByTestId('oauth-button-google')).toBeNull()
    expect(screen.queryByRole('button', { name: /email me a sign-in link/i })).toBeNull()
    expect(screen.queryByRole('button', { name: /continue with a passkey/i })).toBeNull()
    expect(screen.queryByRole('button', { name: /forgot password/i })).toBeNull()
  })

  it('renders GitHub and Google from options.social and starts each with the way back', async () => {
    mocked.fetchAuthOptions.mockResolvedValue(PROD_OPTIONS)
    mocked.socialStart.mockResolvedValue(undefined)
    render(<ForgeSignIn />)

    fireEvent.click(await screen.findByTestId('oauth-button-github'))
    await waitFor(() => expect(mocked.socialStart).toHaveBeenCalledWith('github', '/inbox'))

    fireEvent.click(screen.getByTestId('oauth-button-google'))
    await waitFor(() => expect(mocked.socialStart).toHaveBeenCalledWith('google', '/inbox'))
  })

  it('shows a failed provider round trip', async () => {
    window.history.replaceState({}, '', '/auth/sign-in?error=social&social=failed')
    render(<ForgeSignIn />)
    expect(await screen.findByText(/that sign-in didn’t complete/i)).toBeTruthy()
  })

  it('emails a sign-in link and signs in with its code', async () => {
    mocked.fetchAuthOptions.mockResolvedValue(PROD_OPTIONS)
    mocked.requestMagicLink.mockResolvedValue('If that address can sign in, a link is on its way.')
    mocked.consumeMagicLink.mockResolvedValue(IDENTITY)
    render(<ForgeSignIn />)
    fill(/email address/i, 'a@example.com')
    fireEvent.click(await screen.findByRole('button', { name: /email me a sign-in link/i }))

    expect(await screen.findByText(/a link is on its way/i)).toBeTruthy()
    fill(/code from the email/i, '123456')
    fireEvent.click(screen.getByRole('button', { name: /^sign in$/i }))

    await waitFor(() => expect(landAt).toHaveBeenCalledWith('/inbox'))
    expect(mocked.consumeMagicLink).toHaveBeenCalledWith('123456')
    expect(store.installForgeIdentity).toHaveBeenCalledWith(IDENTITY)
  })
})

describe('ForgeSignIn — sign-up', () => {
  const POLICY = { min_length: 8, has_uppercase: true, has_lowercase: false, has_number: false, has_symbol: false }

  async function openSignUp() {
    render(<ForgeSignIn />)
    fireEvent.click(await screen.findByTestId('auth-mode-toggle'))
    expect(screen.getByRole('heading', { name: /create your account/i })).toBeTruthy()
  }

  it('checks the broker’s policy before sending anything', async () => {
    mocked.fetchPasswordPolicy.mockResolvedValue(POLICY)
    await openSignUp()
    expect(await screen.findByText('At least 8 characters')).toBeTruthy()
    fill(/email address/i, 'new@example.com')
    fill(/^password$/i, 'short')
    fireEvent.click(screen.getByRole('button', { name: /create account/i }))

    expect(await screen.findByText(/doesn’t meet every requirement/i)).toBeTruthy()
    expect(mocked.signUp).not.toHaveBeenCalled()
  })

  it('installs the identity the broker answers with and lands', async () => {
    mocked.signUp.mockResolvedValue(IDENTITY)
    await openSignUp()
    fill(/email address/i, 'new@example.com')
    fill(/^password$/i, 'Longenough1')
    fireEvent.click(screen.getByRole('button', { name: /create account/i }))

    await waitFor(() => expect(landAt).toHaveBeenCalledWith('/inbox'))
    expect(mocked.signUp).toHaveBeenCalledWith({ email: 'new@example.com', password: 'Longenough1' })
    expect(store.installForgeIdentity).toHaveBeenCalledWith(IDENTITY)
  })

  it('shows "check your email" when the broker mailed a confirmation', async () => {
    mocked.signUp.mockResolvedValue({ verify: 'sent', message: 'Check your inbox to confirm.' })
    await openSignUp()
    fill(/email address/i, 'new@example.com')
    fill(/^password$/i, 'Longenough1')
    fireEvent.click(screen.getByRole('button', { name: /create account/i }))

    expect(await screen.findByText('Check your inbox to confirm.')).toBeTruthy()
    expect(landAt).not.toHaveBeenCalled()
  })

  it('reports a refused password specifically', async () => {
    mocked.signUp.mockRejectedValue(new native.AuthError('Password needs a symbol.', 400, ['symbol']))
    await openSignUp()
    fill(/email address/i, 'new@example.com')
    fill(/^password$/i, 'Longenough1')
    fireEvent.click(screen.getByRole('button', { name: /create account/i }))

    expect(await screen.findByText('Password needs a symbol.')).toBeTruthy()
  })
})

describe('ForgeSignIn — desktop', () => {
  it('hands sign-in to the system browser and installs what comes back', async () => {
    const signIn = vi.fn().mockResolvedValue(IDENTITY)
    ;(window as { electronAPI?: unknown }).electronAPI = { forgeAuth: { signIn } }
    render(<ForgeSignIn />)

    expect(screen.queryByLabelText(/email address/i)).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: /sign in with your browser/i }))

    await waitFor(() => expect(landAt).toHaveBeenCalledWith('/inbox'))
    expect(signIn).toHaveBeenCalledTimes(1)
    expect(store.installForgeIdentity).toHaveBeenCalledWith(IDENTITY)
  })

  it('signs in in the window on a desktop app without the bridge', async () => {
    ;(window as { electronAPI?: unknown }).electronAPI = {}
    render(<ForgeSignIn />)
    expect(screen.getByLabelText(/email address/i)).toBeTruthy()
    await waitFor(() => expect(mocked.fetchAuthOptions).toHaveBeenCalled())
    await screen.findByTestId('auth-mode-toggle')
  })
})
