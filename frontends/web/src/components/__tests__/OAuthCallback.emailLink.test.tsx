/**
 * /auth/callback as the landing pad for BOTH shapes of identity round-trip.
 *
 * The user in the reported bug never got a code. Supabase sent a link, they
 * clicked it, and came back into the app still anonymous — because the
 * callback understood only an OAuth `code` and the email path had nowhere good
 * to land anyway (`emailRedirectTo: undefined`).
 *
 * Which shape arrives is not ours to choose. The email template lives in the
 * hosted Supabase dashboard, outside this repo — nobody here can read it,
 * version it, or stop it being reset to the stock `{{ .ConfirmationURL }}`. So
 * the callback accepts:
 *
 *   - `code`                — OAuth, and any email link under the PKCE flow
 *   - `token_hash` + `type` — a non-PKCE confirmation link
 *
 * Handling only one would leave a flow that breaks silently on a dashboard
 * change nobody connects to the outage.
 *
 * The returnTo assertions are the ones that make this feel FIXED rather than
 * merely successful: onboarding's entire state is the `plan` search param, so
 * a callback that lands on "/" drops the user at step one having already
 * answered everything.
 */

import { render, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const mockNavigate = vi.fn()
let mockSearch: Record<string, unknown> = {}

vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => mockNavigate,
  useSearch: () => mockSearch,
}))

const exchangeCodeForSessionMock = vi.fn()
const verifyOtpMock = vi.fn()

vi.mock('@/lib/supabase', () => ({
  supabase: {
    auth: {
      exchangeCodeForSession: exchangeCodeForSessionMock,
      verifyOtp: verifyOtpMock,
    },
  },
}))

const setUser = vi.fn()
const setSession = vi.fn()

vi.mock('@/store/authStore', () => ({
  useAuthStore: () => ({ setUser, setSession }),
}))

