import { useState } from 'react'
import { useAuthStore, type LinkableProvider } from '@/store/authStore'
import { isSafeReturnTo } from '@/lib/returnTo'
import { validatePassword } from '../utils/passwordValidation'
import { OAuthButton } from './OAuthButton'
import { PasswordInput, ConfirmPasswordInput } from './PasswordInput'
import { AuthError, AuthDivider, AuthLegalLinks } from './AuthLayout'

/**
 * Every way to attach a real identity to an EXISTING anonymous account.
 *
 * This is deliberately not built on `AuthScreen`. That screen signs a user
 * IN — a new session, a different account — which for an anonymous user means
 * abandoning the account their chats and projects live on. The mechanic here is
 * linking:
 *   - email + password → `signUp`, which upgrades the anonymous user in place;
 *   - GitHub / Google / Apple → `linkIdentity`.
 *
 * It also has no "Skip for now". `AuthScreen`'s version calls
 * `signInAnonymously`, which CREATES the very session every caller of this form
 * is trying to escape — offering it here is a loop, not an escape hatch.
 *
 * Email is the prominent path because it is the only one that can complete
 * without leaving the page. OAuth redirects the whole window by nature, so it
 * carries `returnTo` to bring the user back; the email path never needs it.
 */

export interface LinkIdentityFormProps {
  /**
   * Called once the account carries a real identity. The caller decides what
   * that means — dismiss a modal, follow a returnTo, advance a step.
   */
  onLinked: () => void
  /**
   * Where the OAuth round-trip should land. Validated before use; an
   * off-origin value is dropped rather than handed to the provider. Unused by
   * the email path, which never leaves the page.
   */
  returnTo?: string
  /**
   * Supabase returns no session when email confirmation is on. By default this
   * form verifies the code inline. A caller that owns a fuller verification
   * screen can take over instead by passing this.
   */
  onVerificationRequired?: (email: string) => void
  /** Label for the email submit button. */
  submitLabel?: string
  autoFocus?: boolean
}

