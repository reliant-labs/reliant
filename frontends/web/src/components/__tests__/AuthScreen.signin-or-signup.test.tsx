/**
 * One form that signs you in OR signs you up — without telling anyone which
 * emails are registered.
 *
 * The property is unchanged from the password version of this screen; the way
 * it is obtained is not, and that is the point of the rewrite.
 *
 * The old flow attempted a sign-in and branched on GoTrue's error. It got
 * ambiguity from `invalid_credentials` meaning both "no such user" and "wrong
 * password", and it had to spend a carefully-worded prompt to keep the two
 * populations looking alike. Tests had to pin that wording.
 *
 * `signInWithOtp({ shouldCreateUser: true })` answers a registered address and
 * an unregistered one with the same empty 200. The client gets no branch to
 * leak from, so the screen renders identically for both because there is
 * nothing else it COULD render. The assertions below are correspondingly
 * stronger: not "the two prompts match" but "the same single request is sent
 * and the same markup comes back".
 *
 * ── Tests carried over from the password version ─────────────────────────
 *
 * Kept, retargeted: single Continue action with no sign-up toggle; identical
 * UI for both populations; never creates an account behind the user's back;
 * a password field that declares `current-password`; "Skip for now".
 *
 * Dropped, with reasons:
 *   - "renders new-password on FIRST paint when the user says they are new"
 *     and "remounts the password field so Chrome re-runs field detection".
 *     Both pinned the machinery that existed to make Chrome's generation offer
 *     reachable from a merged form. There is no account-creation password
 *     field left to generate INTO — accounts are created by emailing a code —
 *     so the behaviour they described is gone rather than regressed.
 *   - "creates the account only after the user explicitly asks" and "keeps a
 *     forgot-password escape on the fork". Both described the ambiguous
 *     invalid_credentials fork. There is no fork: an unknown address is not an
 *     error on the code path. Its safety property — never sign someone up
 *     silently — is now covered by asserting the ONE request the screen makes.
 */
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const mockNavigate = vi.fn()

vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => mockNavigate,
  useSearch: () => ({}),
}))

const signIn = vi.fn()
const sendEmailSignInCode = vi.fn()
const verifyEmailSignInCode = vi.fn()
const signInAnonymously = vi.fn().mockResolvedValue(undefined)

vi.mock('@/store/authStore', () => ({
  useAuthStore: () => ({
    user: null,
    signIn,
    sendEmailSignInCode,
    verifyEmailSignInCode,
    signInAnonymously,
    signInWithGoogle: vi.fn(),
    signInWithGithub: vi.fn(),
    signInWithApple: vi.fn(),
    resendSignupVerification: vi.fn(),
    verifyEmailOTP: vi.fn(),
    signOut: vi.fn(),
  }),
}))

import { AuthScreen } from '../AuthScreen'

const REGISTERED = 'returning@example.com'
const UNREGISTERED = 'brand-new@example.com'

const enterEmail = (address: string) =>
  fireEvent.change(screen.getByLabelText(/email address/i), {
    target: { value: address },
  })

const submit = () =>
  fireEvent.click(screen.getByRole('button', { name: /^continue$/i }))

beforeEach(() => {
  vi.clearAllMocks()
  // GoTrue's answer to signInWithOtp is the same for both populations, so the
  // mock does not distinguish them either — a mock that branched on the
  // address would be testing a server that does not exist.
  sendEmailSignInCode.mockResolvedValue(undefined)
  verifyEmailSignInCode.mockResolvedValue(undefined)
  signIn.mockResolvedValue(undefined)
})

