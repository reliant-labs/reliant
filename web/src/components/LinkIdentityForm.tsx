import { useState } from 'react'
import { useAuthStore, type LinkableProvider } from '@/store/authStore'
import { isSafeReturnTo } from '@/lib/returnTo'
import { describeAuthError, isRateLimitError } from '@/lib/authErrors'
import { validatePassword } from '../utils/passwordValidation'
import { OAuthButton } from './OAuthButton'
import { PasswordInput, ConfirmPasswordInput } from './PasswordInput'
import { AuthError, AuthDivider, AuthLegalLinks } from './AuthLayout'
import { CheckSpamNote } from './CheckSpamNote'

/**
 * Every way to attach a real identity to an EXISTING anonymous account.
 *
 * This is deliberately not built on `AuthScreen`. That screen signs a user
 * IN — a new session, a different account — which for an anonymous user means
 * abandoning the account their chats and projects live on. The mechanic here is
 * linking:
 *   - email + password → `updateUser`, which upgrades the anonymous user in
 *     place (via the store's `linkEmailIdentity`);
 *   - GitHub / Google / Apple → `linkIdentity`.
 *
 * This used to say "email + password → `signUp`, which upgrades the anonymous
 * user in place". That was FALSE, and it was the origin of a loop users could
 * not escape: `signUp` hits /signup, which mints a separate user or returns
 * one with no session, so the email never attached to the anonymous account.
 * The user confirmed their address, came back still anonymous, and hit the
 * same "Finish setting up your account" modal — forever. `updateUser` is the
 * call that upgrades a session in place.
 *
 * It also has no "Skip for now". `AuthScreen`'s version calls
 * `signInAnonymously`, which CREATES the very session every caller of this form
 * is trying to escape — offering it here is a loop, not an escape hatch.
 *
 * Email is the prominent path because it is the only one that CAN complete
 * without leaving the page. It no longer always does: Supabase may send a
 * 6-digit code or a link, depending on a dashboard template this repo cannot
 * read, so the email path now carries `returnTo` as well — if the user
 * completes via the link, /auth/callback needs it to put them back where they
 * were standing.
 */