export function LinkIdentityForm({
  onLinked,
  returnTo,
  onVerificationRequired,
  submitLabel = 'Save my account',
  autoFocus = true,
}: LinkIdentityFormProps) {
  const {
    linkOAuthIdentity,
    signUp,
    sendEmailVerificationOTP,
    verifyEmailOTP,
  } = useAuthStore()

  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)
  // Which provider's link is starting, or null. Cleared only on failure — a
  // success takes the window to the provider.
  const [pendingProvider, setPendingProvider] = useState<LinkableProvider | null>(null)

  // Set once signUp comes back without a session: the address we are awaiting
  // a code for. Its presence switches this form to the verification step.
  const [awaitingCodeFor, setAwaitingCodeFor] = useState<string | null>(null)
  const [code, setCode] = useState('')

  const handleLink = async (provider: LinkableProvider) => {
    setError(null)
    // Only the clicked provider shows pending; a shared flag lights every
    // button at once, which reads as linking an account the user never picked.
    setPendingProvider(provider)
    setSubmitting(true)
    try {
      const state = isSafeReturnTo(returnTo)
        ? { source: 'link' as const, returnTo }
        : { source: 'link' as const }
      // Performs the redirect itself — control does not come back here on web.
      await linkOAuthIdentity(provider, state)
    } catch (err) {
      setError(err instanceof Error ? err.message : `Failed to link ${provider}`)
      setSubmitting(false)
      setPendingProvider(null)
    }
  }

  const handleEmailSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError(null)

    const passwordValidation = validatePassword(password)
    if (!passwordValidation.valid) {
      setError(
        `Password must meet all requirements: ${passwordValidation.unmetRequirements
          .map((r) => r.label.toLowerCase())
          .join(', ')}`,
      )
      return
    }
    if (password !== confirmPassword) {
      setError('Passwords do not match')
      return
    }

    setSubmitting(true)
    try {
      const { session } = await signUp(email, password)
      if (!session) {
        if (onVerificationRequired) {
          onVerificationRequired(email)
        } else {
          // signUp already sent the code; do not send a second one, which
          // would trip Supabase's rate limit before the user has typed.
          setAwaitingCodeFor(email)
        }
        setSubmitting(false)
        return
      }
      // Confirmation disabled: the email is live immediately.
      onLinked()
    } catch (err) {
      let message = 'Failed to save account'
      if (err instanceof Error) message = err.message
      if (message.includes('already registered') || message.includes('already in use')) {
        message =
          'That email is already attached to another account. Sign in with it instead, or use a different email.'
      }
      setError(message)
      setSubmitting(false)
    }
  }

  const handleVerify = async (e: React.FormEvent) => {
    e.preventDefault()
    setError(null)

    if (code.length !== 6) {
      setError('Verification code must be 6 digits.')
      return
    }

    setSubmitting(true)
    try {
      await verifyEmailOTP(code, awaitingCodeFor ?? email)
    } catch (err) {
      let message = 'Failed to verify code. Please try again.'
      if (err instanceof Error) {
        if (err.message.includes('expired')) {
          message = 'Verification code has expired. Please request a new one.'
        } else if (err.message.includes('invalid')) {
          message = 'Invalid verification code. Please check and try again.'
        } else {
          message = err.message || message
        }
      }
      setError(message)
      setSubmitting(false)
      return
    }
    setSubmitting(false)
    onLinked()
  }

  const handleResend = async () => {
    setError(null)
    try {
      await sendEmailVerificationOTP(awaitingCodeFor ?? email)
    } catch (err) {
      setError(
        err instanceof Error ? err.message : 'Failed to resend the code.',
      )
    }
  }

  if (awaitingCodeFor) {
    return (
      <form className="space-y-5" onSubmit={handleVerify}>
        {error && <AuthError message={error} />}

        <p className="text-sm text-muted-foreground">
          Enter the 6-digit code we sent to{' '}
          <span className="font-medium text-foreground">{awaitingCodeFor}</span>.
        </p>

        <div>
          <label htmlFor="link-identity-code" className="block text-sm font-medium mb-1.5">
            Verification code
          </label>
          <input
            id="link-identity-code"
            name="code"
            type="text"
            inputMode="numeric"
            autoComplete="one-time-code"
            autoFocus
            value={code}
            onChange={(e) => {
              setCode(e.target.value.replace(/\D/g, '').slice(0, 6))
              if (error) setError(null)
            }}
            disabled={submitting}
            maxLength={6}
            placeholder="000000"
            className="block w-full px-3 py-2.5 border border-border rounded-lg bg-transparent text-center text-2xl tracking-widest focus:outline-none focus:ring-2 focus:ring-primary focus:border-primary transition-colors"
          />
        </div>

        <button
          type="submit"
          disabled={submitting || code.length !== 6}
          className="w-full flex justify-center py-2.5 px-4 border border-border rounded-lg text-sm font-medium text-primary-foreground bg-primary hover:bg-primary/90 focus:outline-none focus:ring-2 focus:ring-primary disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
        >
          {submitting ? 'Verifying...' : 'Verify and continue'}
        </button>

        <div className="text-center">
          <button
            type="button"
            onClick={() => void handleResend()}
            disabled={submitting}
            className="text-sm text-muted-foreground hover:text-foreground transition-colors disabled:opacity-50"
          >
            Resend the code
          </button>
        </div>
      </form>
    )
  }

  return (
    <form className="space-y-5" onSubmit={handleEmailSubmit} autoComplete="on">
      {error && <AuthError message={error} />}

      <div className="space-y-4">
        <div>
          <label htmlFor="link-identity-email" className="block text-sm font-medium mb-1.5">
            Email address
          </label>
          <input
            id="link-identity-email"
            name="email"
            type="email"
            autoComplete="username email"
            autoFocus={autoFocus}
            required
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            disabled={submitting}
            className="block w-full px-3 py-2.5 border border-border rounded-lg bg-transparent focus:outline-none focus:ring-2 focus:ring-primary focus:border-primary transition-colors"
            placeholder="you@example.com"
          />
        </div>

        <PasswordInput
          id="link-identity-password"
          name="password"
          label="Password"
          value={password}
          onChange={setPassword}
          autoComplete="new-password"
          required
          disabled={submitting}
          showStrengthIndicator
          showRequirements
        />

        <ConfirmPasswordInput
          id="link-identity-confirm-password"
          name="confirmPassword"
          label="Confirm Password"
          value={confirmPassword}
          password={password}
          onChange={setConfirmPassword}
          autoComplete="new-password"
          required
          disabled={submitting}
        />
      </div>

      <button
        type="submit"
        disabled={submitting}
        className="w-full flex justify-center py-2.5 px-4 border border-border rounded-lg text-sm font-medium text-primary-foreground bg-primary hover:bg-primary/90 focus:outline-none focus:ring-2 focus:ring-primary disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
      >
        {submitting ? 'Processing...' : submitLabel}
      </button>

      <AuthDivider label="Or attach a provider" />

      <div className="space-y-3">
        <OAuthButton
          provider="github"
          onClick={() => void handleLink('github')}
          loading={pendingProvider === 'github'}
          disabled={submitting}
        />
        <OAuthButton
          provider="google"
          onClick={() => void handleLink('google')}
          loading={pendingProvider === 'google'}
          disabled={submitting}
        />
      </div>

      <AuthLegalLinks />
    </form>
  )
}