describe('AuthScreen — email code is the default path', () => {
  it('asks only for an email, with a single Continue action', () => {
    render(<AuthScreen />)

    expect(screen.getByLabelText(/email address/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /^continue$/i })).toBeInTheDocument()
    // No password on first paint: that absence is what removes the
    // autocomplete dilemma this screen was rewritten to escape.
    expect(screen.queryByLabelText(/^password$/i)).not.toBeInTheDocument()
    // And no sign-up fork, which is what the merge removed and the
    // generation workaround kept putting back.
    expect(screen.queryByRole('button', { name: /^sign up$/i })).not.toBeInTheDocument()
    expect(screen.queryByText(/don't have an account/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/new to reliant/i)).not.toBeInTheDocument()
  })

  it('sends a code and shows the 6-digit entry screen', async () => {
    render(<AuthScreen />)
    enterEmail(UNREGISTERED)
    submit()

    await waitFor(() =>
      expect(sendEmailSignInCode).toHaveBeenCalledWith(UNREGISTERED, undefined),
    )
    expect(await screen.findByLabelText(/verification code/i)).toBeInTheDocument()
  })

  it('signs the user in once the code verifies', async () => {
    render(<AuthScreen />)
    enterEmail(REGISTERED)
    submit()

    const codeInput = await screen.findByLabelText(/verification code/i)
    fireEvent.change(codeInput, { target: { value: '123456' } })
    fireEvent.click(screen.getByRole('button', { name: /^sign in$/i }))

    await waitFor(() =>
      expect(verifyEmailSignInCode).toHaveBeenCalledWith(REGISTERED, '123456'),
    )
    await waitFor(() => expect(mockNavigate).toHaveBeenCalled())
  })

  it('tells the user the same email carries a link', async () => {
    render(<AuthScreen />)
    enterEmail(REGISTERED)
    submit()

    // Both routes finish the same sign-in. A screen that mentions only the
    // code strands the user whose mail client swallowed the digits, and one
    // that mentions only the link strands the user in a different browser.
    expect(
      await screen.findByTestId('email-code-description'),
    ).toHaveTextContent(/link in the same email/i)
  })

  it('keeps the check-spam note on the screen announcing the email', async () => {
    render(<AuthScreen />)
    enterEmail(REGISTERED)
    submit()

    expect(await screen.findByText(/spam or junk folder/i)).toBeInTheDocument()
  })
})

describe('AuthScreen — anti-enumeration', () => {
  it('makes the identical request for a registered and an unregistered address', async () => {
    const { unmount } = render(<AuthScreen />)
    enterEmail(REGISTERED)
    submit()
    await waitFor(() => expect(sendEmailSignInCode).toHaveBeenCalledTimes(1))
    const registeredCall = sendEmailSignInCode.mock.calls[0]
    unmount()

    vi.clearAllMocks()
    sendEmailSignInCode.mockResolvedValue(undefined)

    render(<AuthScreen />)
    enterEmail(UNREGISTERED)
    submit()
    await waitFor(() => expect(sendEmailSignInCode).toHaveBeenCalledTimes(1))

    // One call, one shape, differing only in the address the user typed. There
    // is no second request whose presence or absence could be observed.
    expect(sendEmailSignInCode.mock.calls[0].slice(1)).toEqual(
      registeredCall.slice(1),
    )
    expect(signIn).not.toHaveBeenCalled()
  })

  it('renders byte-identical UI for a registered and an unregistered address', async () => {
    const { unmount, container } = render(<AuthScreen />)
    enterEmail(REGISTERED)
    submit()
    await screen.findByLabelText(/verification code/i)
    const registeredMarkup = container.innerHTML.replaceAll(REGISTERED, 'ADDRESS')
    unmount()

    const second = render(<AuthScreen />)
    enterEmail(UNREGISTERED)
    submit()
    await screen.findByLabelText(/verification code/i)
    const unregisteredMarkup = second.container.innerHTML.replaceAll(
      UNREGISTERED,
      'ADDRESS',
    )

    // The address itself is normalised away — the user typed it, so echoing it
    // back reveals nothing. Everything else must match exactly.
    expect(unregisteredMarkup).toBe(registeredMarkup)
  })

  it('never creates an account behind the user’s back', async () => {
    render(<AuthScreen />)
    enterEmail(UNREGISTERED)
    submit()

    await waitFor(() => expect(sendEmailSignInCode).toHaveBeenCalledTimes(1))
    // The ONE request the screen makes. It mails a code; it does not attempt a
    // password sign-in first and it does not silently provision an account
    // from a failure. `shouldCreateUser` lives in the store, where the request
    // is built, so the account is created only when the user proves the
    // address by returning a code.
    expect(sendEmailSignInCode).toHaveBeenCalledTimes(1)
    expect(signIn).not.toHaveBeenCalled()
  })
})

describe('AuthScreen — password is the secondary path', () => {
  it('reaches the password screen with no network call, so the toggle leaks nothing', () => {
    render(<AuthScreen />)
    fireEvent.click(screen.getByTestId('auth-method-toggle'))

    expect(sendEmailSignInCode).not.toHaveBeenCalled()
    expect(signIn).not.toHaveBeenCalled()
    expect(screen.getByLabelText(/^password$/i)).toBeInTheDocument()
  })

  it('declares current-password, which is now simply correct', () => {
    render(<AuthScreen />)
    fireEvent.click(screen.getByTestId('auth-method-toggle'))

    // Account creation happens on the code path, so this field is only ever an
    // EXISTING password. The token that suppressed saved-credential autofill
    // for returning users has no reason to appear on any screen here.
    expect(screen.getByLabelText(/^password$/i)).toHaveAttribute(
      'autocomplete',
      'current-password',
    )
  })

  it('signs in with a password when the user chooses to', async () => {
    render(<AuthScreen />)
    fireEvent.click(screen.getByTestId('auth-method-toggle'))
    enterEmail(REGISTERED)
    fireEvent.change(screen.getByLabelText(/^password$/i), {
      target: { value: 'Sup3rStr0ng!pass' },
    })
    fireEvent.click(screen.getByRole('button', { name: /^sign in$/i }))

    await waitFor(() =>
      expect(signIn).toHaveBeenCalledWith(REGISTERED, 'Sup3rStr0ng!pass'),
    )
    await waitFor(() => expect(mockNavigate).toHaveBeenCalled())
  })

  it('keeps a forgot-password escape on the password screen', () => {
    render(<AuthScreen />)
    fireEvent.click(screen.getByTestId('auth-method-toggle'))

    expect(
      screen.getByRole('button', { name: /forgot password/i }),
    ).toBeInTheDocument()
  })

  it('goes back to the code path without leaving both designs on screen', () => {
    render(<AuthScreen />)
    fireEvent.click(screen.getByTestId('auth-method-toggle'))
    fireEvent.click(screen.getByTestId('auth-method-toggle'))

    expect(screen.queryByLabelText(/^password$/i)).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: /^continue$/i })).toBeInTheDocument()
  })
})

describe('AuthScreen — preserved escapes', () => {
  it('still lets a user skip straight into onboarding as a guest', async () => {
    render(<AuthScreen />)
    fireEvent.click(screen.getByRole('button', { name: /skip for now/i }))

    await waitFor(() => expect(signInAnonymously).toHaveBeenCalledTimes(1))
  })

  it('keeps the OAuth providers on the default screen', () => {
    render(<AuthScreen />)

    expect(screen.getByTestId('oauth-button-github')).toBeInTheDocument()
    expect(screen.getByTestId('oauth-button-google')).toBeInTheDocument()
  })
})