export interface LinkIdentityFormProps {
  /**
   * Called once the account carries a real identity. The caller decides what
   * that means — dismiss a modal, follow a returnTo, advance a step.
   */
  onLinked: () => void
  /**
   * Where a round-trip should land. Validated before use; an off-origin value
   * is dropped rather than handed to the provider or embedded in an email
   * link. Used by BOTH paths now: OAuth redirects the window, and the email
   * path may complete by the user clicking a link in their inbox, which
   * returns through /auth/callback the same way.
   */
  returnTo?: string
  /**
   * Supabase returns no session until the new address is confirmed. By default
   * this form verifies inline. A caller that owns a fuller verification screen
   * can take over instead by passing this.
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
    user,
    linkOAuthIdentity,
    linkEmailIdentity,
    sendEmailIdentityVerification,
    verifyEmailIdentityOTP,
  } = useAuthStore()

  // The round-trip state both paths share. Computed once so the email link and
  // the OAuth redirect cannot disagree about where the user came from.
  const linkState = isSafeReturnTo(returnTo)
    ? { source: 'link' as const, returnTo }
    : { source: 'link' as const }

  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)
  // Which provider's link is starting, or null. Cleared only on failure — a
  // success takes the window to the provider.
  const [pendingProvider, setPendingProvider] = useState<LinkableProvider | null>(null)

  // Set once the email is pending confirmation: the address we are waiting on.
  // Its presence switches this form to the verification step. The user may
  // finish there by typing a code, or by clicking the link in the same email —
  // in which case they return through /auth/callback and never come back to
  // this component at all.
  //
  // It SEEDS from `user.new_email`, which is how someone already stuck gets
  // out. GoTrue serialises `users.email_change` as `new_email`, so its
  // presence means "this account has a half-finished upgrade: the password
  // landed, the address never got confirmed". Without this seed such a user
  // was shown the blank email+password form again — and re-submitting it is
  // exactly what produced the `422 same_password` dead end. Now they land
  // directly on the verification step for the address already pending.
  const [awaitingCodeFor, setAwaitingCodeFor] = useState<string | null>(
    user?.new_email ?? null,
  )
  const [code, setCode] = useState('')
  // A non-error status line: "we sent it", or "wait N seconds". Kept apart
  // from `error` because a rate limit is not a failure the user caused, and
  // rendering it in red beside "Authentication Failed" is what made a normal
  // cooldown read as a broken account.
  const [notice, setNotice] = useState<string | null>(null)

  // True when this form opened straight onto the verification step because the
  // account already had a pending address — i.e. the user is the one already
  // trapped, not someone who just submitted the form.
  const resumedPending = !!user?.new_email && awaitingCodeFor === user.new_email

  const handleLink = async (provider: LinkableProvider) => {
    setError(null)
    // Only the clicked provider shows pending; a shared flag lights every
    // button at once, which reads as linking an account the user never picked.
    setPendingProvider(provider)
    setSubmitting(true)
    try {
      // Performs the redirect itself — control does not come back here on web.
      await linkOAuthIdentity(provider, linkState)
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
      // updateUser under the hood: the email attaches to the CURRENT user id,
      // so the org, redeemed coupons and onboarding progress all survive.
      // `linkState` rides along as the emailed link's redirect target.
      const { verificationRequired } = await linkEmailIdentity(
        email,
        password,
        linkState,
      )
      if (verificationRequired) {
        if (onVerificationRequired) {
          onVerificationRequired(email)
        } else {
          // The update already sent the email; do not send a second one, which
          // would trip Supabase's rate limit before the user has typed.
          setAwaitingCodeFor(email)
        }
        setSubmitting(false)
        return
      }
      // Confirmation disabled on the project: the email is live immediately.
      onLinked()
    } catch (err) {
      // describeAuthError owns every code→copy mapping, including the
      // "already attached to another account" wording this used to inline and
      // the rate limit that used to surface as a raw string.
      //
      // A rate limit here is NOT a failure to save the account: the update was
      // accepted and only the mail send was throttled, so the user should be
      // moved to the verification step (where they can wait and resend) rather
      // than left staring at the form wondering if anything happened.
      if (isRateLimitError(err)) {
        setNotice(describeAuthError(err, 'Please wait a moment and try again.'))
        setAwaitingCodeFor(email)
        setSubmitting(false)
        return
      }
      setError(describeAuthError(err, 'Failed to save account'))
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
      await verifyEmailIdentityOTP(code, awaitingCodeFor ?? email)
    } catch (err) {
      setError(describeAuthError(err, 'Failed to verify code. Please try again.'))
      setSubmitting(false)
      return
    }
    setSubmitting(false)
    onLinked()
  }

  const handleResend = async () => {
    setError(null)
    setNotice(null)
    const target = awaitingCodeFor ?? email
    try {
      // Goes through updateUser, not supabase.auth.resend — see the store. A
      // `resend` call cannot find a user whose address is still pending, so it
      // returned 200 and mailed nothing, which is the "resend button does
      // nothing" the owner reported.
      await sendEmailIdentityVerification(target, linkState)
      setNotice(`We sent another confirmation email to ${target}.`)
    } catch (err) {
      // A throttle is expected here and is not the user's fault; say how long
      // to wait rather than colouring it as a failure.
      if (isRateLimitError(err)) {
        setNotice(describeAuthError(err, 'Please wait a moment and try again.'))
        return
      }
      setError(describeAuthError(err, 'Failed to resend the email.'))
    }
  }

  if (awaitingCodeFor) {
    return (
      <form className="space-y-5" onSubmit={handleVerify}>
        {error && <AuthError message={error} />}

        {/* Status, not failure: "we sent another one", or "wait N seconds".
            A cooldown shown in the red error box reads as a broken account. */}
        {notice && (
          <div className="rounded-lg bg-muted border border-border p-4">
            <p className="text-sm text-muted-foreground">{notice}</p>
          </div>
        )}

        {/* The account was ALREADY mid-upgrade when this form mounted — we
            seeded from user.new_email. Say so explicitly: this user has been
            told once already that their password was wrong for an account
            they didn't think they had, so "we're still waiting on the email
            you started earlier" is the sentence that ends that confusion. */}
        {resumedPending && (
          <p className="text-sm text-muted-foreground">
            You already started setting up this account — we’re just waiting on
            your email address to be confirmed. Your password is set; there’s
            nothing to re-enter.
          </p>
        )}

        {/* Honest about what we cannot know. The email template lives in the
            hosted Supabase dashboard, so this code cannot tell whether the
            message contains a 6-digit code, a link, or both — and promising a
            code that is not there is what left users stuck at this step with
            no way forward. Both routes finish the same link: the code below,
            or the link, which returns through /auth/callback. */}
        <p className="text-sm text-muted-foreground">
          We sent a confirmation email to{' '}
          <span className="font-medium text-foreground">{awaitingCodeFor}</span>.
          Click the link in it, or enter the 6-digit code below if the email has
          one — either finishes setting up your account.
        </p>
        <CheckSpamNote className="text-xs text-muted-foreground" />

        <div>
          <label htmlFor="link-identity-code" className="block text-sm font-medium mb-1.5">
            Verification code{' '}
            <span className="font-normal text-muted-foreground">
              (if your email has one)
            </span>
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
            Resend the email
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
