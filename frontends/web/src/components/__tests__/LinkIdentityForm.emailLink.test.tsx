/**
 * The email half of LinkIdentityForm — the call it makes, and the promise it
 * makes to the user.
 *
 * Two failures are pinned here, and the second is the one that trapped people:
 *
 *  1. It must LINK, via the store's `linkEmailIdentity` (updateUser), not
 *     `signUp`. signUp leaves the anonymous account untouched, so the user
 *     returns from their email still anonymous and hits the same "Finish
 *     setting up your account" modal.
 *
 *  2. It must not promise a code it cannot know exists. The Supabase email
 *     template lives in the hosted dashboard, outside this repo. A form that
 *     says "Enter the 6-digit code we sent" and offers only a code input is a
 *     dead end for every user whose email contained a link instead — which is
 *     what the owner actually received.
 */

import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => vi.fn(),
  useSearch: () => ({}),
}))

vi.mock('@/lib/logger', () => ({
  logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

const linkOAuthIdentity = vi.fn().mockResolvedValue(undefined)
const linkEmailIdentity = vi.fn()
const verifyEmailIdentityOTP = vi.fn().mockResolvedValue(undefined)
const sendEmailIdentityVerification = vi.fn().mockResolvedValue(undefined)
const signUp = vi.fn()

/** Mutable so a test can put the store into the half-upgraded state. */
let mockUser: Record<string, unknown> = { is_anonymous: true }

vi.mock('@/store/authStore', () => ({
  useAuthStore: () => ({
    user: mockUser,
    linkOAuthIdentity,
    linkEmailIdentity,
    verifyEmailIdentityOTP,
    sendEmailIdentityVerification,
    signUp,
  }),
}))

const { LinkIdentityForm } = await import('../LinkIdentityForm')

/** The live onboarding URL — its `plan` param is the wizard's entire state. */
const ONBOARDING_URL = '/onboarding?plan=eyJzdGVwIjoiY2hlY2tvdXQifQ'

function fillEmailForm() {
  fireEvent.change(screen.getByLabelText(/Email address/i), {
    target: { value: 'someone@example.com' },
  })
  fireEvent.change(screen.getByLabelText('Password'), {
    target: { value: 'Sup3rSecret!pass' },
  })
  fireEvent.change(screen.getByLabelText(/Confirm Password/i), {
    target: { value: 'Sup3rSecret!pass' },
  })
}

/** Submit the email form and wait for the verification step to appear. */
async function reachVerificationStep(returnTo?: string) {
  render(<LinkIdentityForm onLinked={vi.fn()} returnTo={returnTo} />)
  fillEmailForm()
  fireEvent.click(screen.getByRole('button', { name: /Save my account/i }))
  await screen.findByLabelText(/Verification code/i)
}

beforeEach(() => {
  vi.clearAllMocks()
  mockUser = { is_anonymous: true }
  linkEmailIdentity.mockResolvedValue({
    user: { id: 'anon-user-0001' },
    verificationRequired: true,
  })
})

describe('LinkIdentityForm — the email path links rather than signing up', () => {
  it('calls linkEmailIdentity, never signUp', async () => {
    render(<LinkIdentityForm onLinked={vi.fn()} />)
    fillEmailForm()
    fireEvent.click(screen.getByRole('button', { name: /Save my account/i }))

    await waitFor(() => expect(linkEmailIdentity).toHaveBeenCalled())
    // signUp is what created a second account and stranded the user's chats,
    // org and coupons on the abandoned anonymous one.
    expect(signUp).not.toHaveBeenCalled()
  })

  it('threads returnTo into the emailed link, so a clicked link comes back here', async () => {
    render(<LinkIdentityForm onLinked={vi.fn()} returnTo={ONBOARDING_URL} />)
    fillEmailForm()
    fireEvent.click(screen.getByRole('button', { name: /Save my account/i }))

    await waitFor(() => expect(linkEmailIdentity).toHaveBeenCalled())
    // The email path used to be documented as "never needs returnTo" because
    // it completed on the page. It no longer always does — the user may finish
    // by clicking a link, which returns through /auth/callback.
    expect(linkEmailIdentity).toHaveBeenCalledWith(
      'someone@example.com',
      'Sup3rSecret!pass',
      { source: 'link', returnTo: ONBOARDING_URL },
    )
  })

  it('drops an off-origin returnTo rather than embedding it in an email', async () => {
    render(<LinkIdentityForm onLinked={vi.fn()} returnTo="//evil.example.com/steal" />)
    fillEmailForm()
    fireEvent.click(screen.getByRole('button', { name: /Save my account/i }))

    await waitFor(() => expect(linkEmailIdentity).toHaveBeenCalled())
    expect(linkEmailIdentity).toHaveBeenCalledWith(
      'someone@example.com',
      'Sup3rSecret!pass',
      { source: 'link' },
    )
  })

  it('does not report success while the address is still unconfirmed', async () => {
    const onLinked = vi.fn()
    render(<LinkIdentityForm onLinked={onLinked} />)
    fillEmailForm()
    fireEvent.click(screen.getByRole('button', { name: /Save my account/i }))

    await screen.findByLabelText(/Verification code/i)
    // updateUser issues no new session, so "we have a session" is true both
    // before and after. Reading that as linked is how the old code concluded
    // it had upgraded an account it had not touched.
    expect(onLinked).not.toHaveBeenCalled()
  })

  it('completes immediately when the project has confirmations off', async () => {
    linkEmailIdentity.mockResolvedValue({
      user: { id: 'anon-user-0001', email: 'someone@example.com' },
      verificationRequired: false,
    })
    const onLinked = vi.fn()
    render(<LinkIdentityForm onLinked={onLinked} />)
    fillEmailForm()
    fireEvent.click(screen.getByRole('button', { name: /Save my account/i }))

    await waitFor(() => expect(onLinked).toHaveBeenCalled())
  })

  it('keeps the "already attached to another account" message', async () => {
    linkEmailIdentity.mockRejectedValue(
      new Error('A user with this email address has already been registered'),
    )
    render(<LinkIdentityForm onLinked={vi.fn()} />)
    fillEmailForm()
    fireEvent.click(screen.getByRole('button', { name: /Save my account/i }))

    expect(
      await screen.findByText(/already attached to another account/i),
    ).toBeInTheDocument()
  })
})

describe('LinkIdentityForm — the verification step is honest about the email', () => {
  it('tells the user a link works too, not only a code', async () => {
    await reachVerificationStep()

    // The old copy promised "Enter the 6-digit code we sent" and offered only
    // a code input. For the owner, whose email contained a link and no code,
    // that was a dead end with nothing to type.
    expect(screen.getByText(/click the link in it/i)).toBeInTheDocument()
  })

  it('does not promise a code that may not be in the email', async () => {
    await reachVerificationStep()

    expect(screen.queryByText(/Enter the 6-digit code we sent/i)).not.toBeInTheDocument()
  })

  it('still verifies a typed 6-digit code', async () => {
    const onLinked = vi.fn()
    render(<LinkIdentityForm onLinked={onLinked} />)
    fillEmailForm()
    fireEvent.click(screen.getByRole('button', { name: /Save my account/i }))

    const codeInput = await screen.findByLabelText(/Verification code/i)
    fireEvent.change(codeInput, { target: { value: '123456' } })
    fireEvent.click(screen.getByRole('button', { name: /Verify/i }))

    // Both paths must work: the dashboard template may include {{ .Token }},
    // and supporting only the link would break the day it does.
    await waitFor(() =>
      expect(verifyEmailIdentityOTP).toHaveBeenCalledWith('123456', 'someone@example.com'),
    )
    await waitFor(() => expect(onLinked).toHaveBeenCalled())
  })

  it('resends with the same returnTo, so a second link lands in the same place', async () => {
    await reachVerificationStep(ONBOARDING_URL)

    fireEvent.click(screen.getByRole('button', { name: /Resend the email/i }))

    await waitFor(() => expect(sendEmailIdentityVerification).toHaveBeenCalled())
    expect(sendEmailIdentityVerification).toHaveBeenCalledWith('someone@example.com', {
      source: 'link',
      returnTo: ONBOARDING_URL,
    })
  })

  it('confirms the resend actually happened', async () => {
    await reachVerificationStep()

    fireEvent.click(screen.getByRole('button', { name: /Resend the email/i }))

    // The old button reported nothing at all, which is indistinguishable from
    // the endpoint that returned 200 and mailed nothing.
    expect(await screen.findByText(/sent another confirmation email/i)).toBeInTheDocument()
  })

  it('renders a rate-limited resend as a wait, not as a raw code', async () => {
    sendEmailIdentityVerification.mockRejectedValueOnce(
      Object.assign(
        new Error('For security purposes, you can only request this after 54 seconds.'),
        { code: 'over_email_send_rate_limit', status: 429 },
      ),
    )
    await reachVerificationStep()

    fireEvent.click(screen.getByRole('button', { name: /Resend the email/i }))

    expect(await screen.findByText(/54 seconds/)).toBeInTheDocument()
    expect(screen.queryByText(/over_email_send_rate_limit/)).not.toBeInTheDocument()
  })
})

/**
 * The owner's live state: password set, email never confirmed. Everything here
 * is about them being able to FINISH rather than being asked to start over.
 */
describe('LinkIdentityForm — an account already half-upgraded', () => {
  it('opens straight on verification for the address already pending', async () => {
    // GoTrue serialises users.email_change as new_email. Its presence means
    // the upgrade is half-done.
    mockUser = { is_anonymous: true, new_email: 'someone@example.com' }

    render(<LinkIdentityForm onLinked={vi.fn()} />)

    // Not the blank email+password form — re-submitting that is exactly what
    // produced `422 same_password` and told the user their password was wrong
    // for an account they didn't believe they had.
    expect(await screen.findByLabelText(/Verification code/i)).toBeInTheDocument()
    expect(screen.queryByLabelText('Password')).not.toBeInTheDocument()
    expect(screen.getByText('someone@example.com')).toBeInTheDocument()
  })

  it('says the password is already set, so nothing needs re-entering', async () => {
    mockUser = { is_anonymous: true, new_email: 'someone@example.com' }

    render(<LinkIdentityForm onLinked={vi.fn()} />)

    expect(
      await screen.findByText(/already started setting up this account/i),
    ).toBeInTheDocument()
    expect(screen.getByText(/nothing to re-enter/i)).toBeInTheDocument()
  })

  it('lets them resend from that state without re-entering anything', async () => {
    mockUser = { is_anonymous: true, new_email: 'someone@example.com' }

    render(<LinkIdentityForm onLinked={vi.fn()} returnTo={ONBOARDING_URL} />)
    fireEvent.click(await screen.findByRole('button', { name: /Resend the email/i }))

    await waitFor(() => expect(sendEmailIdentityVerification).toHaveBeenCalled())
    // Uses the PENDING address from the store, not an empty form field.
    expect(sendEmailIdentityVerification).toHaveBeenCalledWith('someone@example.com', {
      source: 'link',
      returnTo: ONBOARDING_URL,
    })
  })

  it('lets them finish by typing the code from the email they already have', async () => {
    mockUser = { is_anonymous: true, new_email: 'someone@example.com' }
    const onLinked = vi.fn()

    render(<LinkIdentityForm onLinked={onLinked} />)
    fireEvent.change(await screen.findByLabelText(/Verification code/i), {
      target: { value: '123456' },
    })
    fireEvent.click(screen.getByRole('button', { name: /Verify/i }))

    await waitFor(() =>
      expect(verifyEmailIdentityOTP).toHaveBeenCalledWith('123456', 'someone@example.com'),
    )
    await waitFor(() => expect(onLinked).toHaveBeenCalled())
  })
})

describe('LinkIdentityForm — a rate-limited first submit is not a failure', () => {
  it('moves to verification and explains the wait', async () => {
    linkEmailIdentity.mockRejectedValueOnce(
      Object.assign(
        new Error('For security purposes, you can only request this after 54 seconds.'),
        { code: 'over_email_send_rate_limit', status: 429 },
      ),
    )
    render(<LinkIdentityForm onLinked={vi.fn()} />)
    fillEmailForm()
    fireEvent.click(screen.getByRole('button', { name: /Save my account/i }))

    // The update was accepted; only the mail send was throttled. Leaving the
    // user on the form implies nothing happened and invites the retry that
    // dead-ends.
    expect(await screen.findByLabelText(/Verification code/i)).toBeInTheDocument()
    expect(screen.getByText(/54 seconds/)).toBeInTheDocument()
  })
})