vi.mock('@/lib/logger', () => ({
  logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

const { OAuthCallback } = await import('../OAuthCallback')

/** The account the user is upgrading. Same id before and after — that is the point. */
const ANON_USER_ID = 'anon-user-0001'

const LINKED = {
  user: { id: ANON_USER_ID, is_anonymous: false, email: 'someone@example.com' },
  session: { access_token: 'fresh' },
}

/** The live onboarding URL, whose `plan` param IS the wizard's entire state. */
const ONBOARDING_URL = '/onboarding?plan=eyJzdGVwIjoiY2hlY2tvdXQifQ'

beforeEach(() => {
  vi.clearAllMocks()
  mockSearch = {}
  exchangeCodeForSessionMock.mockResolvedValue({ data: LINKED, error: null })
  verifyOtpMock.mockResolvedValue({ data: LINKED, error: null })
})

describe('OAuthCallback — email confirmation link', () => {
  it('completes a token_hash link by verifying it', async () => {
    mockSearch = { token_hash: 'pkce_abc123', type: 'email_change' }

    render(<OAuthCallback />)

    await waitFor(() => expect(verifyOtpMock).toHaveBeenCalled())
    // The hash IS the credential, so no email accompanies it — GoTrue rejects
    // a token_hash sent alongside an email and looks the user up from the hash.
    expect(verifyOtpMock).toHaveBeenCalledWith({
      token_hash: 'pkce_abc123',
      type: 'email_change',
    })
    expect(exchangeCodeForSessionMock).not.toHaveBeenCalled()
  })

  it('adopts the session from a link, so the user comes back NON-anonymous', async () => {
    mockSearch = { token_hash: 'pkce_abc123', type: 'email_change' }

    render(<OAuthCallback />)

    // The reported symptom was returning still-anonymous and hitting the same
    // modal. Setting the verified session is what breaks that loop.
    await waitFor(() => expect(setUser).toHaveBeenCalledWith(LINKED.user))
    expect(setSession).toHaveBeenCalledWith(LINKED.session)
  })

  it('lands the user back on the onboarding URL they left, not the app root', async () => {
    mockSearch = {
      token_hash: 'pkce_abc123',
      type: 'email_change',
      returnTo: ONBOARDING_URL,
    }

    render(<OAuthCallback />)

    await waitFor(() => expect(mockNavigate).toHaveBeenCalled())
    // `href` preserves the query string verbatim. Navigating `to: '/'` here
    // would technically complete the link and still leave the user re-answering
    // the whole wizard.
    expect(mockNavigate).toHaveBeenCalledWith({ href: ONBOARDING_URL })
  })

  it('refuses an off-origin returnTo and falls back to the app root', async () => {
    // `//evil.example.com` is protocol-relative: a browser reads it as another
    // origin, so a `startsWith('/')` check alone waves it straight through.
    mockSearch = {
      token_hash: 'pkce_abc123',
      type: 'email_change',
      returnTo: '//evil.example.com/steal',
    }

    render(<OAuthCallback />)

    await waitFor(() => expect(mockNavigate).toHaveBeenCalled())
    expect(mockNavigate).toHaveBeenCalledWith({ to: '/', search: {} })
    expect(mockNavigate).not.toHaveBeenCalledWith({ href: '//evil.example.com/steal' })
  })

  it('surfaces a rejected link instead of silently returning the user anonymous', async () => {
    verifyOtpMock.mockResolvedValue({
      data: { user: null, session: null },
      error: Object.assign(new Error('Token has expired or is invalid'), {
        code: 'otp_expired',
      }),
    })
    mockSearch = { token_hash: 'pkce_expired', type: 'email_change' }

    const { findByText } = render(<OAuthCallback />)

    // Rendered through describeAuthError, so an expired token becomes an
    // instruction ("send yourself a new one") rather than a bare status.
    expect(await findByText(/expired/i)).toBeInTheDocument()
    expect(setSession).not.toHaveBeenCalled()
  })
})

describe('OAuthCallback — a stale PKCE code degrades to an instruction', () => {
  it('never shows the raw pkce_code_verifier_not_found', async () => {
    // The owner's exact failure. The code verifier lives in the browser that
    // STARTED the flow; they clicked the link in their mail client, so the
    // exchange could not succeed and the app printed the bare code at them.
    exchangeCodeForSessionMock.mockResolvedValue({
      data: { user: null, session: null },
      error: Object.assign(new Error('code verifier not found'), {
        code: 'pkce_code_verifier_not_found',
      }),
    })
    mockSearch = { code: 'stale-pkce-code' }

    const { findByText, queryByText } = render(<OAuthCallback />)

    expect(await findByText(/6-digit code/i)).toBeInTheDocument()
    expect(queryByText(/pkce_code_verifier_not_found/i)).not.toBeInTheDocument()
    expect(setSession).not.toHaveBeenCalled()
  })
})

describe('OAuthCallback — the OAuth code path still works', () => {
  it('prefers the code when one is present', async () => {
    // GoTrue's own /verify redirect appends `code` under PKCE, so a link can
    // arrive carrying both. Exchanging the code is the path that also yields a
    // session in one hop.
    mockSearch = { code: 'oauth-code', token_hash: 'pkce_abc', type: 'email_change' }

    render(<OAuthCallback />)

    await waitFor(() => expect(exchangeCodeForSessionMock).toHaveBeenCalledWith('oauth-code'))
    expect(verifyOtpMock).not.toHaveBeenCalled()
  })

  it('still honors returnTo on the code path', async () => {
    mockSearch = { code: 'oauth-code', returnTo: ONBOARDING_URL }

    render(<OAuthCallback />)

    await waitFor(() => expect(mockNavigate).toHaveBeenCalledWith({ href: ONBOARDING_URL }))
  })

  it('reports a callback carrying neither credential', async () => {
    mockSearch = {}

    const { findByText } = render(<OAuthCallback />)

    expect(await findByText(/Invalid authentication callback/i)).toBeInTheDocument()
  })
})
